// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
)

// THE FOUR CONSTANTS BELOW ARE THE CONSOLIDATION-ONLY MERGE ALLOCATION BASELINE,
// measured through measureConsolidationMerge on the UNMODIFIED tree — the branch's
// merge-base, fb68ea2f. NEVER REGENERATE THEM.
//
// They are the red-first evidence the post-change bound rests on. The bound is
// declared once, here, and cited rather than restated elsewhere:
//
//	allocB < 2 * allocA, given outputB >= 2 * outputA
//
// The pre-change tree VIOLATES it — 4,785,720 against a 2,842,848 threshold, an
// allocation ratio of 3.366 against an output ratio of 3.765, allocations tracking
// output almost exactly, which is the buffered-not-streamed signature. Freezing
// the violating numbers is what keeps that statement checkable forever: it is an
// assertion about four literals rather than about whichever tree is checked out,
// so it still reads the same after the fix lands, where a `go test` expecting a
// red would have inverted.
//
// THEY REPLACE AN EARLIER SET THAT MEASURED THE WRONG THING, and the reason is
// recorded so the mistake is not repeated. The previous instrument measured a
// whole ReplaceBucketFields call, which a build-only control showed to be 95-98%
// tokenize-and-seal cost — a term this changeset does not touch. Both sides of the
// old pair were dominated by it, so the bound was unsatisfiable by any merge
// implementation and the red-first pair testified to nothing about the merge. The
// names moved with the instrument: whole-path became consolidation.
//
// Regenerating them from a fixed tree destroys exactly that evidence and leaves
// the post-change bound asserting nothing. Recovery is a plain checkout rather
// than an excavation: extract the tree at fb68ea2f, copy this file's helper and
// fixtures in as a self-contained probe — inlining paddedFieldDocs and
// storedSegFiles, since this file does not exist there — and re-run the capture.
//
// CAPTURED ACROSS THREE CONSECUTIVE RUNS: the outputs were byte-identical every
// run and the allocation spread was under 0.09%, which is itself the evidence that
// the instrument is stable enough for a 2x bound to mean something.
const (
	consolidationBaselineAllocA  = 1421424
	consolidationBaselineOutputA = 608092
	consolidationBaselineAllocB  = 4785720
	consolidationBaselineOutputB = 2289184
)

