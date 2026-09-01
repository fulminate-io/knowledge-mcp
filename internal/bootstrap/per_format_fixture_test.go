// SPDX-License-Identifier: Apache-2.0

package bootstrap

// per_format_fixture_test.go holds the two per-format arm fixtures.
//
// RELOCATED from the deleted client_segment_arm_verdict_test.go. That file's TESTS
// were about ArmVerdict — the decision the per-format probe used to make, and which
// moved to the caller holding the embedded denominator — so the file went with the
// verdict. These two fixtures did not: they build documents and an evicted pool using
// only EXPORTED segmentdist surface, with no dependence on the rail, and
// per_format_degeneracy_test.go consumes both. Relocating them keeps that coverage
// compiling without preserving one line of the verdict machinery.

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
// through the budget pass. It uses only exported segmentdist surface — the eviction
// is driven by EnforceResidencyBudget against a one-byte budget rather than by
// reaching into the package.
func evictedArmFixture(t *testing.T) (*client, kgtypes.GraphType, string, []searchengine.Document) {
	t.Helper()
	ctx := context.Background()

	gt, name := kgtypes.GraphKnowledge, propagationGraphName
	// The auth state exists because NewRouter needs one. It no longer selects a
	// segment source: there is one source and it is local.
	authState := auth.NewAuthState(newFakeAuthStore(), time.Minute)
	local := graphclient.NewGraphClientForURL("http://local.invalid")
	t.Cleanup(local.Close)
	router := graphclient.NewRouter(local, "http://local.invalid", staticTokenSource{tok: "tok"}, authState)

	c := &client{local: local, router: router, authState: authState}
	// Built directly rather than through ensureSegmentManager: that path deliberately
	// arms the budget at a literal 0 until the consumer partition and the decider
	// fences are all in place, and this fixture needs an eviction to exist. A 1-byte
	// budget puts any resident pool over it.
	c.segmentMgr = segmentdist.NewManager(segmentCacheDirFor(t.TempDir()), 0,
		segmentdist.WithResidencyBudget(1))
	// Only Manager.Close stops the per-engine merger goroutines this spawns.
	t.Cleanup(c.segmentMgr.Close)
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
