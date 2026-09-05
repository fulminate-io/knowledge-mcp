// SPDX-License-Identifier: Apache-2.0

package segmentdist

// store_census_test.go — the control set for CensusStore, and the operator arm
// that points it at a real store.
//
// WHY THE CONTROL SET IS THE LARGER HALF. A census whose answer is "nothing
// found" is exactly the answer a census that examines nothing gives, so every
// question it asks needs a fixture that makes it answer YES. There are two
// questions and so there are two known positives: a payload stored under a name
// it does not hash to, and the preserved incident artifact whose dictionary
// addresses a posting run past the end of its own payload. The clean segment is
// the third arm — without it, a census that flagged everything would pass the
// other two.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// incidentCorruptID is the content address of the preserved artifact that
// crashed the daemon, pinned as a literal rather than computed here. Computing
// it from the same bytes the census hashes would make the address arm of this
// test agree with itself no matter what either side did.
const incidentCorruptID = "fbc34f956c49385e2040fbc740209c8aeffcfccaa55dc8b2a1d1957d3a1dfc26"

// censusEnvRoot names an on-disk segment store for the operator arm below.
const censusEnvRoot = "KNOWLEDGE_SEGMENT_CENSUS_ROOT"

// buildCleanSegmentPayload seals a real bm25 segment from real documents and
// returns its encoded payload — the format's own output, not a hand-built blob,
// so "clean" means what the producer actually writes.
func buildCleanSegmentPayload(t *testing.T) []byte {
	t.Helper()
	docs := make([]searchengine.Document, 0, 64)
	for i := range 64 {
		docs = append(docs, searchengine.Document{
			ID: fmt.Sprintf("doc-%d", i),
			Fields: map[string]string{
				"name":    fmt.Sprintf("census fixture %d", i),
				"content": strings.Repeat(fmt.Sprintf("term%d structural census payload ", i), 8),
			},
		})
	}
	seg, _, err := bm25.New().Build(docs)
	require.NoError(t, err)
	payload, err := seg.Encode()
	require.NoError(t, err)
	return payload
}

// writeCensusFixture stores payload at the store's canonical depth under the
// given id, which is how the census learns both the format and the name the
// content address is checked against.
func writeCensusFixture(t *testing.T, root, format, graph, bucket, id string, payload []byte) string {
	t.Helper()
	dir := filepath.Join(root, format, graph, bucket)
	require.NoError(t, os.MkdirAll(dir, 0o750))
	path := filepath.Join(dir, id+".seg")
	//nolint:gosec // G703: every component is a t.TempDir() path or a literal from this file.
	require.NoError(t, os.WriteFile(path, payload, 0o600))
	return path
}

func TestCensusStore_FlagsCorruptionAndMisnamingAndPassesACleanSegment(t *testing.T) {
	root := t.TempDir()

	clean := buildCleanSegmentPayload(t)
	cleanSum := sha256.Sum256(clean)
	cleanID := hex.EncodeToString(cleanSum[:])
	cleanPath := writeCensusFixture(t, root, "bm25v2", "code", "census", cleanID, clean)

	// KNOWN POSITIVE #1 — the same good payload under a name it does not hash
	// to. The name is a constant chosen here, so the expectation is independent
	// of anything the census computes.
	const misnamedID = "0000000000000000000000000000000000000000000000000000000000000000"
	misnamedPath := writeCensusFixture(t, root, "bm25v2", "code", "census", misnamedID, clean)

	// KNOWN POSITIVE #2 — the incident artifact, stored under its own true id.
	// Its content address is intact and its structure is not; that combination
	// is the whole reason the census asks two questions instead of one.
	raw, err := os.ReadFile(filepath.Join("testdata", "incident-fbc34f-enveloped.seg"))
	require.NoError(t, err, "the preserved incident artifact must be present")
	corruptPath := writeCensusFixture(t, root, "bm25v2", "code", "census", incidentCorruptID, raw)

	verdicts, err := CensusStore(root)
	require.NoError(t, err)
	require.Len(t, verdicts, 3, "the census must return one row per stored file")

	byPath := map[string]SegmentVerdict{}
	for _, v := range verdicts {
		byPath[v.Path] = v
	}

	cleanV := byPath[cleanPath]
	require.NoError(t, cleanV.Err, "a segment the format itself just produced must pass its own reader")
	require.True(t, cleanV.AddressOK)
	require.True(t, cleanV.StructureExamined, "a bm25 segment reported as clean must actually have been walked")
	require.False(t, cleanV.Corrupt())
	require.Equal(t, "bm25v2", cleanV.Format)
	require.Equal(t, "code", cleanV.GraphType)
	require.Equal(t, "census", cleanV.GraphName)
	require.Zero(t, cleanV.Superseded, "a freshly built segment replaces nothing, so it carries no supersession envelope")

	misnamedV := byPath[misnamedPath]
	require.False(t, misnamedV.AddressOK, "a payload stored under a name it does not hash to must fail the address arm")
	require.ErrorContains(t, misnamedV.Err, "content address")
	require.False(t, misnamedV.Corrupt(), "a misnamed payload is not a structural corruption, and conflating them would hide either one")

	corruptV := byPath[corruptPath]
	require.True(t, corruptV.AddressOK, "the incident artifact hashes to its own filename — its damage is inside a faithfully stored payload")
	require.True(t, corruptV.StructureExamined)
	require.True(t, corruptV.Corrupt(), "the artifact that crashed the daemon must be flagged by the structural arm")
	require.ErrorContains(t, corruptV.Err, "misaligned or past the")
	var ce *searchengine.CorruptSegmentError
	require.ErrorAs(t, corruptV.Err, &ce)
	require.Equal(t, searchengine.SegmentID(incidentCorruptID), ce.ID,
		"the census must stamp the failing file's id onto the corruption, or a sweep of thousands cannot say which file")
}