// measureConsolidationMerge drives ONE real BM25 consolidation and reports the
// bytes it allocated and the bytes of merged segment it produced.
//
// IT MEASURES A CONSOLIDATION THAT BUILDS NOTHING, and that is the whole reason
// this helper was rewritten. The previous instrument measured a whole
// ReplaceBucketFields call, which is 95-98% tokenize-and-seal cost — a term this
// changeset does not touch and which scales with text by construction. The bound
// taken over it was unsatisfiable by ANY merge implementation, including one that
// allocated nothing, and both sides of the red-first pair were dominated by the
// build rather than by the merge.
//
// THE OBVIOUS REPAIR IS A NO-OP, and it is written down so nobody re-proposes it.
// Driving the pass with docs nil AND superseded nil merges nothing at all:
// replaceBucketGroups opens with a guard that returns immediately when docs,
// superseded and priorityLast are all empty, and ReplaceBucketFields routes
// priorityLast as nil. That call allocates nothing and stores no file — it would
// replace an unsatisfiable gate with a vacuous one.
//
// WHAT WORKS is passing the RESIDENT SEGMENT IDS as priorityLast, which is the
// shape production already uses for exactly this purpose: absorbBuildWindowSurvivors
// calls replaceBucketGroups with no documents and no supersessions and only a
// priority set, and its own comment records that seeding that set is the only work
// such a call has. The seed reaches the merge because the partition spans come from
// a walk of the RESIDENT set, independent of docs and superseded, so every
// partition those segments span is marked dirty and consolidated.
//
// THE SECOND SEED MUST NOT CONSOLIDATE. Two ReplaceBucketFields passes leave ONE
// resident segment per partition, and a merge-of-one reproduces its constituent's
// content hash, so nothing is stored and the measurement is empty. The second seed
// therefore goes through replaceBucketGroups with pass one's ids as EXCLUDE, so it
// builds without merging and leaves two resident segments for the measured pass.
//
// THE OnMerge HOOK IS INSIDE THE WINDOW ON PURPOSE. It fires synchronously within
// the swap, so the reclaim and its L2 Put are measured — and the envelope
// concatenation this changeset removes lived on exactly that path. A window
// excluding it would measure half the change. persistResident stays OUTSIDE.
//
// IT MEASURES TotalAlloc, NOT A SAMPLED HeapAlloc PEAK. TotalAlloc is cumulative
// and exact, and a peak can never exceed the total, so a bound on the total bounds
// the peak with no sampling window that could miss a spike.
//
// NOTHING MAY RUN CONCURRENTLY WITH THE MEASURED WINDOW. Another goroutine's
// allocations land in the same TotalAlloc delta, and the bound would then be
// measuring noise. Callers must not mark themselves t.Parallel().
func measureConsolidationMerge(t *testing.T, docs []searchengine.Document) (allocated, outputBytes int64) {
	t.Helper()

	ctx := context.Background()
	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))
	gt, name := kgtypes.GraphCode, "consolidation-baseline"
	half := len(docs) / 2
	require.Positive(t, half, "the corpus must split into two non-empty seeds")

	// SEED ONE, unmeasured.
	require.NoError(t, mgr.ReplaceBucketFields(ctx, gt, name, nil, docs[:half]))

	dm := mgr.bm25ManagerFor(gt, name)
	firstResident := dm.engine.ResidentSegmentIDs()
	require.NotEmpty(t, firstResident, "the first seed published nothing")

	// SEED TWO, unmeasured, and EXCLUDED so it builds without consolidating.
	_, _, err := replaceBucketGroups(dm, nil, docs[half:], firstResident,
		dm.engine.DistinctResidentDocCount()+len(docs[half:]), nil)
	require.NoError(t, err)
	_, err = dm.persistResident()
	require.NoError(t, err)

	resident := dm.engine.ResidentSegmentIDs()
	require.GreaterOrEqual(t, len(resident), 2,
		"only %d resident segment(s) after the excluded seed — the exclude did not take, and the measured "+
			"pass would be a merge-of-one that stores nothing", len(resident))

	cacheDir := graphCacheDirFor(mgr.cacheDir, gt, name, bm25.New().Name())
	before := storedSegFiles(t, cacheDir)

	// THE MEASURED WINDOW. Nothing else goes inside it.
	var msBefore, msAfter runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&msBefore)
	_, _, mergeErr := replaceBucketGroups(dm, nil, nil, nil, dm.engine.DistinctResidentDocCount(), resident)
	runtime.ReadMemStats(&msAfter)
	require.NoError(t, mergeErr)

	// THE MERGED OUTPUT IS WHAT APPEARED, identified by set difference rather than
	// by size or recency: a segment id is a content hash, so a consolidated
	// segment's stored copy is a file that did not exist before this pass.
	fresh := storedSegFiles(t, cacheDir)
	var added []string
	for path := range fresh {
		if !before[path] {
			added = append(added, path)
		}
	}
	require.NotEmpty(t, added,
		"the consolidation stored no new segment, so there is nothing to measure against")

	for _, path := range added {
		info, statErr := os.Stat(path)
		require.NoError(t, statErr)
		require.Positive(t, info.Size(), "the consolidation stored an empty segment at %s", path)
		outputBytes += info.Size()
	}
	t.Logf("consolidated %d partition(s) into %d bytes of merged segment", len(added), outputBytes)

	return int64(msAfter.TotalAlloc - msBefore.TotalAlloc), outputBytes
}

// storedSegFiles returns the set of stored segment paths in a pool's L2 cache
// root. Absent directory reads as empty rather than as an error, because the
// first pass is what creates it.
func storedSegFiles(t *testing.T, dir string) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return map[string]bool{}
	}
	require.NoError(t, err)
	out := make(map[string]bool, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".seg") {
			continue
		}
		out[filepath.Join(dir, e.Name())] = true
	}
	return out
}

