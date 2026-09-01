// SPDX-License-Identifier: Apache-2.0

package bm25

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// merge_structural_gate_test.go — the producer-side gate: a merge must never
// emit a segment whose dictionary refers to bytes it did not write.
//
// WHY THIS LAYER EXISTS AT ALL, given the write-time content-address check. That
// check hashes the payload and refuses an id that does not name it, which
// catches a producer whose buffer changed after it hashed. The incident was not
// that: the corrupt segment's payload hashed EXACTLY to its own filename, so the
// producer wrote precisely what it addressed and the damage was internal — a
// dictionary describing a layout larger than the blob the same producer stamped
// a length into. Hashing is structurally blind to that. This is the check that
// is not.
//
// EVERY OFFSET BELOW COMES FROM A REAL MERGE. The gate is exercised against
// offsets the emitter actually produced rather than hand-written numbers,
// because the property under test is a relationship between what the emitter
// wrote and what the writer reported — and invented numbers would satisfy it by
// construction.

// realMergeEmitterState runs the genuine merge machinery — the same calls
// streamMergeToFile makes, in the same order — and returns the emitter it used
// alongside the length the merge would have reported.
//
// It replicates rather than calls streamMergeToFile because the emitter is
// internal to that function, and the gate's inputs are exactly that emitter's
// accumulated state.
func realMergeEmitterState(t *testing.T, kind byte) (*mergeEmitter, int64) {
	t.Helper()
	acc := buildAccumulator(t, sampleDocs())
	blob, err := encodeSegmentV2(acc, kind)
	require.NoError(t, err)
	seg, err := openSegmentV2(blob)
	require.NoError(t, err)

	sink, err := os.CreateTemp(t.TempDir(), "gate-*.seg")
	require.NoError(t, err)
	t.Cleanup(func() { _ = sink.Close() })

	ins := []*mappedSegment{seg}
	accept := []func(searchengine.ExternalID) bool{func(searchengine.ExternalID) bool { return true }}
	members, remap := resolveMergeLayout(ins, accept)

	termCount := make([]int, len(defaultFieldConfigs))
	dfCount := 0
	mergeWalk(ins, remap,
		func(_ string, field int, _ []uint32, _ []uint16) { termCount[field]++ },
		func(string, int64) { dfCount++ })

	p := planMerge(kind, members, termCount, dfCount)
	w := newMergeWriter(sink, int64(p.prefixEnd))
	writeMergePrefix(w, p, ins, remap)
	e := newMergeEmitter(w, p)
	mergeWalk(ins, remap, e.field, e.term)
	e.flushBlocks()
	require.NoError(t, w.err)
	return e, w.tail
}

// TestMergeStructuralGate_AcceptsAHonestMergeAndRefusesAShortenedOne is the
// gate's two directions over one real merge.
//
// THE REFUSAL DIRECTION IS THE INCIDENT'S OWN SHAPE, produced the way the
// incident most plausibly produced it: the dictionary is left exactly as the
// merge wrote it and the LENGTH is shortened underneath it. That is what a tail
// that advanced past the content, or a header backpatched to a smaller value,
// leaves behind — a dictionary describing more bytes than the segment declares.
func TestMergeStructuralGate_AcceptsAHonestMergeAndRefusesAShortenedOne(t *testing.T) {
	for _, dk := range dictKinds {
		t.Run(dk.name, func(t *testing.T) {
			e, tail := realMergeEmitterState(t, dk.kind)

			// THE GATE IS NOT VACUOUS: the merge really did record references, and
			// the furthest one really does reach into the blob. Without this a gate
			// over an emitter that recorded nothing would pass every check while
			// asserting nothing at all.
			require.NotZero(t, e.furthest.end,
				"the emitter recorded no dictionary references, so the gate below would pass vacuously")
			require.LessOrEqual(t, int64(e.furthest.end), tail,
				"an honest merge's furthest reference lies inside what it wrote")

			require.NoError(t, e.verifyWithin(tail),
				"the gate must accept the segment this merge actually produced")

			// SHORTEN THE DECLARED LENGTH BY ONE BYTE — the smallest divergence that
			// still puts the furthest row outside the blob.
			err := e.verifyWithin(int64(e.furthest.end) - 1)
			require.Error(t, err, "a dictionary reaching past the declared length must be refused")
			require.Contains(t, err.Error(), "REFUSING to emit a structurally invalid segment")
			require.Contains(t, err.Error(), "The merge is abandoned and its constituents are untouched")
		})
	}
}

// TestMergeStructuralGate_RefusesAMisalignedPostingRun covers the gate's other
// leg: a run that fits in the blob but is not 4-aligned.
//
// IT IS A SEPARATE FAILURE FROM OVERRUNNING, and both are the reader's rule.
// mappedField.postings refuses either, so a merge that emitted either would have
// written a segment every future read rejects.
func TestMergeStructuralGate_RefusesAMisalignedPostingRun(t *testing.T) {
	e, tail := realMergeEmitterState(t, dictFlat)
	require.NoError(t, e.verifyWithin(tail), "control: the honest merge passes")

	// A run at an odd offset, well inside the blob so only alignment can fail it.
	e.noteRun(3, 1, 0, "misaligned-term")
	err := e.verifyWithin(tail)
	require.Error(t, err, "a posting run that is not 4-aligned must be refused")
	require.Contains(t, err.Error(), "not 4-aligned")
	require.Contains(t, err.Error(), "misaligned-term")
}

// TestMergeStructuralGate_IgnoresEmptyRunsAndTerms pins that the gate is not
// STRICTER than the reader it mirrors, which is the way a producer-side check
// does damage: by refusing correct segments.
//
// postings returns before its bounds check when the count is zero, and termView
// returns before its own for an empty term — so neither offset is constrained,
// and an assertion on them would reject ordinary merges. This is not
// hypothetical: applying the posting run's 4-alignment rule to TERM offsets,
// which are appended 1-aligned by design, made a correct merge fail while this
// change was being written.
func TestMergeStructuralGate_IgnoresEmptyRunsAndTerms(t *testing.T) {
	e, tail := realMergeEmitterState(t, dictFlat)

	e.noteRun(3, 0, 0, "empty-run-at-odd-offset")
	e.noteSpan(5, 0, 0, "empty-term-at-odd-offset")

	require.NoError(t, e.verifyWithin(tail),
		"a zero-length run or term is unconstrained for the reader and must be unconstrained here")
	require.Nil(t, e.misaligned, "an empty run must not be recorded as misaligned")
}
