// SPDX-License-Identifier: Apache-2.0

package bm25

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// mergeInputs builds n sealed segments over a shared vocabulary. The corpus is
// split so terms recur across inputs — which is what makes the k-way merge do
// real work rather than concatenate disjoint dictionaries.
func mergeInputs(t *testing.T, docs []searchengine.Document, n int) []*mappedSegment {
	t.Helper()
	per := (len(docs) + n - 1) / n
	var ins []*mappedSegment
	for i := 0; i < len(docs); i += per {
		seg, err := Format{}.Build(docs[i:min(i+per, len(docs))])
		require.NoError(t, err)
		ins = append(ins, seg.(*mappedSegment))
	}
	require.Len(t, ins, n)
	return ins
}

// mergeHeavyDocs builds a corpus whose documents carry a realistic amount of
// text: termsPerField terms in each of summary and content, over a vocabulary
// wide enough that dictionaries overlap without collapsing. The measured live
// corpus averages roughly 1.2 KB per document on disk, and merge measurements
// are only meaningful against documents of that order.
func mergeHeavyDocs(n, termsPerField int) []searchengine.Document {
	rng := rand.New(rand.NewPCG(0x51DE, 0xF00D))
	vocab := make([]string, 4000)
	for i := range vocab {
		vocab[i] = fmt.Sprintf("vocab%05dterm", i)
	}
	docs := make([]searchengine.Document, n)
	for i := range docs {
		var summary, content strings.Builder
		for range termsPerField {
			summary.WriteString(vocab[rng.IntN(len(vocab))] + " ")
			content.WriteString(vocab[rng.IntN(len(vocab))] + " ")
		}
		docs[i] = searchengine.Document{
			ID: fmt.Sprintf("d%d", i),
			Fields: map[string]string{
				searchengine.FieldSummary: summary.String(),
				searchengine.FieldContent: content.String(),
			},
		}
	}
	return docs
}

// mergeToBytes runs the streamed merge into a caller-owned file and returns the
// merged segment's bytes.
//
// IT IS TEST-ONLY PLUMBING, not a wrapper around anything production does. The
// production path never reads its own output back: the engine maps the finished
// file instead, which is the whole point of the change. A test that wants the
// merged bytes in memory has to read them itself, so the write-size-read
// sequence lives here rather than at each call site.
//
// IT TAKES kind BECAUSE MergeTo DOES NOT. MergeTo emits defaultDictKind, so
// routing these per-encoding tests through it would silently collapse three
// cases into one; streamMergeToFile is the layer that still carries the
// parameter, and it is the layer these tests are about.
func mergeToBytes(t *testing.T, ins []*mappedSegment, accept []func(searchengine.ExternalID) bool, kind byte) ([]byte, error) {
	t.Helper()
	f, err := os.Create(filepath.Join(t.TempDir(), "merged.seg")) //nolint:gosec // test-owned temp path
	require.NoError(t, err)
	defer func() { require.NoError(t, f.Close()) }()

	n, err := streamMergeToFile(f, ins, accept, kind)
	if err != nil {
		return nil, err
	}
	// The engine owns this in production; here the test is the engine.
	require.NoError(t, f.Truncate(n))
	return os.ReadFile(f.Name()) //nolint:gosec // test-owned temp path
}

// mergeToSegment is mergeToBytes followed by the decode the engine performs, so
// a test can assert on the merged SEGMENT rather than on its bytes.
func mergeToSegment(t *testing.T, ins []*mappedSegment, accept []func(searchengine.ExternalID) bool, kind byte) (*mappedSegment, error) {
	t.Helper()
	blob, err := mergeToBytes(t, ins, accept, kind)
	if err != nil {
		return nil, err
	}
	return openSegmentV2(blob)
}

// dropEverySeventh is the liveness filter the merge tests run under. A merge
// with nothing dropped never exercises the remap's -1 arm or the dropped-term
// omission, so the filtered shape is the one worth asserting on.
func dropEverySeventh(ins []*mappedSegment) []func(searchengine.ExternalID) bool {
	out := make([]func(searchengine.ExternalID) bool, len(ins))
	for i := range ins {
		out[i] = func(id searchengine.ExternalID) bool { return docOrdinal(id)%7 != 0 }
	}
	return out
}