// TestCensusStore_UnexaminedFormatsAreNotReportedClean pins the distinction a
// population count depends on: a format with no structural validator must leave
// StructureExamined false rather than contributing a silent pass.
//
// rebuildstate IS SUCH A FAMILY and is not a hypothetical — the live store keeps
// one beside bm25v2 and hnswv3. Using a real unvalidated family rather than an
// invented directory name keeps this test measuring the dispatch the census
// actually performs.
func TestCensusStore_UnexaminedFormatsAreNotReportedClean(t *testing.T) {
	root := t.TempDir()
	payload := []byte("rebuild-state bookkeeping, in no segment format and not claimed to be one")
	sum := sha256.Sum256(payload)
	path := writeCensusFixture(t, root, "rebuildstate", "knowledge", "default", hex.EncodeToString(sum[:]), payload)

	verdicts, err := CensusStore(root)
	require.NoError(t, err)
	require.Len(t, verdicts, 1)

	v := verdicts[0]
	require.Equal(t, path, v.Path)
	require.Equal(t, "rebuildstate", v.Format)
	require.Equal(t, "knowledge", v.GraphType, "the census reads graph and format off the store's own layout, so a second graph must land in a different row")
	require.True(t, v.AddressOK, "the content-address arm is format-agnostic and must still run")
	require.False(t, v.StructureExamined, "no validator is registered for this family, and the row must say so rather than read as clean")
	require.NoError(t, v.Err)
}

// TestCensusStore_ExaminesHNSWStructurally is the hnsw half of the control set:
// the census must now WALK an hnsw payload rather than count it.
//
// WHY THE SECOND ARM IS UNREADABLE BYTES rather than a subtly damaged segment.
// StructureExamined is a boolean the census sets beside the call, so a flag flip
// with no call behind it would satisfy the clean arm on its own. Feeding the
// hnsw validator something it cannot open forces it to SPEAK — the row's error
// is the validator's own sentence, which only exists if it ran.
//
// The deep structural discrimination — a neighbor run naming a node the segment
// does not have, damaged minimally with the footer CRC re-sealed so it still
// opens — is proven in the hnsw package's own tests, where the format's header
// offsets and checksum polynomial live. Duplicating those constants here to
// stage the same damage would put this format's layout in a second package that
// does not own it.
func TestCensusStore_ExaminesHNSWStructurally(t *testing.T) {
	root := t.TempDir()

	clean := buildCleanHNSWPayload(t)
	cleanSum := sha256.Sum256(clean)
	cleanPath := writeCensusFixture(t, root, "hnswv3", "knowledge", "default",
		hex.EncodeToString(cleanSum[:]), clean)

	unreadable := []byte("bytes filed as an hnsw segment that are not one")
	badSum := sha256.Sum256(unreadable)
	badPath := writeCensusFixture(t, root, "hnswv3", "knowledge", "default",
		hex.EncodeToString(badSum[:]), unreadable)

	verdicts, err := CensusStore(root)
	require.NoError(t, err)
	require.Len(t, verdicts, 2)

	byPath := map[string]SegmentVerdict{}
	for _, v := range verdicts {
		byPath[v.Path] = v
	}

	cleanV := byPath[cleanPath]
	require.True(t, cleanV.StructureExamined, "an hnsw segment must now be walked, not merely counted")
	require.NoError(t, cleanV.Err, "a segment the hnsw format itself just produced must pass its own reader-rule validator")
	require.True(t, cleanV.AddressOK)

	badV := byPath[badPath]
	require.True(t, badV.StructureExamined)
	require.True(t, badV.AddressOK, "the bytes hash to their own filename; what is wrong with them is inside")
	require.Error(t, badV.Err, "the hnsw validator must have RUN and refused these bytes, which is what proves the dispatch is real")
	require.ErrorContains(t, badV.Err, "does not open")
	require.False(t, badV.Corrupt(),
		"bytes that are not a segment at all are a different verdict from a segment whose stored references are wrong, and the census must not merge them")
}