// paddedFieldDocs is bm25FieldDocs with each field's text repeated, so the merged
// output grows several-fold while the per-document merge state does not.
//
// THE DOCUMENT COUNT IS NOT A PARAMETER, and that is deliberate rather than a
// simplification: holding it EQUAL across both fixtures is the invariant the whole
// comparison rests on, so exposing it as an argument would invite the one change
// that silently invalidates every number taken over these fixtures.
//
// THE PADDING, NOT A BIGGER CORPUS, IS THE POINT. Merge state is per-DOCUMENT — the
// last-wins winner map, the member list, the id remap, the per-field document
// lengths — so raising the document count grows the state and the output
// together, and a slope measured that way would prove nothing about buffering.
// Repeating the text of a FIXED document count grows only the output.
func paddedFieldDocs(repeat int) []searchengine.Document {
	docs := bm25FieldDocs(baselineDocCount)
	for i := range docs {
		padded := make(map[string]string, len(docs[i].Fields))
		for field, text := range docs[i].Fields {
			var b strings.Builder
			for r := range repeat {
				// Each repetition carries its own token so the padding adds real
				// dictionary entries rather than inflating one term's frequency —
				// a merge over one repeated term would exercise almost none of the
				// dictionary writer this measurement is about.
				fmt.Fprintf(&b, "%s pad%dtok%d ", text, r, i)
			}
			padded[field] = b.String()
		}
		docs[i].Fields = padded
	}
	return docs
}

// TestConsolidationMergeAllocationBaseline is an INSTRUMENT test, not a property
// gate, and the distinction is deliberate.
//
// It proves the harness runs, drives a real consolidation, and produces two
// fixtures whose outputs differ by the factor the whole slope argument rests on.
// It asserts ONLY that vacuity control, so it is green in every tree state this
// changeset produces. The allocation bound itself is asserted in two other
// places: arithmetically over the frozen constants above, which is the red-first
// evidence, and behaviourally over the current tree by
// TestMergeAllocatesNoOutputSizedBuffer once the fix has landed.
//
// It logs the fresh numbers beside the FROZEN ones so a re-run is
// self-documenting and drift is visible without instrumenting anything.
//
// LOGGING THE FROZEN FOUR PUTS THE BEFORE AND AFTER ON ONE SCREEN, which is the
// cheapest way for a reader to see what the change bought without running
// anything else. The frozen pair is also asserted arithmetically by this file's
// own gate and behaviourally by TestMergeAllocatesNoOutputSizedBuffer.
func TestConsolidationMergeAllocationBaseline(t *testing.T) {
	allocA, outputA := measureConsolidationMerge(t, paddedFieldDocs(baselineLeanRepeat))
	t.Logf("fixture A: allocated %d bytes producing a %d-byte merged segment", allocA, outputA)

	allocB, outputB := measureConsolidationMerge(t, paddedFieldDocs(baselineFatRepeat))
	t.Logf("fixture B: allocated %d bytes producing a %d-byte merged segment", allocB, outputB)

	t.Logf("frozen pre-change baseline: A alloc=%d output=%d, B alloc=%d output=%d",
		consolidationBaselineAllocA, consolidationBaselineOutputA,
		consolidationBaselineAllocB, consolidationBaselineOutputB)

	require.Greater(t, outputB, outputA*2,
		"fixture B's merged output (%d bytes) is not at least twice fixture A's (%d bytes) — "+
			"the slope comparison every allocation bound over these fixtures rests on is vacuous",
		outputB, outputA)

	// THE SAME CONTROL OVER THE FROZEN PAIR. It is the one part of the frozen
	// capture that is still true and still checkable: the two fixtures really did
	// differ several-fold in output when they were measured, so any later argument
	// built on them is at least not comparing two identical runs.
	require.Greater(t, consolidationBaselineOutputB, consolidationBaselineOutputA*2,
		"the frozen fixtures' outputs are not several-fold apart, so the capture they came from was vacuous")
}

const (
	// baselineDocCount is held EQUAL across both fixtures on purpose: the two
	// differ only in text per document, so the per-document merge state is
	// identical and the output size is the only thing that moved.
	//
	// IT IS DELIBERATELY SMALL AND THE TEXT DELIBERATELY HEAVY, which is the
	// opposite of the obvious choice and is what makes the measurement mean
	// anything. Merge cost has a large per-document component, so a corpus of many
	// tiny documents buries the output-sized copies under it: measured at 2000
	// documents of one repetition each, quadrupling the text moved the output 1.95x
	// while allocations moved only 1.63x, and the bound was already satisfied on
	// the UNFIXED tree. At 400 documents of 20 repetitions the output-sized copies
	// dominate and allocations track output almost exactly.
	baselineDocCount = 400
	// baselineLeanRepeat and baselineFatRepeat are the two padding factors. The
	// ratio between them sets the output slope the whole comparison rests on.
	baselineLeanRepeat = 20
	baselineFatRepeat  = 80
)