// docOrdinal reads the trailing decimal run of a manyTermDocs id ("d417" → 417).
// An id with no digits yields -1, which no modulus test drops — a filter that
// silently rejected every id would make every merge assertion vacuous, and the
// first draft of this helper did exactly that.
func docOrdinal(id searchengine.ExternalID) int {
	n, seen := 0, false
	for _, c := range id {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
			seen = true
		}
	}
	if !seen {
		return -1
	}
	return n
}

// TestStreamedMergeMatchesMapMerge proves the streamed writer produces the same
// SEGMENT as the map-shaped merge it replaced: same members, same document
// frequencies, and identical hits, scores and order on every query — under a
// filter that actually drops documents.
//
// The comparison runs for every dictionary encoding, because the merge writes
// the output dictionary and a defect in one encoding's writer would otherwise
// hide behind the default.
func TestStreamedMergeMatchesMapMerge(t *testing.T) {
	docs := manyTermDocs(400)
	ins := mergeInputs(t, docs, 4)
	accept := dropEverySeventh(ins)

	want := mapMergeReference(t, ins, accept)
	wantStats := Format{}.AggregateStats([]searchengine.Segment[Query, *CorpusStats]{want})
	require.NotEmpty(t, want.IDs(), "the reference merge kept nothing — every comparison below would be vacuous")
	require.Less(t, len(want.IDs()), len(docs), "the filter dropped nothing, so the liveness arm is untested")

	terms := sortedKeys(buildAccumulator(t, docs).docFreq)
	for _, dk := range dictKinds {
		t.Run(dk.name, func(t *testing.T) {
			got, err := mergeToSegment(t, ins, accept, dk.kind)
			require.NoError(t, err)
			gotStats := Format{}.AggregateStats([]searchengine.Segment[Query, *CorpusStats]{got})

			require.Equal(t, want.IDs(), got.IDs(), "member list")
			require.Equal(t, wantStats.TotalDocs, gotStats.TotalDocs)
			require.Equal(t, docFreqSnapshot(wantStats, terms), docFreqSnapshot(gotStats, terms),
				"document frequencies must match the map merge term for term")

			nonEmpty := 0
			for qi, text := range equalityQueries {
				q := NewQuery(text)
				w := want.Search(q, wantStats, 25, nil)
				g := got.Search(q, gotStats, 25, nil)
				require.Len(t, g, len(w), "query %d hit count", qi)
				for i := range w {
					require.Equal(t, w[i].ID, g[i].ID, "query %d rank %d id", qi, i)
					require.Equal(t, math.Float64bits(w[i].Score), math.Float64bits(g[i].Score),
						"query %d rank %d score", qi, i)
				}
				nonEmpty += len(w)
			}
			require.Positive(t, nonEmpty, "no query matched anything — comparing empty result sets proves nothing")
		})
	}
}

// TestStreamedMergeByteDeterministic proves two writers converge on one
// content-addressed blob: five independent streamed merges of the same inputs
// must produce byte-identical output, since the segment id is sha256 of those
// bytes and a store only dedups if they agree.
func TestStreamedMergeByteDeterministic(t *testing.T) {
	docs := manyTermDocs(300)
	ins := mergeInputs(t, docs, 3)
	accept := dropEverySeventh(ins)

	for _, dk := range dictKinds {
		t.Run(dk.name, func(t *testing.T) {
			first, err := mergeToBytes(t, ins, accept, dk.kind)
			require.NoError(t, err)
			require.NotEmpty(t, first)
			for i := range 4 {
				again, err := mergeToBytes(t, ins, accept, dk.kind)
				require.NoError(t, err)
				require.Equal(t, first, again, "merge %d diverged from the first", i)
			}
		})
	}
}