// buildCleanHNSWPayload seals a real hnsw segment from real vectors — the
// format's own output, so "clean" means what the producer actually writes.
func buildCleanHNSWPayload(t *testing.T) []byte {
	t.Helper()
	const vecBytes = 32
	docs := make([]searchengine.Document, 0, 128)
	for i := range 128 {
		vec := make([]byte, vecBytes)
		for j := range vec {
			vec[j] = byte(i*7 + j*13)
		}
		docs = append(docs, searchengine.Document{ID: fmt.Sprintf("vec-%d", i), Vector: vec})
	}
	seg, _, err := hnsw.New().Build(docs)
	require.NoError(t, err)
	payload, err := seg.Encode()
	require.NoError(t, err)
	return payload
}

// TestCensusStore_RealStore is the OPERATOR ARM. It walks a real segment store
// when one is named and prints the population; it skips otherwise, which is why
// the control set above carries the discrimination.
//
//	KNOWLEDGE_SEGMENT_CENSUS_ROOT=~/.knowledge/segments \
//	  go test ./cmd/knowledge/internal/segmentdist/ -run TestCensusStore_RealStore -v
//
// It FAILS on any hit. A corrupt or misnamed segment in a live store is not an
// observation to note and move past: it is a file that will crash or mislead a
// read, and the run that finds it should be the run that says so.
func TestCensusStore_RealStore(t *testing.T) {
	root := os.Getenv(censusEnvRoot)
	if root == "" {
		t.Skipf("set %s to a segment store root to census it", censusEnvRoot)
	}

	verdicts, err := CensusStore(root)
	require.NoError(t, err)
	require.NotEmpty(t, verdicts, "censusing %s found no stored segments at all — the root is wrong, or the layout is not the one this census walks", root)

	var (
		hits      []SegmentVerdict
		examined  int
		merges    int
		byFormat  = map[string]int{}
		byGraph   = map[string]int{}
		totalSize int64
	)
	for _, v := range verdicts {
		byFormat[v.Format]++
		byGraph[v.GraphType]++
		totalSize += v.Size
		if v.StructureExamined {
			examined++
		}
		if v.Superseded > 0 {
			merges++
		}
		if v.Err != nil {
			hits = append(hits, v)
		}
	}

	t.Logf("censused %d stored segments (%d MiB) under %s", len(verdicts), totalSize>>20, root)
	t.Logf("  structurally walked: %d; merge products: %d; build/tail products: %d",
		examined, merges, len(verdicts)-merges)
	for _, line := range sortedCounts("format", byFormat) {
		t.Log("  " + line)
	}
	for _, line := range sortedCounts("graph", byGraph) {
		t.Log("  " + line)
	}
	// A QUARANTINED HIT IS REPORTED BUT DOES NOT FAIL THE RUN. Those bytes are
	// evidence of a disposition already taken — the segment is out of service and
	// kept deliberately — so failing on them would make this gate permanently red
	// for a store that is behaving exactly as designed, which is how a gate stops
	// being read. A hit in a SERVED bucket is the thing that must fail.
	for _, v := range hits {
		line := fmt.Sprintf("%s\n  format=%s type=%s name=%s size=%d payload=%d mtime=%s superseded=%d structural=%t\n  %v",
			v.Path, v.Format, v.GraphType, v.GraphName, v.Size, v.PayloadSize,
			v.ModTime.Format("2006-01-02T15:04:05Z07:00"), v.Superseded, v.Corrupt(), v.Err)
		if v.Quarantined {
			t.Logf("QUARANTINED (already withdrawn from service, retained as evidence) %s", line)
			continue
		}
		t.Errorf("HIT %s", line)
	}
}

func sortedCounts(label string, counts map[string]int) []string {
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, fmt.Sprintf("%s %-24s %d", label, k, counts[k]))
	}
	return out
}

// TestCensusStore_SkipsMergeScratch reproduces F8: a merge's in-flight output
// was censused as a segment.
//
// A merge writes to merge-*.seg in the same directory and unlinks it the moment
// its mapping succeeds, so one is visible DURING a merge — or after a crash,
// until the next merge sweeps it. Half-written bytes do not hash to the name in
// their path, so the census reported a content-address failure for a file that
// is not a segment and was never claimed to be one. That is the false alarm that
// teaches a reader to stop believing this gate.
func TestCensusStore_SkipsMergeScratch(t *testing.T) {
	root := t.TempDir()

	clean := buildCleanHNSWPayload(t)
	sum := sha256.Sum256(clean)
	kept := writeCensusFixture(t, root, "hnswv3", "knowledge", "default", hex.EncodeToString(sum[:]), clean)

	// A merge scratch file, named exactly as the engine's os.CreateTemp pattern
	// produces, carrying bytes that hash to nothing in particular.
	dir := filepath.Join(root, "hnswv3", "knowledge", "default")
	scratch := filepath.Join(dir, "merge-2517293910.seg")
	//nolint:gosec // G703: every component is a t.TempDir() path or a literal from this file.
	require.NoError(t, os.WriteFile(scratch, []byte("half-written merge output"), 0o600))

	verdicts, err := CensusStore(root)
	require.NoError(t, err)

	require.Len(t, verdicts, 1, "the merge scratch must not appear as a census row at all")
	require.Equal(t, kept, verdicts[0].Path)
	require.NoError(t, verdicts[0].Err, "and the real segment beside it is still examined")
}
