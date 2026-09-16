// SPDX-License-Identifier: Apache-2.0

// publish_cost_test.go — the two publish-cost gates of issue #172: a publish must
// cost what its OWN segment holds rather than what the engine holds (R1), and
// twenty concurrent publishers must not multiply that cost (R2).
//
// THESE ARE TESTS RATHER THAN BENCHMARKS ON PURPOSE. CI runs `go test` without
// -bench, so a Benchmark-only gate never executes on a pull request and the
// regression it guards against returns unobserved. What makes a Test viable here
// is that the PRIMARY gate is ALLOCATED BYTES per publish, which is a property of
// the code rather than of the machine; CPU is carried alongside as a pathology
// fence with generous headroom, never as the gate.
//
// THE MEASUREMENT WINDOW IS THE INSTRUMENT HAZARD THIS FILE EXISTS TO AVOID. The
// two-level route flattens once every routeTailLimit publishes, so a SHORT window
// measures whichever side of a flatten it happened to land on: a 20-publish window
// placed just after one reads perfectly flat in corpus size, and the same window
// placed just before one reads a sevenfold ratio. Every per-publish figure below is
// amortized over publishCostWindow publishes, which is four flattens' worth, so the
// amortized flatten is inside every figure. Do not shorten it to make the suite
// faster; shorten the corpus instead.
//
// THE WINDOW RE-PUBLISHES A FIXED ID SET rather than fresh ids per publish, so the
// distinct corpus size M is exactly what the seed established for the whole
// measurement. A window of fresh ids grows M while it measures the cost of M, which
// mixes the independent variable into the reading — and at the small corpus it
// triples it.

package searchengine

import (
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	// publishCostSegments is the resident segment count both corpora are seeded to.
	// It is the count the external report's host reached between drains, so the
	// measurement is taken at the shape the defect was reported at.
	publishCostSegments = 1637
	// publishCostSmallM and publishCostLargeM are the two corpus sizes the ratio
	// gate spans — a 38x range, which is what makes a ratio meaningful.
	publishCostSmallM = 13096
	publishCostLargeM = 499285
	// publishCostBatch is one publish: the per-partition share of an embed lease is
	// single digits at these corpus sizes.
	publishCostBatch = 8
	// publishCostWindow is how many publishes each per-publish figure is amortized
	// over. It is FOUR TIMES the settled tail limit of 1024, so the window spans
	// four flattens and no figure can be an artifact of where it started. The
	// derivation is pinned by TestPublishCostWindowSpansSeveralFlattens, which
	// reddens if the tail limit is ever raised past a quarter of this.
	publishCostWindow = 4096
	// publishCostAllocCap is R1's primary gate: bytes allocated per publish at the
	// large corpus. 42.05 MB at v0.10.5.
	publishCostAllocCap = 1 << 20
	// publishCostAllocRatioCap is R1's secondary gate: how much the per-publish
	// allocation may grow across the 38x corpus range. 32.3x at v0.10.5.
	publishCostAllocRatioCap = 8.0
	// publishCostCPUFence is a PATHOLOGY FENCE, not the gate: a wall-derived number
	// on a loaded runner measures the runner. 59.76 ms at v0.10.5, so it is red by
	// thirty times there and has thirty times' headroom afterwards.
	publishCostCPUFence = 2 * time.Millisecond
)

// publishCostEngine builds the engine the two gates measure: the mock format over
// the options segmentdist constructs its production engines with
// (manager_factory.go — MergeDisabledCountTarget / MergeDisabledDeadRatio), so the
// count-triggered merge never fires and the resident set grows exactly as it does
// on the machine the issue was reported from.
func publishCostEngine(t testing.TB) *SegmentedIndex[mockQuery, mockStats] {
	t.Helper()
	return closeOnCleanup(t, New[mockQuery, mockStats](mockFormat{}, Options{
		MinSegmentDocs:     1,
		DeletesPctAllowed:  MergeDisabledDeadRatio,
		SegmentCountTarget: MergeDisabledCountTarget,
	}))
}

// publishCostDocs builds n documents whose ids are prefix-scoped and whose content
// carries the generation, so re-publishing the same ids under a new generation
// produces a segment with a DIFFERENT content hash and therefore a real publish
// rather than the idempotent drop.
func publishCostDocs(prefix string, from, n, generation int) []Document {
	docs := make([]Document, n)
	for i := range docs {
		docs[i] = Document{
			ID:     fmt.Sprintf("%s%028d", prefix, from+i),
			Fields: map[string]string{FieldContent: fmt.Sprintf("gen %d", generation)},
		}
	}
	return docs
}

// seedPublishCostCorpus publishes segments segments carrying m distinct ids in
// total, which is the state a merge-disabled engine reaches between drains.
func seedPublishCostCorpus(t testing.TB, e *SegmentedIndex[mockQuery, mockStats], segments, m int) {
	t.Helper()
	per := m / segments
	require.Equal(t, m, per*segments, "the corpus size must divide evenly into the segment count, or the seeded M is not the M the gate names")
	for s := range segments {
		_, err := e.AddSealAndSupersede(publishCostDocs("seed", s*per, per, 0))
		require.NoError(t, err)
	}
}