// TestMergeOmitsFullyDroppedTerms pins the trap a naive port falls into: a term
// whose every posting the filter drops must be ABSENT from the merged segment.
// The inputs still hold it, so a merge that walks dictionaries rather than
// surviving documents will happily carry it across — putting an entry in the
// output that no document justifies, which a fresh build of the same survivors
// would never produce.
//
// The known-positive is asserted, not assumed: the term is confirmed PRESENT in
// the inputs and confirmed to have a non-zero document frequency there, so its
// absence from the output is a result rather than a fixture accident.
func TestMergeOmitsFullyDroppedTerms(t *testing.T) {
	const doomed = "quarantinedterm"
	docs := []searchengine.Document{
		{ID: "keep-1", Fields: map[string]string{searchengine.FieldContent: "alpha shared filler"}},
		{ID: "doomed-1", Fields: map[string]string{searchengine.FieldContent: doomed + " shared filler"}},
		{ID: "keep-2", Fields: map[string]string{searchengine.FieldContent: "beta shared filler"}},
		{ID: "doomed-2", Fields: map[string]string{searchengine.FieldSummary: doomed + " other text"}},
	}
	ins := mergeInputs(t, docs, 2)

	// KNOWN-POSITIVE: the term really is in the inputs, in both a content and a
	// summary field, with a real document frequency.
	inputDF := int64(0)
	found := false
	for _, ms := range ins {
		inputDF += ms.segmentDocFreq(doomed)
		for _, mf := range ms.fields {
			if _, _, ok := mf.lookup(doomed); ok {
				found = true
			}
		}
	}
	require.True(t, found, "fixture broken: %q is not in any input dictionary", doomed)
	require.Equal(t, int64(2), inputDF, "fixture broken: %q must be carried by two input documents", doomed)

	accept := make([]func(searchengine.ExternalID) bool, len(ins))
	for i := range accept {
		accept[i] = func(id searchengine.ExternalID) bool { return id != "doomed-1" && id != "doomed-2" }
	}

	for _, dk := range dictKinds {
		t.Run(dk.name, func(t *testing.T) {
			got, err := mergeToSegment(t, ins, accept, dk.kind)
			require.NoError(t, err)
			require.ElementsMatch(t, []searchengine.ExternalID{"keep-1", "keep-2"}, got.IDs())

			require.Zero(t, got.segmentDocFreq(doomed),
				"%q survived into the merged docFreq dictionary despite every posting being dropped", doomed)
			for _, mf := range got.fields {
				_, _, ok := mf.lookup(doomed)
				require.False(t, ok, "%q survived into field %q's dictionary", doomed, mf.config.Name)
			}
			// A term the filter did NOT empty must still be there — otherwise
			// this test would pass against a merge that dropped everything.
			require.Positive(t, got.segmentDocFreq("shared"),
				"the surviving control term vanished too, so the omission above is not selective")
		})
	}
}

// TestStreamedMergePeakHeapBounded proves the writer STREAMS rather than
// buffers: the whole merge allocates less than half the output it produces.
//
// It measures TotalAlloc, not sampled HeapAlloc, and that choice is the point.
// TotalAlloc is cumulative and exact, and peak heap can never exceed total bytes
// allocated — so a total under the bound proves the peak bound with no sampling
// window that could miss a spike. A sampled peak could report a small number
// simply by never looking while the spike existed.
//
// The measurement covers streamMergeToFile, which is the WRITER. There is no
// longer a read-back to exclude: the merge path stopped reading its own output
// back into a blob when the engine took over mapping the file, so this bound and
// the whole-path bound in segmentdist now differ only by the engine's map and
// decode rather than by an output-sized allocation.
//
// The 64 KiB of fixed coalescing windows the writer holds IS inside this
// measurement, which is why the fixture has to produce a segment several times
// that size for the bound to say anything.
func TestStreamedMergePeakHeapBounded(t *testing.T) {
	allocated, blobSize := measureMerge(t, mergeHeavyDocs(600, 40), 6)
	t.Logf("streamed merge allocated %d bytes writing a %d-byte segment", allocated, blobSize)
	require.Less(t, allocated, blobSize/2,
		"the writer allocated %d bytes for a %d-byte output — that is a buffered merge, not a streamed one",
		allocated, blobSize)

	// The ratio above is sensitive to the fixture, and saying so is cheaper than
	// pretending otherwise: merge state is per-DOCUMENT (the last-wins winner
	// map, the member list, the id remap, the per-field document lengths) while
	// the bound is against the OUTPUT SIZE, so a corpus of unrealistically tiny
	// documents inverts the ratio no matter how well the writer streams. The
	// fixture is sized to the real corpus instead — the measured one runs about
	// 1.2 KB per document on disk.
	//
	// This second measurement is what pins the streaming property INDEPENDENT of
	// that ratio: quadrupling the text per document, with the SAME document
	// count, multiplies the output several-fold while leaving the per-document
	// state identical. A writer that buffered its output would track the blob;
	// this one must barely move.
	fatAlloc, fatSize := measureMerge(t, mergeHeavyDocs(600, 160), 6)
	t.Logf("4x the text per document: allocated %d bytes writing a %d-byte segment", fatAlloc, fatSize)
	require.Greater(t, fatSize, blobSize*2, "the fixture did not actually grow the output; the control below is vacuous")
	require.Less(t, fatAlloc, allocated*2,
		"allocations grew with the output (%d → %d bytes for %d → %d bytes of segment), which is what buffering looks like",
		allocated, fatAlloc, blobSize, fatSize)
}

