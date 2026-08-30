// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestReconcileLoopWakesOnNudge proves the periodic reconcile loop runs a pass when
// the segment manager reports that a publish coverage gate went unsatisfiable,
// instead of waiting out its own cadence.
//
// FALSIFIABILITY: the ticker interval is an HOUR, so the ticker case cannot fire
// within the test's lifetime and the nudge is the ONLY thing that can explain a
// pass. Against a loop with no nudge case the observable never moves and the
// Eventually times out.
//
// THE NUDGE IS DRIVEN THROUGH A REAL RECORDER, and which recorder that is has
// changed. It used to be the publish coverage gate: a graph with a shipped corpus on
// a server and a sub-floor resident force-sealed a tail, every publish attempt was
// coverage-skipped, and the skip streak crossing its bound recorded the wake. There
// is no publish, no coverage gate and no skip streak, so that recorder cannot be
// reached from any input. Two recorders survive — a backlog crossing the 64 MiB
// re-emit byte cap, and a SEARCH on the graph — and the search is the one a test can
// drive honestly: the cap would need tens of megabytes of fixture documents to trip,
// which measures the fixture's size rather than the loop's wiring.
//
// The recording is made BEFORE the loop starts on purpose — the wake channel is
// buffered, so the queued wake is delivered as soon as the loop reaches its select,
// which removes any ordering race from the assertion.
func TestReconcileLoopWakesOnNudge(t *testing.T) {
	t.Cleanup(resetBM25HealProgress)
	const repo = "nudgeLoopRepo"
	// embedded=300 against a LOST pool arms the woken pass: nothing is resident, so the
	// pass reaches RebuildSegments and pages PipelineScan — the observable that a pass
	// ran. A PARTIAL corpus would arm nothing here, because the ratio band is retired
	// for the HNSW arm and this periodic loop is not the quiescence edge.
	c, eng, _ := buildReconcileClientWithDir(t, 300, repo)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	eng.scanItems[repo] = makeReconcileScanPage(repo, 10)

	// Record the wake through the search recorder — one search on this graph asks the
	// reconcile loop to pull its delta now rather than at its next tick.
	probe := fastloadVecDocs(repo, 1)[0]
	_, err := c.segmentMgr.Search(ctx, kgtypes.GraphCode, repo, "token", probe.Vector, 5)
	require.NoError(t, err)
	// Observed WITHOUT draining: TakeReconcileNudges empties the set, so calling it
	// here would consume the very wake the loop is supposed to act on. The buffered
	// wake channel's length is the non-destructive read.
	require.NotEmpty(t, c.segmentMgr.ReconcileNudge(),
		"PRE: the search must have QUEUED a wake — otherwise the loop below has nothing "+
			"to wake on and the Eventually would be timing out on a fixture that never signaled")
	require.Equal(t, 0, eng.scanCallCount(repo),
		"PRE: recording a nudge alone runs no reconcile pass")

	done := make(chan struct{})
	go func() {
		c.runSegmentReconcileLoop(ctx, time.Hour)
		close(done)
	}()

	require.Eventually(t, func() bool {
		return eng.scanCallCount(repo) >= 1
	}, 10*time.Second, 10*time.Millisecond,
		"the loop ran a reconcile pass on the nudge — an hour-long ticker cannot explain it")

	// The no-leak contract is unchanged: the loop still exits promptly on cancel.
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runSegmentReconcileLoop did not return after ctx cancel — goroutine leak")
	}
}

// TestReconcileLoopNilManagerTicksWithoutNudge is the headless / --no-llm-pipeline
// side: a client with no segment manager cannot be nudged (there is no publisher),
// and reading the nudge channel off a nil manager would dereference a nil receiver.
// The loop must fall back to its plain ticker shape and still exit on cancel.
func TestReconcileLoopNilManagerTicksWithoutNudge(t *testing.T) {
	c := &client{} // segmentMgr nil — headless/degraded.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		c.runSegmentReconcileLoop(ctx, 5*time.Millisecond)
		close(done)
	}()

	// Let several ticks fire: each runs the reconcile body, which no-ops on the nil
	// manager. A nil-channel read in the loop would panic here instead.
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runSegmentReconcileLoop did not return after ctx cancel — goroutine leak")
	}
}
