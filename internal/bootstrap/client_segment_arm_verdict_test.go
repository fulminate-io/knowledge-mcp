// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
	"github.com/fulminate-io/knowledge-mcp/internal/segmentdist"
)

// armVerdictFixtureDocs builds documents carrying BOTH a 32-byte vector (HNSW) and
// text fields (BM25), so one corpus populates both format arms of a graph.
func armVerdictFixtureDocs(n int) []searchengine.Document {
	docs := make([]searchengine.Document, n)
	for i := range docs {
		vec := make([]byte, 32)
		for b := range vec {
			vec[b] = byte((i*31 + b*7) % 251)
		}
		docs[i] = searchengine.Document{
			ID:     fmt.Sprintf("armverdict-%03d", i),
			Vector: vec,
			Fields: map[string]string{
				searchengine.FieldSymbolName: fmt.Sprintf("uniqueterm%d", i),
				searchengine.FieldSummary:    fmt.Sprintf("shared corpus body token%d common", i),
			},
		}
	}
	return docs
}

// evictedArmFixture builds a client over a real segment manager, populates BOTH
// format arms of one graph, makes them resident with a search, and then evicts them
// through the budget pass. It is the starting state every subtest below needs, and
// it uses only exported segmentdist surface — the eviction is driven by
// EnforceResidencyBudget against a one-byte budget rather than by reaching into the
// package.
func evictedArmFixture(t *testing.T) (*client, kgtypes.GraphType, string, []searchengine.Document) {
	t.Helper()
	ctx := context.Background()

	gt, name := kgtypes.GraphKnowledge, propagationGraphName
	authState := auth.NewAuthState(newFakeAuthStore(), time.Minute) // logged out → OSS local source
	local := graphclient.NewGraphClientForURL("http://local.invalid")
	router := graphclient.NewRouter(local, "http://local.invalid", staticTokenSource{tok: "tok"}, authState)

	c := &client{local: local, router: router, authState: authState}
	// Built directly rather than through ensureSegmentManager: that path deliberately
	// arms the budget at a literal 0 until the consumer partition and the decider
	// fences are all in place, and this fixture needs an eviction to exist. A 1-byte
	// budget puts any resident pool over it.
	c.segmentMgr = segmentdist.NewManager(router, segmentCacheDirFor(t.TempDir()), 0,
		segmentdist.WithResidencyBudget(1))
	c.markPipelineReady()

	docs := armVerdictFixtureDocs(6)
	require.NoError(t, c.segmentMgr.AddAndMarkDirty(ctx, gt, name, docs))
	require.NoError(t, c.segmentMgr.AddAndMarkDirtyFields(ctx, gt, name, docs))
	require.NoError(t, c.segmentMgr.ReEmitDirtyBuckets(ctx, gt, name))

	// The load path is what records resident bytes, so one search is what gives the
	// budget pass something to evict.
	_, err := c.segmentMgr.Search(ctx, gt, name, "common", docs[0].Vector, 5)
	require.NoError(t, err)

	c.segmentMgr.EnforceResidencyBudget()
	require.True(t, c.segmentMgr.PoolEvicted(gt, name),
		"PRECONDITION: the budget pass must have evicted this graph's pools")
	return c, gt, name, docs
}