// measureMerge runs the streamed writer over docs split into n inputs and
// reports the bytes it allocated and the bytes it wrote.
//
// It measures TotalAlloc rather than a sampled HeapAlloc peak. TotalAlloc is
// cumulative and exact, and peak can never exceed total, so a bound on the total
// bounds the peak with no sampling window that could miss a spike — a sampled
// peak could report a small number simply by never looking while one existed.
//
// The measurement covers streamMergeToFile, the WRITER, and the writer is now
// the whole of what the format does: there is no read-back left to exclude,
// because the engine maps the finished file rather than the format reading it
// back into an output-sized blob.
func measureMerge(t *testing.T, docs []searchengine.Document, n int) (allocated, blobSize int64) {
	t.Helper()
	ins := mergeInputs(t, docs, n)
	accept := dropEverySeventh(ins)

	f, err := os.Create(filepath.Join(t.TempDir(), "merged.seg")) //nolint:gosec // test-owned temp path
	require.NoError(t, err)
	defer func() { require.NoError(t, f.Close()) }()

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	size, err := streamMergeToFile(f, ins, accept, defaultDictKind)
	runtime.ReadMemStats(&after)
	require.NoError(t, err)

	// THE REPORTED SIZE IS n, NOT f.Stat(). The writer no longer truncates — the
	// engine does, from this same n — so the file on disk can be longer than the
	// segment whenever the last aligned append moved the tail past the last byte
	// carrying content. Reporting Stat here would silently loosen every bound
	// taken against this number rather than break it, which is the worse failure.
	require.Positive(t, size, "the merge produced no output, so any bound over it is vacuous")
	return int64(after.TotalAlloc - before.TotalAlloc), size
}

// THE MERGE FILE'S LIFECYCLE IS NO LONGER THIS PACKAGE'S TO TEST. This format
// used to create, own and unlink its own temp file, and a test here asserted
// that on both the success and the error path; the engine owns all of it now, so
// that assertion lives in searchengine as
// TestMergeLeavesNoScratchFileOnSuccessOrError.
//
// The same retired test also asserted the merged payload was HEAP-backed rather
// than a mapping. That claim did not move — it INVERTS. A merged payload is now
// deliberately mapping-backed in production, and the assertion lives in
// segmentdist as TestMergedPayloadIsMappingBackedNotHeapBacked, which is where
// the real mapping hook is wired; searchengine's default hook produces heap
// bytes, so the property is not even present there.
//
// TestMergedPostingRunsAscend pins the property the cursor order buys: every
// merged posting run is ascending by construction, never sorted afterwards. A
// descending pair would break the reader's assumptions silently rather than
// loudly, so it is asserted directly.
func TestMergedPostingRunsAscend(t *testing.T) {
	docs := manyTermDocs(300)
	ins := mergeInputs(t, docs, 3)
	got, err := mergeToSegment(t, ins, dropEverySeventh(ins), defaultDictKind)
	require.NoError(t, err)

	runs := 0
	for _, mf := range got.fields {
		mf.eachTerm(func(term string, docIDs []uint32, _ []uint16) {
			require.True(t, slices.IsSorted(docIDs), "field %q term %q posting run is not ascending", mf.config.Name, term)
			runs++
		})
	}
	require.Positive(t, runs, "the merged segment held no posting runs, so nothing was checked")
}

