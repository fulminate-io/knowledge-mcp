// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

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
		// THE BATCH MUST BE BIG ENOUGH TO SEAL, and that is a property of the
		// OBSERVABLE, not a magic number. This test used to read a publish-call count,
		// which rises whether or not the drain produced anything; it now reads DURABLE
		// .seg ids, and a sub-threshold batch leaves the documents in an unsealed tail
		// that no export carries — the drain runs, writes nothing, and the assertion
		// reads a no-op as a missing call. Measured: at 20 the drain ran (the write-diff
		// log fires) and the id count stayed at 1; at MinSegmentDocs it seals a second
		// segment. Verified to still discriminate by removing the drain call from the
		// per-graph pass — the assertion goes red.
		batchN = 1024 // searchengine.DefaultMinSegmentDocs — enough to seal a new segment
	)

	c, _, dir := buildReconcileClientWithDir(t, embedded, repo)

	// Seed a published corpus, then confirm the graph really is healthy — if this
	// precondition ever broke, the test would be gating the wrong branch.
	require.NoError(t, c.segmentMgr.AddAndMarkDirty(ctx, kgtypes.GraphCode, repo, fastloadVecDocs(repo, corpusN)))
	require.NoError(t, c.segmentMgr.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, repo))

	degenerate := armIsDegenerate(t, c.segmentMgr, kgtypes.GraphCode, repo, embedded)
	require.False(t, degenerate,
		"PRECONDITION: the fixture graph must be HEALTHY, so the pass reaches the healthy-graph continue")

	// Queue a backlog WITHOUT draining it — the state a drain leaves between ticks.
	require.NoError(t, c.segmentMgr.AddAndMarkDirty(ctx, kgtypes.GraphCode, repo, fastloadVecDocs("wiringBatch", batchN)))

	before := len(l2SegmentIDs(t, dir, repo, hnsw.New().Name()))
	c.reconcileSegmentCoverage(ctx)
	after := len(l2SegmentIDs(t, dir, repo, hnsw.New().Name()))

	require.Greater(t, after, before,
		"one reconcile pass must drain the backlog of a HEALTHY graph and publish the result; "+
			"no publish means the drain is either not called from the pass at all, or called after "+
			"the healthy-graph continue, where it never runs for the graphs that have work")
}