// TestArmVerdictConsumersBranchOnEvicted is the SHARED-PRODUCER partition's gate.
// An evicted arm reports {Evicted:true, Degenerate:false, Err:nil}, which every
// consumer written before this ticket reads as MEASURED-AND-HEALTHY. One subtest per
// consumer, so a single fixed consumer cannot green the rest.
//
// EVERY SUBTEST CARRIES A KNOWN-POSITIVE CONTROL: the same consumer, the same graph,
// materialized — otherwise a consumer hard-wired to decline would pass.
func TestArmVerdictConsumersBranchOnEvicted(t *testing.T) {
	ctx := context.Background()

	t.Run("propagation_gate_declines", func(t *testing.T) {
		c, gt, name, docs := evictedArmFixture(t)
		gate := coverageGateAdapter{c: c}

		ok, reason, err := gate.HNSWCoverageTrustworthy(ctx)
		require.NoError(t, err)
		require.False(t, ok, "an evicted HNSW arm is not a trustworthy arm — it was never measured")
		require.Contains(t, reason, "evicted", "the decline must carry its reason, not a bare false")
		require.True(t, c.segmentMgr.PoolEvicted(gt, name), "the gate must not have resurrected the pool")

		// CONTROL: a consumer search materializes the pool, and the SAME gate now
		// trusts it. Without this, a gate hard-wired to false would pass.
		_, err = c.segmentMgr.Search(ctx, gt, name, "common", docs[0].Vector, 5)
		require.NoError(t, err)
		ok, reason, err = gate.HNSWCoverageTrustworthy(ctx)
		require.NoError(t, err)
		require.True(t, ok, "a materialized, non-degenerate arm is trustworthy (reason: %s)", reason)
	})

	t.Run("hnsw_arm_probe_declines", func(t *testing.T) {
		c, gt, name, docs := evictedArmFixture(t)

		_, degenerate, err := c.hnswArmProbe(ctx, gt, name)
		require.Error(t, err, "an evicted arm takes the probe's documented could-not-be-read path")
		require.Contains(t, err.Error(), "evicted")
		require.False(t, degenerate, "an unread arm must never report degenerate")
		require.True(t, c.segmentMgr.PoolEvicted(gt, name))

		// CONTROL: materialized, the same probe answers cleanly.
		_, err = c.segmentMgr.Search(ctx, gt, name, "common", docs[0].Vector, 5)
		require.NoError(t, err)
		_, degenerate, err = c.hnswArmProbe(ctx, gt, name)
		require.NoError(t, err, "a materialized arm is readable")
		require.False(t, degenerate)
	})

	t.Run("bm25_gate_does_not_clear_the_bound", func(t *testing.T) {
		// THE SHARPEST SUBTEST. Both the decline and the pre-fix clear-and-decline
		// return false, so the only assertion that tells them apart is whether the
		// no-progress bound — the sole thing stopping an endless per-tick BM25 rebuild
		// — is STILL ARMED afterwards.
		c, gt, name, docs := evictedArmFixture(t)
		t.Cleanup(resetBM25HealProgress)

		key := bm25HealKey(gt, name)
		armBound := func() {
			bm25HealMu.Lock()
			bm25HealArmed[key] = 42
			bm25HealMu.Unlock()
		}
		boundArmed := func() bool {
			bm25HealMu.Lock()
			defer bm25HealMu.Unlock()
			_, armed := bm25HealArmed[key]
			return armed
		}

		armBound()
		needs, err := c.healNeedsRebuildBM25(ctx, gt, name, nil)
		require.NoError(t, err)
		require.False(t, needs, "an evicted arm must not drive a rebuild")
		require.True(t, boundArmed(), "an evicted arm must decline WITHOUT clearing the no-progress bound")

		// CONTROL: materialized and healthy, the same call reaches the Degenerate
		// branch and DOES clear the bound — so the assertion above is about eviction
		// rather than about a gate that never clears.
		_, err = c.segmentMgr.Search(ctx, gt, name, "common", docs[0].Vector, 5)
		require.NoError(t, err)
		armBound()
		needs, err = c.healNeedsRebuildBM25(ctx, gt, name, nil)
		require.NoError(t, err)
		require.False(t, needs)
		require.False(t, boundArmed(), "a healthy measured arm clears the bound on its way out")
	})

	t.Run("reconcile_graph_does_not_rebuild", func(t *testing.T) {
		// A1 in the partition: the reconcile cascade's rebuild decision is already
		// correct by arithmetic (an evicted arm contributes Degenerate false to the
		// ANY-arm OR, and the cascade returns on !degenerate). This asserts that
		// BEHAVIOUR rather than the doc sentence that now records it.
		c, gt, name, docs := evictedArmFixture(t)

		degenerate, err := c.segmentMgr.ReconcileResidentDegenerate(ctx, gt, name)
		require.NoError(t, err)
		require.False(t, degenerate, "an evicted graph must never be reported degenerate — that would drive a rebuild")
		require.True(t, c.segmentMgr.PoolEvicted(gt, name), "the probe must not have resurrected the pool")

		verdicts, err := c.segmentMgr.ReconcileResidentDegenerateByFormat(ctx, gt, name)
		require.NoError(t, err)
		require.Len(t, verdicts, 2)
		for _, v := range verdicts {
			require.True(t, v.Evicted, "%s arm reports the eviction the wrapper collapses", v.Format)
			require.Zero(t, v.ResidentAfterLoad)
		}

		// CONTROL: materialized, the SAME per-format probe measures a real resident
		// count and reports Evicted false — so the zeros above are the decline, not a
		// probe that never measures anything.
		_, err = c.segmentMgr.Search(ctx, gt, name, "common", docs[0].Vector, 5)
		require.NoError(t, err)
		verdicts, err = c.segmentMgr.ReconcileResidentDegenerateByFormat(ctx, gt, name)
		require.NoError(t, err)
		for _, v := range verdicts {
			require.False(t, v.Evicted, "%s arm is materialized", v.Format)
			require.Positive(t, v.ResidentAfterLoad, "%s arm measured a real resident count", v.Format)
		}
		require.Contains(t, []string{hnsw.New().Name(), bm25.New().Name()}, verdicts[0].Format)
	})
}