// processCPU is user+system CPU consumed by this process so far. It is
// process-wide, so it is only read as a fence and only around a window that runs
// nothing else: the engines these tests build have their background merge disabled
// by construction.
func processCPU() time.Duration {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

// publishCost is one amortized reading.
type publishCost struct {
	segments int
	distinct int
	cpu      time.Duration
	alloc    uint64
}

// measurePublishCost seeds an engine to (publishCostSegments, m) and amortizes the
// cost of publishCostWindow publishes over that fixed corpus.
func measurePublishCost(t *testing.T, m int) publishCost {
	t.Helper()
	e := publishCostEngine(t)
	seedPublishCostCorpus(t, e, publishCostSegments, m)

	// PRECONDITIONS, so a fixture that seeded nothing cannot pass the gates by
	// measuring nothing. Both are read from the engine's own surfaces.
	set := e.set.Load()
	require.Len(t, set.entries, publishCostSegments, "the seeded resident segment count is the shape the reading is taken at")
	require.Equal(t, m, e.DistinctResidentDocCount(), "the seeded distinct corpus size is the independent variable of this gate")

	// The window re-publishes ONE id set, so M does not move while M's cost is
	// being read. Resolved before the window so the allocation it costs is not
	// charged to the publishes.
	ids := publishCostDocs("window", 0, publishCostBatch, 0)

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	startCPU := processCPU()
	for i := range publishCostWindow {
		for d := range ids {
			ids[d].Fields[FieldContent] = fmt.Sprintf("gen %d", i+1)
		}
		if _, err := e.AddSealAndSupersede(ids); err != nil {
			require.NoError(t, err)
		}
	}
	cpu := processCPU() - startCPU
	runtime.ReadMemStats(&after)

	require.Equal(t, m, e.DistinctResidentDocCount()-publishCostBatch,
		"the window must not have moved the distinct corpus size beyond its own id set; a moving M makes the ratio a reading of two different experiments")
	require.Greater(t, after.TotalAlloc, before.TotalAlloc,
		"an allocation instrument that reads zero cannot distinguish a cheap publish from a publish that never happened")

	return publishCost{
		segments: len(e.set.Load().entries),
		distinct: e.DistinctResidentDocCount(),
		cpu:      cpu / publishCostWindow,
		alloc:    (after.TotalAlloc - before.TotalAlloc) / publishCostWindow,
	}
}

// TestPublishCostDoesNotScaleWithCorpus is R1: publishing a sealed segment costs
// work proportional to that segment's own members, not to the engine's whole
// resident id set.
//
// At v0.10.5 every publish copied the entire externalID→SegmentID route map, so
// the per-publish allocation was the corpus: 42 MB at the large corpus against
// 1.3 MB at the small one.
func TestPublishCostDoesNotScaleWithCorpus(t *testing.T) {
	small := measurePublishCost(t, publishCostSmallM)
	large := measurePublishCost(t, publishCostLargeM)

	t.Logf("publish cost: M=%d segments=%d alloc=%d B/publish cpu=%v/publish",
		small.distinct, small.segments, small.alloc, small.cpu)
	t.Logf("publish cost: M=%d segments=%d alloc=%d B/publish cpu=%v/publish",
		large.distinct, large.segments, large.alloc, large.cpu)

	require.LessOrEqual(t, large.alloc, uint64(publishCostAllocCap),
		"a publish at M=%d allocated %d bytes; publication must cost the segment's own members, not the resident corpus",
		publishCostLargeM, large.alloc)

	ratio := float64(large.alloc) / float64(small.alloc)
	require.LessOrEqual(t, ratio, publishCostAllocRatioCap,
		"per-publish allocation grew %.2fx across a %.1fx corpus range (%d B at M=%d against %d B at M=%d); the publish is still reading the whole corpus",
		ratio, float64(publishCostLargeM)/float64(publishCostSmallM),
		large.alloc, publishCostLargeM, small.alloc, publishCostSmallM)

	require.LessOrEqual(t, large.cpu, publishCostCPUFence,
		"pathology fence: a publish at M=%d burned %v of CPU", publishCostLargeM, large.cpu)
}

// TestConcurrentPublishersDoNotMultiplyCost is R2: twenty concurrent publishers —
// the shipped embed-workers default — cost no more than a bounded multiple of one.
//
// At v0.10.5 a publisher that lost the publish CAS repeated the whole corpus copy,
// so twenty workers paid between five and eight times the serial allocation and
// between eight and thirteen times the serial CPU for the same forty publishes.
func TestConcurrentPublishersDoNotMultiplyCost(t *testing.T) {
	const (
		// 400 PUBLISHES, NOT 40, AND THE REASON IS THE CPU READING. Allocation per
		// publish is deterministic at any window length, but CPU per publish over a
		// forty-publish window is dominated by the fixed cost of starting twenty
		// goroutines: measured across a whole-package run that read 5.8x on a window
		// this fix had already made 1.01x in allocation. A window ten times longer
		// amortizes the spawn cost and reads the work.
		publishes = 400
		workers   = 20
		// allocRatioCap is the primary gate: allocation is a property of the code.
		allocRatioCap = 1.5
		// cpuRatioCap is the secondary gate. It is looser than the allocation gate
		// because scheduling twenty goroutines costs real CPU that has nothing to do
		// with what a publish copies.
		cpuRatioCap = 4.0
	)

	// THE ENGINES ARE SEEDED HERE, NOT INSIDE THE MEASUREMENT, so the measurement
	// takes no testing handle at all. A helper that both fails the test through
	// require and hands errors back has two failure channels, and which one carries
	// a given failure stops being readable from its signature.
	serialEngine := publishCostEngine(t)
	seedPublishCostCorpus(t, serialEngine, publishCostSegments, publishCostSmallM)
	parallelEngine := publishCostEngine(t)
	seedPublishCostCorpus(t, parallelEngine, publishCostSegments, publishCostSmallM)

	serial, serialErrs := measureConcurrentPublish(serialEngine, 1, publishes)
	parallel, parallelErrs := measureConcurrentPublish(parallelEngine, workers, publishes)
	require.Empty(t, serialErrs, "every publish in the serial arm must have landed")
	require.Empty(t, parallelErrs, "every publish in the concurrent arm must have landed")

	// EVERY PUBLISH MUST HAVE LANDED IN BOTH ARMS, asserted here rather than inside
	// the helper: a dropped publish would make the two arms measure different
	// amounts of work, and the ratio below would be a reading of that difference.
	require.Equal(t, publishCostSegments+publishes, serial.segments)
	require.Equal(t, publishCostSegments+publishes, parallel.segments)

	t.Logf("publish cost: 1 worker alloc=%d B/publish cpu=%v/publish", serial.alloc, serial.cpu)
	t.Logf("publish cost: %d workers alloc=%d B/publish cpu=%v/publish", workers, parallel.alloc, parallel.cpu)

	allocRatio := float64(parallel.alloc) / float64(serial.alloc)
	require.LessOrEqual(t, allocRatio, allocRatioCap,
		"%d concurrent publishers allocated %.2fx the serial cost per publish (%d B against %d B); a lost publish race is still repeating the build",
		workers, allocRatio, parallel.alloc, serial.alloc)

	cpuRatio := float64(parallel.cpu) / float64(serial.cpu)
	require.LessOrEqual(t, cpuRatio, cpuRatioCap,
		"%d concurrent publishers burned %.2fx the serial CPU per publish (%v against %v)",
		workers, cpuRatio, parallel.cpu, serial.cpu)
}

// measureConcurrentPublish runs `publishes` eight-document publishes spread over
// `workers` goroutines against one freshly seeded engine, and amortizes the cost
// over the publishes.
//
// Each worker publishes under its OWN id prefix. Two workers publishing identical
// documents would mint one content hash and the second publish would be dropped as
// idempotent — the measurement would then be of nineteen publishes and a drop.
//
// IT TAKES NO TESTING HANDLE AND RETURNS THE WORKERS' ERRORS, and three rules meet
// at that shape. A worker goroutine may not call Fatal or FailNow — those are
// undefined off the test's own goroutine. A helper that calls Error instead has
// become an assertion facility, putting the failure somewhere other than the Test
// that gives it meaning. And a helper that takes a testing handle AND returns
// errors has two failure channels, so which one carries a failure is no longer
// readable from its signature. Taking the seeded engine and returning what went
// wrong satisfies all three: the goroutines report nothing, and the Test does the
// failing.
func measureConcurrentPublish(
	e *SegmentedIndex[mockQuery, mockStats], workers, publishes int,
) (publishCost, []error) {
	per := publishes / workers

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	startCPU := processCPU()

	var (
		errMu sync.Mutex
		errs  []error
		wg    sync.WaitGroup
	)
	for w := range workers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := range per {
				if _, err := e.AddSealAndSupersede(
					publishCostDocs(fmt.Sprintf("w%d-", w), 0, publishCostBatch, i+1)); err != nil {
					errMu.Lock()
					errs = append(errs, fmt.Errorf("worker %d publish %d: %w", w, i, err))
					errMu.Unlock()
					return
				}
			}
		}(w)
	}
	wg.Wait()

	cpu := processCPU() - startCPU
	runtime.ReadMemStats(&after)

	return publishCost{
		segments: len(e.set.Load().entries),
		distinct: e.DistinctResidentDocCount(),
		cpu:      cpu / time.Duration(publishes),
		alloc:    (after.TotalAlloc - before.TotalAlloc) / uint64(publishes),
	}, errs
}
