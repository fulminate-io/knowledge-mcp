// SPDX-License-Identifier: Apache-2.0

package bm25

import (
	"encoding/binary"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// merge_blockindex_test.go — the merged blob's BLOCK INDEX, which is the structure
// the streaming writer used to overwrite with zeros.
//
// WHY A DEDICATED STRUCTURAL PROBE BESIDE ValidateSegment. ValidateSegment is the
// authority and every row here asserts it, but it reports the FIRST violation it
// reaches as a reader's error. badBlockIdx names every (field, block) whose entry is
// wrong, which is what makes a sweep's failure legible: a shape that damages three
// fields and one that damages one are different facts about the writer.
//
// THE DETECTOR'S OWN KNOWN POSITIVE is the preserved incident artifact
// (testdata/corrupt-posting-run.seg): TestBadBlockIdxDetectsThePreservedArtifact
// runs the same function over bytes that are known corrupt and requires a hit, so a
// clean sweep is a measurement rather than a detector that cannot fire.

// badBlockIdx reports every (field, block) whose block-index entry is zero or does
// not address a block payload inside the blob.
//
// A ZERO ENTRY IS THE INCIDENT'S OWN SHAPE: mapped_dict.go's scanBlock resolves the
// block payload at the entry's value, so a zero makes it read the v2 header as
// {postOff, postCount} — which is where "posting run of N at offset 327938" comes
// from (327938 is version 2 | dictBlocked<<8 | fieldCount 5<<16 read as a u32).
func badBlockIdx(blob []byte) []string {
	var out []string
	n := len(blob)
	fieldCount := int(binary.LittleEndian.Uint16(blob[v2HdrFieldCount:]))
	fieldTable := int(binary.LittleEndian.Uint32(blob[v2HdrFieldTable:]))
	for f := range fieldCount {
		entry := fieldTable + v2FieldEntrySize*f
		dict := int(binary.LittleEndian.Uint32(blob[entry+v2FldDict:]))
		blocks := int(binary.LittleEndian.Uint32(blob[dict+4:]))
		blockIdx := int(binary.LittleEndian.Uint32(blob[dict+12:]))
		for b := range blocks {
			at := int(binary.LittleEndian.Uint32(blob[blockIdx+4*b:]))
			if at == 0 || at+8 > n {
				out = append(out, fmt.Sprintf("field=%d block=%d/%d blockIdxOff=%d entry=%d", f, b, blocks, blockIdx, at))
			}
		}
	}
	return out
}

// shapeDocs builds nDocs documents whose DISTINCT term count per field is exactly
// what perField names, so a shape's block count per field is arithmetic rather than
// an estimate: blocks = ceil(terms / blockedBlockTerms).
//
// EVERY CONSTITUENT SHARES THE VOCABULARY, which is what makes the merged
// dictionary's term count equal the per-field count regardless of how many
// constituents are merged — and what makes the k-way merge coalesce real posting
// runs instead of concatenating disjoint dictionaries.
//
// TWO DETAILS ARE PRODUCTION SHAPE RATHER THAN DECORATION, and the reproduction
// needs both: the member id is 64 characters, the length of a real content-addressed
// node id, because member bytes sit in the fixed prefix and move every dictionary
// after them; and `keywords` carries per-document terms unless the caller names it,
// because a field whose terms differ per document keeps a second dictionary stream
// live while the others are being patched. A fixture of short ids and four identical
// fields lays the prefix out differently and misses the defect entirely — measured:
// the same sweep over such a fixture reported zero bad blobs on the unfixed writer.
func shapeDocs(idPrefix string, nDocs int, perField map[string]int) []searchengine.Document {
	docs := make([]searchengine.Document, nDocs)
	for d := range docs {
		fields := make(map[string]string, len(perField)+1)
		for name, terms := range perField {
			var sb strings.Builder
			for i := range terms {
				// THE TERM WIDTHS ARE THE INCIDENT'S OWN: a 9-byte symbol_name term
				// beside 8-byte text terms. Term bytes are appended to the tail, so
				// their widths move every later offset — and the defect is an
				// offset-arithmetic one. A fixture that widened them uniformly laid
				// the blob out differently and did not reproduce (measured).
				if name == searchengine.FieldSymbolName {
					fmt.Fprintf(&sb, "sym%06d ", i)
				} else {
					fmt.Fprintf(&sb, "word%04d ", i)
				}
			}
			fields[name] = sb.String()
		}
		if _, named := perField[searchengine.FieldKeywords]; !named {
			fields[searchengine.FieldKeywords] = fmt.Sprintf("kw%d kw%d", d, d+1)
		}
		docs[d] = searchengine.Document{
			ID:     fmt.Sprintf("%s%0*d", idPrefix, 64-len(idPrefix), d),
			Fields: fields,
		}
	}
	return docs
}

// shapeInputs seals constituent segments over one shape.
func shapeInputs(t *testing.T, constituents, docsPer int, perField map[string]int) []*mappedSegment {
	t.Helper()
	ins := make([]*mappedSegment, constituents)
	for c := range constituents {
		ins[c] = buildBM25(t, shapeDocs(fmt.Sprintf("c%d", c), docsPer, perField))
	}
	return ins
}

// TestMergedBlobIsStructurallyValidAcrossShapes is the GENERAL row: a merged blob
// addresses its own block payloads whatever the term count, the constituent count
// or the per-field layout.
//
// IT IS NOT A 65..96 REGRESSION TEST. The defect was reported in that window
// because a field of exactly three blocks opens a forward gap of exactly
// mergeRunMaxGap, but the writer's rule — zeros may be padded only where nothing is
// written — is independent of block counts, and a row that only swept the reported
// window would certify the arithmetic rather than the rule. The sweep therefore
// crosses 1, 2, 3 and 4 blocks term by term AND runs past 128 blocks, on 2, 3, 5 and
// 8 constituents.
func TestMergedBlobIsStructurallyValidAcrossShapes(t *testing.T) {
	t.Run("dense term sweep", func(t *testing.T) {
		var failures []string
		for _, constituents := range []int{2, 3} {
			for terms := 1; terms <= 260; terms++ {
				ins := shapeInputs(t, constituents, 4, map[string]int{
					searchengine.FieldSymbolName:  200,
					searchengine.FieldSummary:     terms,
					searchengine.FieldDescription: terms,
					searchengine.FieldContent:     terms,
				})
				blob, err := mergeToBytes(t, ins, nil, dictBlocked)
				require.NoError(t, err, "constituents=%d terms=%d", constituents, terms)
				if bad := badBlockIdx(blob); len(bad) > 0 {
					failures = append(failures, fmt.Sprintf("constituents=%d terms=%d: %v", constituents, terms, bad))
					continue
				}
				if verr := ValidateSegment("", blob); verr != nil {
					failures = append(failures, fmt.Sprintf("constituents=%d terms=%d: validate=%v", constituents, terms, verr))
				}
			}
		}
		if len(failures) > 0 {
			t.Errorf("%d merged blobs are structurally invalid:\n%s", len(failures), strings.Join(failures, "\n"))
		}
	})

	t.Run("long term lists", func(t *testing.T) {
		for _, terms := range []int{512, 1024, 2048, 4096} {
			for _, constituents := range []int{2, 3, 5, 8} {
				t.Run(fmt.Sprintf("terms=%d/constituents=%d", terms, constituents), func(t *testing.T) {
					ins := shapeInputs(t, constituents, 2, map[string]int{
						searchengine.FieldSummary: terms,
					})
					blob, err := mergeToBytes(t, ins, nil, dictBlocked)
					require.NoError(t, err)
					require.Empty(t, badBlockIdx(blob), "blocks=%d", (terms+blockedBlockTerms-1)/blockedBlockTerms)
					require.NoError(t, ValidateSegment("", blob))
				})
			}
		}
	})

	// UNEQUAL FIELDS IN ONE SEGMENT, which is the layout the dense sweep cannot
	// reach: five dictionaries of different block counts are laid out back to back,
	// so one field's block index sits inside the region a neighboring field's
	// coalescing run is filling, and the writer's per-stream slots are contended by
	// five ascending streams rather than two.
	t.Run("unequal fields in one segment", func(t *testing.T) {
		shapes := []map[string]int{
			{
				searchengine.FieldSymbolName: 33, searchengine.FieldSummary: 90,
				searchengine.FieldKeywords: 1, searchengine.FieldDescription: 500,
				searchengine.FieldContent: 4096,
			},
			{
				searchengine.FieldSymbolName: 96, searchengine.FieldSummary: 65,
				searchengine.FieldKeywords: 64, searchengine.FieldDescription: 97,
				searchengine.FieldContent: 129,
			},
			{
				searchengine.FieldSymbolName: 1, searchengine.FieldSummary: 32,
				searchengine.FieldKeywords: 33, searchengine.FieldDescription: 2048,
				searchengine.FieldContent: 31,
			},
		}
		for i, shape := range shapes {
			for _, constituents := range []int{2, 3, 5, 8} {
				t.Run(fmt.Sprintf("shape=%d/constituents=%d", i, constituents), func(t *testing.T) {
					ins := shapeInputs(t, constituents, 2, shape)
					blob, err := mergeToBytes(t, ins, nil, dictBlocked)
					require.NoError(t, err)
					require.Empty(t, badBlockIdx(blob), "shape %v", shape)
					require.NoError(t, ValidateSegment("", blob))
				})
			}
		}
	})
}

// TestBadBlockIdxDetectsThePreservedArtifact is the detector's known positive, and
// it is what makes every Empty(badBlockIdx(...)) above a measurement.
//
// The artifact is the segment preserved by the reader-side containment work: three
// blocks in field 0, entries 0 and 1 zeroed, entry 2 intact. If this row ever goes
// quiet the sweep above has stopped being able to see the defect it exists for.
func TestBadBlockIdxDetectsThePreservedArtifact(t *testing.T) {
	raw, err := os.ReadFile("testdata/corrupt-posting-run.seg")
	require.NoError(t, err)
	// The stored file is envelope followed by payload; the block index lives in the
	// payload, which is also what the id names (see realCorruptSegment).
	_, blob, err := searchengine.SplitStoredBlob(raw)
	require.NoError(t, err)
	bad := badBlockIdx(blob)
	require.NotEmpty(t, bad, "the preserved corrupt artifact must be detected")
	t.Logf("preserved artifact: %v", bad)
	require.Error(t, ValidateSegment("preserved", blob))
}

// TestWriterCannotProduceThePreservedArtifactShape is the GENERATOR row: the shape
// of the preserved artifact — a field of exactly three blocks whose first two index
// entries are zero — is not something the writer can emit any more.
//
// IT DRIVES THE TRACE'S OWN SLOT ORDER. The researcher's instrumented run reached
// the defect with a symbol_name field of 200 terms (7 blocks) beside a 90-term
// field (3 blocks) over two constituents: the wide field's rows keep the
// round-robin slots turning while the three-block field's first-term run ends
// exactly at its block index. The shape is deterministic, so this row is a
// generator rather than a sample.
func TestWriterCannotProduceThePreservedArtifactShape(t *testing.T) {
	for _, constituents := range []int{2, 3} {
		t.Run(fmt.Sprintf("constituents=%d", constituents), func(t *testing.T) {
			ins := shapeInputs(t, constituents, 4, map[string]int{
				searchengine.FieldSymbolName:  200,
				searchengine.FieldSummary:     90,
				searchengine.FieldDescription: 90,
				searchengine.FieldContent:     90,
			})
			blob, err := mergeToBytes(t, ins, nil, dictBlocked)
			require.NoError(t, err)
			bad := badBlockIdx(blob)
			sort.Strings(bad)
			require.Empty(t, bad, "the writer reproduced the preserved artifact's shape")
			require.NoError(t, ValidateSegment("", blob))
		})
	}
}
