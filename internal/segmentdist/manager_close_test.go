// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// mergerFrame is the RUNNING frame of a merger goroutine, as runtime.Stack
// renders it. The trailing ".func1()" is load-bearing: each merger contributes
// TWO lines naming startMerger — the running closure and the "created by
// ...startMerger" attribution beneath it — so matching the bare method name
// double-counts every goroutine.
const mergerFrame = "SegmentedIndex[...]).startMerger.func1()"

// mergerGoroutines counts the live merger goroutines by name rather than by
// counting goroutines at large.
//
// runtime.NumGoroutine would answer a different question — how many goroutines
// exist — and any unrelated background goroutine moves that number, so a test
// built on it reports someone else's activity as this code's leak. Matching the
// merger's own frame counts only the thing under test.
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

// waitForMergers polls until the merger count drops to want, and returns the
// last count it saw. Polling rather than sampling once: Close closes a channel
// and the goroutines observe it on their own schedule, so an immediate read
// races the scheduler rather than the code.
func waitForMergers(want int, within time.Duration) int {
	deadline := time.Now().Add(within)
	got := mergerGoroutines()
	for got != want && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
		got = mergerGoroutines()
	}
	return got
}

// settledMergerCount polls until the census reads the same value twice across a
// quiet interval, and returns that value.
//
// THE BASELINE HAS TO BE SETTLED, NOT SAMPLED, and this helper exists because
// sampling it once was a real intermittent failure rather than a theoretical one.
// An EARLIER test's Manager has already had Close called by its cleanup, but its
// merger goroutines observe the closed channel on their own schedule — the exact
// asynchrony waitForMergers exists for. A single read taken inside that drain
// window counts goroutines that are already leaving; they are gone by the time
// the rise is asserted, so the rise reads SHORT by however many were still
// draining. Observed in CI-shaped whole-package runs as `expected: 10, actual:
// 6`: a baseline of four draining mergers, all six new ones correctly built.
//
// Staying sequential (no t.Parallel) does not close this: it keeps a CONCURRENT
// sibling's engines out of the count, but says nothing about a PRECEDING one's
// goroutines still unwinding.
func settledMergerCount(within time.Duration) int {
	deadline := time.Now().Add(within)
	prev := mergerGoroutines()
	for time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		got := mergerGoroutines()
		if got == prev {
			return got
		}
		prev = got
	}
	return prev
}

// TestManagerCloseStopsEveryEngineMerger pins the OTHER half of the two lazy
// constructors: each of them starts a merger goroutine that nothing else ever
// stops, and Manager.Close is what stops them.
//
// It deliberately does NOT use closeOnCleanup — this test owns the Close it is
// asserting about, and registering a second one would let a no-op Close pass by
// having the cleanup do the work at the end of the test instead.
//
// The rise before the fall is the known-positive. Asserting only that the count
// returns to its baseline would be satisfied by a Manager that never built an
// engine at all, so the test first requires the count to CLIMB by two per graph
// (one HNSW engine, one BM25) against a fixture-derived expectation.
func TestManagerCloseStopsEveryEngineMerger(t *testing.T) {
	// No t.Parallel: this reads a process-wide goroutine census, and Go resumes
	// parallel tests only after the sequential pass, so staying sequential is what
	// keeps a sibling's engines out of the count.
	const graphs = 3
	const enginesPerGraph = 2 // one HNSW, one BM25

	ctx := context.Background()

	// SETTLED, not sampled — see settledMergerCount. A preceding test's mergers may
	// still be unwinding, and counting them into the baseline makes the rise below
	// read short by exactly that many.
	baseline := settledMergerCount(5 * time.Second)

	mgr := NewManager(t.TempDir(), 0)
	for i := range graphs {
		name := "closegraph" + string(rune('a'+i))
		require.NoError(t, mgr.AddAndMarkDirty(ctx, kgtypes.GraphCode, name, hnswVecDocs(4)))
		require.NoError(t, mgr.AddAndMarkDirtyFields(ctx, kgtypes.GraphCode, name, hnswVecDocs(4)))
	}

	// KNOWN-POSITIVE: the engines really are running before anything is closed.
	require.Equal(t, baseline+graphs*enginesPerGraph, mergerGoroutines(),
		"each graph must have constructed one HNSW and one BM25 engine, each with a live merger; "+
			"without this the fall below could be satisfied by a Manager that built nothing")

	mgr.Close()

	require.Equal(t, baseline, waitForMergers(baseline, 5*time.Second),
		"Manager.Close must stop every engine's merger goroutine; a surviving one is a ticker that "+
			"runs for the life of the process, because nothing removes an engine from the memo maps")
}