// TestMergeAllocatesNoOutputSizedBuffer is THE PLAN'S HEADLINE CLAIM, AS A
// MEASUREMENT: a merge's allocations stay FLAT while the segment it produces
// grows several-fold.
//
// IT MEASURES A CONSOLIDATION THAT BUILDS NOTHING. measureConsolidationMerge
// seeds the corpus outside the measured window and then consolidates the resident
// set with no documents and no supersessions, so the only work inside the window
// is harvest-and-merge — including the reclaim's L2 Put, which is where the
// envelope concatenation this changeset removes used to live.
//
// ITS RED-FIRST EVIDENCE IS AN ARTIFACT, NOT A PARAGRAPH. The consolidationBaseline
// constants above were measured through this same helper, over these same two
// fixtures, on the UNMODIFIED tree at this branch's merge-base, and this file's
// arithmetic gate asserts that they VIOLATE the bound asserted here. The numbers
// are declared once, in that const block, and are deliberately not repeated here.
//
// THE BOUND IS DECLARED ONCE, in the const block's paragraph, and cited rather
// than restated: allocB < 2 * allocA, given outputB >= 2 * outputA.
//
// IT MEASURES bm25 AND MAKES NO HNSW CLAIM. hnsw's merge re-inserts every survivor
// into a fresh graph whose vector block is output-sized by algorithm, so no
// zero-allocation claim is available there and none is made. The hnsw-side gate is
// TestEncodeGraphV3ToAllocatesNoOutputBuffer and it is scoped to the ENCODER.
//
// A SECOND, INDEPENDENT INSTRUMENT CORROBORATES THIS ONE and is named so the two
// are read together: TestStreamedMergePeakHeapBounded in formats/bm25 measures the
// WRITER alone, in a different package, at a different layer, with no engine and
// no L2 in its window. It is kept rather than folded in precisely because it is
// not a second view of this measurement.
//
// NOT t.Parallel(), and nothing may run concurrently with it: another goroutine's
// allocations would land in the same TotalAlloc delta and the bound would be
// measuring noise.
func TestMergeAllocatesNoOutputSizedBuffer(t *testing.T) {
	allocA, outputA := measureConsolidationMerge(t, paddedFieldDocs(baselineLeanRepeat))
	t.Logf("fixture A: allocated %d bytes producing a %d-byte merged segment", allocA, outputA)

	allocB, outputB := measureConsolidationMerge(t, paddedFieldDocs(baselineFatRepeat))
	t.Logf("fixture B: allocated %d bytes producing a %d-byte merged segment", allocB, outputB)

	// THE VACUITY CONTROL FIRST. Without it two fixtures producing similar outputs
	// would satisfy the allocation leg trivially.
	require.Greater(t, outputB, outputA*2,
		"fixture B's merged output (%d bytes) is not at least twice fixture A's (%d bytes) — "+
			"the bound below is vacuous", outputB, outputA)

	require.Less(t, allocB, allocA*2,
		"merge allocations grew with the output (%d then %d bytes, for %d then %d bytes of segment) — "+
			"that is an output-sized buffer, which is the state this changeset removes",
		allocA, allocB, outputA, outputB)

	// THE OUTPUTS MUST MATCH THE FROZEN CAPTURE BYTE FOR BYTE. The stored format is
	// not what this changeset set out to alter, and two fixtures producing the same
	// bytes here as on the unmodified tree is independent corroboration that it did
	// not — measured through an instrument that has nothing to do with the goldens.
	require.Equal(t, int64(consolidationBaselineOutputA), outputA,
		"fixture A's merged output moved from the frozen capture — the stored format changed")
	require.Equal(t, int64(consolidationBaselineOutputB), outputB,
		"fixture B's merged output moved from the frozen capture — the stored format changed")
}
