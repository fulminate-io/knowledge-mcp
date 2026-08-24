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

	baseline := mergerGoroutines()

	ctx := opCtx()
	c, _, _ := buildReconcileClientWithSeg(t, 100, repo)
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