// The three constants below pin the merged payload's BYTES, one per dictionary
// encoding. They were captured from the streamed writer as it stood before the
// MergeSink restructure. NEVER REGENERATE THEM.
//
// Regenerating them destroys the only evidence that the stored format held
// still across that restructure: the whole value of a golden is that it was
// written down before the change, so a later mismatch means the bytes moved
// rather than that the expectation was refreshed to agree with them. If one of
// these has to be recovered, check the tree out at the commit before this
// changeset and re-run the capture there — do not re-derive it from a tree that
// carries the change.
//
// The capture procedure, for that recovery: set the constant to the empty
// string, run TestStreamedMergeGolden, and read the observed hash out of the
// failure output.
//
// Nothing else in this file may hold a 64-hex literal. A fourth one would make
// the distinctness check that guards these three meaningless.
const (
	goldenMergeSHA256Flat    = "3d7272246aac55924c43d742aa267198dead27ef31cac0ca6a9b9d84cd470777"
	goldenMergeSHA256Blocked = "6aa716eac9b123a8a9959be3bd493f8a67333d6aecd5c77339198a6ac49e763a"
	goldenMergeSHA256Hash    = "4720fe48c7aafbc3129c5c8275e34a4a7077df7da455f1c441fb746f00107532"
)

// TestStreamedMergeGolden pins the merged payload byte-for-byte, for every
// dictionary encoding, against a hash captured from the unmodified writer.
//
// WHY A GOLDEN AND NOT A DERIVED EQUIVALENCE. There is no in-tree equivalence to
// lean on. bucket_swap_alias_test.go's "merge-of-one reproduces its build bytes"
// is a statement about the mock format; bm25's Build (encodeSegmentV2) and its
// Merge (the mergePlan layout) are two independent emitters, and nothing in this
// tree asserts they agree. TestStreamedMergeByteDeterministic merges twice and
// compares, which pins run-to-run stability rather than cross-version stability
// — so it cannot serve as this pin either.
//
// The per-kind loop is not decoration: the merge writes the output dictionary
// itself, so a defect in one encoding's writer would otherwise hide behind the
// default kind.
func TestStreamedMergeGolden(t *testing.T) {
	docs := manyTermDocs(200)
	ins := mergeInputs(t, docs, 2)
	accept := dropEverySeventh(ins)

	golden := map[string]string{
		"flat":    goldenMergeSHA256Flat,
		"blocked": goldenMergeSHA256Blocked,
		"hash":    goldenMergeSHA256Hash,
	}
	for _, dk := range dictKinds {
		t.Run(dk.name, func(t *testing.T) {
			want, ok := golden[dk.name]
			require.True(t, ok,
				"dictionary kind %q has no golden constant — a new encoding was added and this pin was not extended, so its bytes are unpinned", dk.name)

			f, err := os.Create(filepath.Join(t.TempDir(), "merged.seg")) //nolint:gosec // test-owned temp path
			require.NoError(t, err)
			defer func() { require.NoError(t, f.Close()) }()
			n, err := streamMergeToFile(f, ins, accept, dk.kind)
			require.NoError(t, err)

			// THE TRUNCATE IS PART OF THE FIXTURE, not incidental cleanup. The
			// writer no longer sizes its own destination — the engine does, from
			// the returned n — so hashing the file without it would hash whatever
			// the last aligned append reached rather than the segment. The three
			// constants below were captured over a file sized to exactly this
			// length, so omitting this would move the hash for a merge that had not
			// changed at all.
			require.NoError(t, f.Truncate(n))

			blob, err := os.ReadFile(f.Name()) //nolint:gosec // test-owned temp path
			require.NoError(t, err)
			require.NotEmpty(t, blob,
				"the merge wrote nothing, so hashing it pins an empty file rather than a segment")

			sum := sha256.Sum256(blob)
			require.Equal(t, want, hex.EncodeToString(sum[:]),
				"the merged payload's bytes moved for dictionary kind %q (%d bytes written)", dk.name, len(blob))
		})
	}
}
