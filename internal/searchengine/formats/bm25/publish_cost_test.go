// SPDX-License-Identifier: Apache-2.0

// publish_cost_test.go — the BM25 arm of the publish-cost gate
// (GitHub issue #172, requirement R1).
//
// WHY THIS ARM EXISTS AT ALL. The gate in the searchengine package measures the
// route, over the mock format whose AggregateStats sums row lengths. This format's
// does not: it walks every resident segment on EVERY publish and allocates a fresh
// CorpusStats plus a field-totals map (format.go, AggregateStats — whose own godoc
// says it runs inside the engine's publish CAS retry loops). A mock-only gate would
// therefore certify a per-publish cost it never measured on the engine that pays
// the larger one, so the same two thresholds are run here against the real format.
//
// It is a SECOND HARNESS POINT rather than an arm of the first because searchengine
// cannot import this package: this package imports searchengine.
//
// THE CORPUS IS SMALLER THAN THE MOCK ARM'S, and the reason is stated rather than
// hidden: building half a million real BM25 segments — tokenizing, serializing and
// mapping each one — is minutes of work for a gate whose subject is what a publish
// COPIES. The ratio across the corpus range is what discriminates here, and the
// range below is a 16x one over the same shape.

package bm25

import (
	"fmt"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

const (
	// bm25CostSegments is the resident segment count the readings are taken at. It
	// is also what AggregateStats walks per publish, so it is the term this arm
	// exists to expose.
	bm25CostSegments = 512
	bm25CostSmallM   = 8192
	bm25CostLargeM   = 131072
	bm25CostBatch    = 8
	// bm25CostWindow is four times the engine's tail limit, for the reason the mock
	// arm's own constant states: a window shorter than several flattens reads as
	// whatever side of a flatten it started on. The engine's limit is unexported and
	// this package cannot see it, so the number is stated here with its derivation
	// and pinned by the searchengine-side test that CAN see the constant.
	bm25CostWindow = 4096
	// bm25CostAllocCap and bm25CostAllocRatioCap are R1's two thresholds, unchanged
	// from the mock arm: this arm asks whether they hold for the format that folds
	// corpus statistics on every publish.
	bm25CostAllocCap      = 1 << 20
	bm25CostAllocRatioCap = 8.0
)

// THE RESIDENT-SEGMENT AXIS. The gate above sweeps the CORPUS with
// the segment count held fixed at bm25CostSegments, so the term AggregateStats
// actually walked — the number of RESIDENT SEGMENTS — was constant across both of
// its cells and was never under test. This axis holds the corpus fixed and sweeps
// the segment count instead.
const (
	// bm25ResidentCorpus is the document count BOTH cells index, so the flatten the
	// route pays every routeTailLimit publishes costs the same in each and the only
	// independent variable is the segment count.
	bm25ResidentCorpus = 16384
	bm25ResidentSmall  = 256
	bm25ResidentLarge  = 16384
	// bm25ResidentPointerBytes is the floor a COPY-ON-WRITE append cannot go below:
	// the new snapshot's entries slice is one pointer per resident entry, copied so
	// the receiver's slice stays intact for readers still serving from it. That term
	// is the snapshot contract itself (segmentset.go) and is deliberately NOT what
	// this gate bounds.
	bm25ResidentPointerBytes = 8
	// bm25ResidentNonPointerCap is what EVERYTHING ELSE on the append path may
	// allocate per publish, and it is the number this gate exists to hold. Before the
	// incremental shape the corpus-statistics fold allocated per RESIDENT SEGMENT —
	// a probe registration and one map assign per field, each — so this term grew
	// with the segment count; now it is the appended segment's own fields plus the
	// amortized share of the route flatten.
	bm25ResidentNonPointerCap = 1 << 16
)

// bm25CostDocs builds a seed batch. It takes NO generation parameter: every call
// site seeds generation zero, and the window below re-stamps its own generation on
// the ids it re-publishes, so a generation knob here was a promise of variation the
// file does not keep.
func bm25CostDocs(prefix string, from, n int) []searchengine.Document {
	docs := make([]searchengine.Document, n)
	for i := range docs {
		docs[i] = searchengine.Document{
			ID:     fmt.Sprintf("%s%020d", prefix, from+i),
			Fields: map[string]string{searchengine.FieldSummary: fmt.Sprintf("term%d gen0", (from+i)%97)},
		}
	}
	return docs
}

// TestBM25PublishCostDoesNotScaleWithCorpus is R1 over the real BM25 format.
func TestBM25PublishCostDoesNotScaleWithCorpus(t *testing.T) {
	small, smallSegs := measureBM25PublishCost(t, bm25CostSmallM)
	large, largeSegs := measureBM25PublishCost(t, bm25CostLargeM)

	// THE SEEDED AND THE FINAL SEGMENT COUNTS ARE BOTH REPORTED, because they are
	// not the same number and the difference is this arm's whole subject: the
	// window publishes onto a merge-disabled engine, so AggregateStats is walking a
	// resident set that GROWS across the reading. Logging the seeded count alone
	// understates the per-publish work every figure here was taken against.
	t.Logf("bm25 publish cost: M=%d seeded_segments=%d final_segments=%d alloc=%d B/publish",
		bm25CostSmallM, bm25CostSegments, smallSegs, small)
	t.Logf("bm25 publish cost: M=%d seeded_segments=%d final_segments=%d alloc=%d B/publish",
		bm25CostLargeM, bm25CostSegments, largeSegs, large)

	require.LessOrEqual(t, large, uint64(bm25CostAllocCap),
		"a BM25 publish at M=%d allocated %d bytes; the corpus-statistics fold is per-segment, so per-publish allocation must not track the corpus",
		bm25CostLargeM, large)

	ratio := float64(large) / float64(small)
	require.LessOrEqual(t, ratio, bm25CostAllocRatioCap,
		"BM25 per-publish allocation grew %.2fx across a %.0fx corpus range (%d B against %d B)",
		ratio, float64(bm25CostLargeM)/float64(bm25CostSmallM), large, small)
}

// TestBM25PublishCostDoesNotScaleWithResidentSegments is the per-publish-cost
// requirement of the resident-growth work: a publish at 16,384 resident segments
// must cost the same order as one at 256.
//
// THE INSTRUMENT IS ALLOCATED BYTES, NOT A VISIT COUNT, and that is forced rather
// than preferred: this format's fold type-asserts s.(*mappedSegment) and silently
// CONTINUES past anything else (format.go), so a counting wrapper segment inside
// the bm25 arm is skipped and its counter reads zero — a zero indistinguishable
// from success. Allocated bytes from runtime.MemStats is the instrument this file
// already uses, and it cannot be skipped by a type assert. The exact per-segment
// VISIT count is asserted in the searchengine package, where countingFanoutFormat
// unwraps deliberately.
//
// WHAT IS BOUNDED IS EVERYTHING EXCEPT THE COPY-ON-WRITE POINTER FLOOR. See
// bm25ResidentPointerBytes: an append must copy one pointer per resident entry, so
// a gate demanding a flat TOTAL would be demanding the snapshot contract be broken.
// The residual — what the publish allocates BEYOND that floor — is what the
// corpus-statistics fold used to grow, and it is what this gate holds flat.
func TestBM25PublishCostDoesNotScaleWithResidentSegments(t *testing.T) {
	small := measureBM25ResidentCost(t, bm25ResidentSmall)
	large := measureBM25ResidentCost(t, bm25ResidentLarge)

	floorSmall := uint64(bm25ResidentPointerBytes * bm25ResidentSmall)
	floorLarge := uint64(bm25ResidentPointerBytes * bm25ResidentLarge)
	t.Logf("bm25 publish cost: corpus=%d resident_segments=%d alloc=%d B/publish pointer_floor=%d B",
		bm25ResidentCorpus, bm25ResidentSmall, small, floorSmall)
	t.Logf("bm25 publish cost: corpus=%d resident_segments=%d alloc=%d B/publish pointer_floor=%d B",
		bm25ResidentCorpus, bm25ResidentLarge, large, floorLarge)

	require.Positive(t, small,
		"an allocation instrument reading zero cannot tell a cheap publish from a publish that never happened")
	require.Greater(t, large, floorLarge,
		"PRECONDITION: the reading must at least contain the pointer floor it is being measured against")

	require.LessOrEqual(t, large-floorLarge, uint64(bm25ResidentNonPointerCap),
		"a BM25 publish at %d resident segments allocated %d bytes beyond the copy-on-write pointer floor; "+
			"the corpus-statistics fold must not be per-resident-segment",
		bm25ResidentLarge, large-floorLarge)

	ratio := float64(large-floorLarge) / float64(small-floorSmall)
	require.LessOrEqual(t, ratio, bm25CostAllocRatioCap,
		"BM25 per-publish allocation beyond the pointer floor grew %.2fx across a %dx resident-segment range (%d B against %d B)",
		ratio, bm25ResidentLarge/bm25ResidentSmall, large-floorLarge, small-floorSmall)
}

// measureBM25ResidentCost seeds ONE fixed corpus across the given number of
// segments and amortizes allocated bytes per publish over a window spanning
// several route flattens. The corpus is held constant so the flatten's own
// O(corpus) rebuild contributes the same amount to both cells and the segment
// count is the only thing that moves.
func measureBM25ResidentCost(t *testing.T, segments int) uint64 {
	t.Helper()
	e := searchengine.New[Query, *CorpusStats](Format{}, searchengine.Options{
		MinSegmentDocs:     1,
		DeletesPctAllowed:  searchengine.MergeDisabledDeadRatio,
		SegmentCountTarget: searchengine.MergeDisabledCountTarget,
	})
	t.Cleanup(e.Close)

	per := bm25ResidentCorpus / segments
	require.Equal(t, bm25ResidentCorpus, per*segments, "the corpus must divide evenly into the segment count")
	for s := range segments {
		_, err := e.AddSealAndSupersede(bm25CostDocs("seed", s*per, per))
		require.NoError(t, err)
	}
	require.Equal(t, bm25ResidentCorpus, e.DistinctResidentDocCount(),
		"the corpus is held CONSTANT across the cells; only the segment count moves")
	require.Equal(t, segments, e.ResidentSegmentCount(),
		"the resident segment count is this gate's independent variable")

	ids := bm25CostDocs("window", 0, bm25CostBatch)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for i := range bm25CostWindow {
		for d := range ids {
			ids[d].Fields[searchengine.FieldSummary] = fmt.Sprintf("term%d gen%d", d%97, i+1)
		}
		if _, err := e.AddSealAndSupersede(ids); err != nil {
			require.NoError(t, err)
		}
	}
	runtime.ReadMemStats(&after)
	return (after.TotalAlloc - before.TotalAlloc) / bm25CostWindow
}

// measureBM25PublishCost seeds one engine over the real format and amortizes
// allocated bytes per publish over a window spanning several route flattens. The
// window re-publishes ONE id set, so the corpus size does not move while its cost
// is being read.
func measureBM25PublishCost(t *testing.T, m int) (allocPerPublish uint64, finalSegments int) {
	t.Helper()
	e := searchengine.New[Query, *CorpusStats](Format{}, searchengine.Options{
		MinSegmentDocs:     1,
		DeletesPctAllowed:  searchengine.MergeDisabledDeadRatio,
		SegmentCountTarget: searchengine.MergeDisabledCountTarget,
	})
	t.Cleanup(e.Close)

	per := m / bm25CostSegments
	require.Equal(t, m, per*bm25CostSegments, "the corpus must divide evenly into the segment count")
	for s := range bm25CostSegments {
		_, err := e.AddSealAndSupersede(bm25CostDocs("seed", s*per, per))
		require.NoError(t, err)
	}
	require.Equal(t, m, e.DistinctResidentDocCount(), "the seeded corpus is this gate's independent variable")
	require.Len(t, e.ResidentSegmentIDs(), bm25CostSegments,
		"AggregateStats walks the resident segments on every publish, so the segment count is part of what is being measured")

	ids := bm25CostDocs("window", 0, bm25CostBatch)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for i := range bm25CostWindow {
		for d := range ids {
			ids[d].Fields[searchengine.FieldSummary] = fmt.Sprintf("term%d gen%d", d%97, i+1)
		}
		if _, err := e.AddSealAndSupersede(ids); err != nil {
			require.NoError(t, err)
		}
	}
	runtime.ReadMemStats(&after)

	require.Greater(t, after.TotalAlloc, before.TotalAlloc,
		"an allocation instrument reading zero cannot tell a cheap publish from a publish that never happened")
	return (after.TotalAlloc - before.TotalAlloc) / bm25CostWindow, len(e.ResidentSegmentIDs())
}
