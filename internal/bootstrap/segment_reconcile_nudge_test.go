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
// The suppression is driven through the REAL publish path rather than injected: a
// graph with a shipped corpus on the server and a sub-floor resident force-seals a
// tail, so every publish attempt is coverage-skipped and the streak crosses its
// suppression bound. The recording is made BEFORE the loop starts on purpose — the
// wake channel is buffered, so the queued wake is delivered as soon as the loop
// reaches its select, which removes any ordering race from the assertion.
func TestReconcileLoopWakesOnNudge(t *testing.T) {
	t.Cleanup(resetBM25HealProgress)
	const repo = "nudgeLoopRepo"
	// embedded=300 arms the shipped-completeness gate for the woken pass: the shipped
	// corpus (128) is genuinely incomplete against it, so the pass reaches
	// RebuildSegments and pages PipelineScan — the observable that a pass ran.
	c, eng, backend := buildReconcileClientWithSeg(t, 300, repo)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	shipHNSW(t, backend, repo, 64, 64)
	eng.scanItems[repo] = makeReconcileScanPage(repo, 10)

	// Force-seal a sub-floor tail against that shipped corpus, then re-fire the
	// publish until the coverage-skip streak crosses its suppression bound. Each
	// Flush re-reads the SAME sub-ratio resident and skips again; the skip that
	// passes the bound records the graph for an earlier reconcile look.
	require.NoError(t, c.segmentMgr.AddAndMarkDirty(ctx, kgtypes.GraphCode, repo, fastloadVecDocs(repo, 10)))
	for range 4 {
		require.NoError(t, c.segmentMgr.Flush(ctx, kgtypes.GraphCode, repo))
	}
	require.Equal(t, 0, eng.scanCallCount(repo),
		"PRE: the publish path alone runs no reconcile pass")

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
