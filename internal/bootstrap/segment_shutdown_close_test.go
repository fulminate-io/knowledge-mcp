// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// mergerFrame is the RUNNING frame of a segment engine's merger goroutine, as
// runtime.Stack renders it. The trailing ".func1()" is load-bearing: each merger
// contributes TWO lines naming startMerger — the running closure and the
// "created by ...startMerger" attribution beneath it — so matching the bare
// method name double-counts every goroutine.
const mergerFrame = "SegmentedIndex[...]).startMerger.func1()"

// mergerGoroutines counts live segment-engine merger goroutines by frame name.
// Counting goroutines at large (runtime.NumGoroutine) would answer a different
// question, and every unrelated daemon goroutine would move the number.
func mergerGoroutines() int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return strings.Count(string(buf[:n]), mergerFrame)
		}
		buf = make([]byte, 2*len(buf))
	}
}

// TestShutdownClosesSegmentEngines pins the wiring, not the mechanism.
//
// segmentdist proves separately that Manager.Close stops every engine's merger.
// What this closes is the other failure: a Close with no production caller
// passes that test while leaving a real daemon leaking a ticker per graph for
// its whole lifetime. So it drives the REAL shutdown closure — drainOnShutdown —
// exactly as the backlog-drain test above it does, and asserts the count at the
// end rather than asserting that some method was called.
//
// The rise before the fall is the known-positive: requiring only a return to
// baseline would be satisfied by a client that never constructed an engine, so
// the count must first CLIMB by at least the two engines — one HNSW, one BM25 —
// that writing to a single graph is guaranteed to construct. The floor is a
// GreaterOrEqual rather than an exact count because the drain reaches the
// client's other dirty graphs too and constructs their engines as it goes; what
// the known-positive needs is that the number was genuinely non-zero, and what
// the assertion needs is that ALL of them, however many, are gone afterward.
func TestShutdownClosesSegmentEngines(t *testing.T) {
	// No t.Parallel: this reads a process-wide goroutine census, and Go resumes
	// parallel tests only after the sequential pass, so staying sequential keeps a
	// sibling's engines out of the count.
	const repo = "shutdownCloseRepo"
	const enginesForOneGraph = 2 // one HNSW, one BM25

	baseline := quiescedMergerBaseline(t)

	ctx := opCtx()
	c, _ := buildReconcileClientWith(t, 100, repo)
	require.NoError(t, c.segmentMgr.AddAndMarkDirty(ctx, kgtypes.GraphCode, repo, fastloadVecDocs(repo, 32)))
	require.NoError(t, c.segmentMgr.AddAndMarkDirtyFields(ctx, kgtypes.GraphCode, repo, fastloadVecDocs(repo, 32)))

	// KNOWN-POSITIVE: the daemon really is holding live mergers before the drain.
	require.GreaterOrEqual(t, mergerGoroutines(), baseline+enginesForOneGraph,
		"the fixture must have constructed this graph's HNSW and BM25 engines, each with a live merger; "+
			"without that the drop below would prove nothing")

	// The close is gated on the same readiness flag as the backlog drain, because
	// markPipelineReady is the atomic Store that publishes c.segmentMgr.
	c.markPipelineReady()
	c.drainOnShutdown()

	require.Equal(t, baseline, waitForMergers(baseline, 5*time.Second),
		"a clean shutdown must close the segment engines; a surviving merger is a 50ms ticker that "+
			"outlives every graph the daemon ever touched, since nothing removes an engine from the memo maps")
}

// quiescedMergerBaseline samples the merger census only once it has STOPPED
// FALLING, and fails loudly if it never settles.
//
// THE BUG THIS CLOSES IS IN THE SAMPLING, NOT IN THE CODE UNDER TEST.
// Manager.Close is signal-only by contract — its own doc says it "does NOT wait"
// — so a prior test's Close returns while that manager's mergers are still
// observing the closed channel and exiting. Reading the census at that instant
// captures goroutines that are already on their way out, and they are gone by
// the time the known-positive reads again a few milliseconds later. The count
// then FALLS across the two reads, so the rise the known-positive requires never
// materializes and the test fails at its own control.
//
// Reproduced deterministically at GOMAXPROCS=1, where the constrained scheduler
// widens the exit delay: baseline sampled 4, the known-positive then saw 2
// against a want of 6 — the exact reported numbers. The source is the
// immediately preceding test; running that pair alone reproduces it, and running
// this test alone samples 0 and passes.
//
// WAITING HERE IS SOUND RATHER THAN A LONGER SLEEP, and the difference is that
// the drain is already under way with a terminating condition. Every producer
// registered t.Cleanup(segmentMgr.Close), and Go runs a test's cleanups before
// the next test begins, so by this line every residual merger has ALREADY been
// signaled to stop and no new one can start: this test takes no t.Parallel, and
// parallel siblings resume only after the sequential pass. There is nothing left
// to race — only a bounded exit to finish.
//
// A census that never settles is therefore a REAL LEAK, not slow scheduling, and
// this fails naming the count rather than proceeding on a number it knows is
// moving.
func quiescedMergerBaseline(t *testing.T) int {
	t.Helper()
	const settleReads = 3 // consecutive equal reads that call it quiesced
	deadline := time.Now().Add(5 * time.Second)

	prev, stable := mergerGoroutines(), 1
	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
		got := mergerGoroutines()
		if got == prev {
			if stable++; stable >= settleReads {
				return got
			}
			continue
		}
		prev, stable = got, 1
	}
	t.Fatalf("the merger census never settled within 5s (last read %d): every prior test closed its "+
		"manager before this one began, so a count still moving here is a merger that is not exiting at all", prev)
	return 0
}

// waitForMergers polls until the merger count reaches want, returning the last
// count seen. Close closes a channel and the goroutines observe it on their own
// schedule, so a single immediate read would race the scheduler, not the code.
func waitForMergers(want int, within time.Duration) int {
	deadline := time.Now().Add(within)
	got := mergerGoroutines()
	for got != want && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
		got = mergerGoroutines()
	}
	return got
}
