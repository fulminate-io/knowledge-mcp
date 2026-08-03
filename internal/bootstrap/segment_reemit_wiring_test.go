// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// publishCount reads the fake backend's manifest-publish counter under its lock.
func (b *fakeSegBackend) publishCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.publishCalls
}

// TestReconcilePassDrainsBacklogForHealthyGraph is the WIRING gate for the
// partitioned re-emit. The drain has exactly one production caller — the reconcile
// pass — so if that call is missing or misplaced, writes are made searchable in
// this process and never become durable anywhere else.
//
// THE FIXTURE IS DELIBERATELY HEALTHY, and that is the whole design of the test.
// The per-graph loop skips healthy graphs with a `continue`, so a drain placed
// after the degeneracy probe never runs for exactly the graphs that hold backlogs:
// a graph with unpublished writes is not degenerate, it is a perfectly healthy
// graph that simply has work queued. A degenerate fixture would reach a misplaced
// call and let this gate pass against broken wiring.
//
// It asserts an OBSERVABLE publish rather than the presence of a call, so a comment
// naming the drain cannot satisfy it.
func TestReconcilePassDrainsBacklogForHealthyGraph(t *testing.T) {
	ctx := opCtx()
	const (
		repo     = "wiringRepo"
		corpusN  = 128 // clears the resident backstop floor
		embedded = 100 // resident 128 >= 0.5*100, so the graph is HEALTHY
		batchN   = 20
	)

	c, _, backend := buildReconcileClientWithSeg(t, embedded, repo)

	// Seed a published corpus, then confirm the graph really is healthy — if this
	// precondition ever broke, the test would be gating the wrong branch.
	require.NoError(t, c.segmentMgr.AddAndMarkDirty(ctx, kgtypes.GraphCode, repo, fastloadVecDocs(repo, corpusN)))
	require.NoError(t, c.segmentMgr.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, repo))

	degenerate, err := c.segmentMgr.ReconcileResidentDegenerate(ctx, kgtypes.GraphCode, repo)
	require.NoError(t, err)
	require.False(t, degenerate,
		"PRECONDITION: the fixture graph must be HEALTHY, so the pass reaches the healthy-graph continue")

	// Queue a backlog WITHOUT draining it — the state a drain leaves between ticks.
	require.NoError(t, c.segmentMgr.AddAndMarkDirty(ctx, kgtypes.GraphCode, repo, fastloadVecDocs("wiringBatch", batchN)))

	before := backend.publishCount()
	c.reconcileSegmentCoverage(ctx)
	after := backend.publishCount()

	require.Greater(t, after, before,
		"one reconcile pass must drain the backlog of a HEALTHY graph and publish the result; "+
			"no publish means the drain is either not called from the pass at all, or called after "+
			"the healthy-graph continue, where it never runs for the graphs that have work")
}
