// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// wireID builds a wire identity. The fake provider is used throughout: it needs
// no credential and no network, so a failure below is about identity resolution
// rather than about a missing key.
func wireID(model string, dim int32) *knowledgev1.EmbedIdentity {
	return &knowledgev1.EmbedIdentity{
		Provider: string(config.EmbedProviderFake), Model: model, Dimension: dim, Dtype: "ubinary",
	}
}

// catalogOf builds a fetcher over a fixed per-type catalog and counts its reads,
// so "one read per TYPE, not per graph" is observable.
func catalogOf(byType map[string][]*knowledgev1.GraphInfo, reads *atomic.Int64) graphCatalogFetcher {
	return func(_ context.Context, graphType string) ([]*knowledgev1.GraphInfo, error) {
		if reads != nil {
			reads.Add(1)
		}
		return byType[graphType], nil
	}
}

// TestQueryEmbedderResolvesFromTargetGraph covers the three duties of query-side
// identity resolution, one subtest each.
func TestQueryEmbedderResolvesFromTargetGraph(t *testing.T) {
	ctx := context.Background()

	t.Run("single graph uses the graphs identity not config", func(t *testing.T) {
		// THE CONFIG SAYS ONE THING AND THE GRAPH SAYS ANOTHER, which is the only
		// fixture that can tell the two sources apart. A resolver reading config
		// would return the 256-dim default here; the graph records 1024.
		cfg, err := config.Parse([]byte("[embedder]\nprovider = \"fake\"\nmodel = \"config-model\"\ndimension = 256\n"))
		require.NoError(t, err)
		require.Equal(t, 256, mustProfile(t, cfg, "default").Dimension, "control: config really does say 256")

		var reads atomic.Int64
		fetch := catalogOf(map[string][]*knowledgev1.GraphInfo{
			"knowledge": {{Name: "default", EmbedIdentity: wireID("graph-model", 1024)}},
		}, &reads)

		got, err := resolveQueryEmbedders(ctx, fetch,
			[]graphTarget{{GraphType: "knowledge", Name: "default"}})
		require.NoError(t, err)
		require.Equal(t, 1, got.DistinctIdentities(), "one graph, one identity, one embedder")

		e, ok := got.EmbedderFor(graphTarget{GraphType: "knowledge", Name: "default"})
		require.True(t, ok, "the target resolved to an embedder")
		require.NotNil(t, e)

		// The embedder is the GRAPH'S, proven by the width of what it produces:
		// 1024 bits is 128 bytes, and the config's 256 would be 32.
		vec, err := e.EmbedBinary(ctx, "some query")
		require.NoError(t, err)
		assert.Len(t, vec, 1024/8,
			"the query embedder must be the GRAPH's (1024-dim), not the config's (256-dim)")

		// A graph with NO recorded identity resolves to no embedder — not an
		// error. It holds no vectors, so there is nothing to compare against.
		bare := catalogOf(map[string][]*knowledgev1.GraphInfo{
			"knowledge": {{Name: "fresh"}},
		}, nil)
		none, err := resolveQueryEmbedders(ctx, bare,
			[]graphTarget{{GraphType: "knowledge", Name: "fresh"}})
		require.NoError(t, err, "a graph that was never embedded is not an error")
		assert.Equal(t, 0, none.DistinctIdentities())
		_, ok = none.EmbedderFor(graphTarget{GraphType: "knowledge", Name: "fresh"})
		assert.False(t, ok)
	})

	t.Run("three graphs two identities embeds twice", func(t *testing.T) {
		// THE FIXTURE IS THE ASSERTION HERE. Three graphs sharing ONE identity
		// could not tell "once per identity" from "once globally"; three graphs
		// with three identities could not tell it from "once per graph". Two
		// identities across three graphs separates all three readings.
		var reads atomic.Int64
		fetch := catalogOf(map[string][]*knowledgev1.GraphInfo{
			"code": {
				{Name: "repoA", EmbedIdentity: wireID("m1", 256)},
				{Name: "repoB", EmbedIdentity: wireID("m1", 256)},
				{Name: "repoC", EmbedIdentity: wireID("m2", 256)},
			},
		}, &reads)

		targets := []graphTarget{
			{GraphType: "code", Name: "repoA"},
			{GraphType: "code", Name: "repoB"},
			{GraphType: "code", Name: "repoC"},
		}
		got, err := resolveQueryEmbedders(ctx, fetch, targets)
		require.NoError(t, err)

		assert.Equal(t, 2, got.DistinctIdentities(),
			"three graphs over TWO identities must build TWO embedders — not one (which would compare "+
				"repoC against a vector from another model) and not three (which pays twice for m1)")

		// The two graphs that SHARE an identity share the embedder instance; the
		// third does not. Without this the count above is satisfiable by a
		// resolver that built two arbitrary embedders.
		eA, okA := got.EmbedderFor(targets[0])
		eB, okB := got.EmbedderFor(targets[1])
		eC, okC := got.EmbedderFor(targets[2])
		require.True(t, okA && okB && okC)
		assert.Same(t, eA, eB, "graphs on the same identity share one embedder")
		assert.NotSame(t, eA, eC, "a graph on a different identity gets its own")

		assert.Equal(t, int64(1), reads.Load(),
			"the catalog is read once per graph TYPE, not once per graph")
	})

	t.Run("unconstructible identity errors rather than degrading", func(t *testing.T) {
		// An API provider with no credential and no base_url cannot be built.
		// BuildEmbedder (the WRITE side) answers nil for this and degrades to
		// BM25 by design; the QUERY side must not, because the graph HAS vectors
		// and a keyword answer reported as success is a worse answer the caller
		// cannot detect.
		_, err := config.Parse([]byte("[default]\nprovider = \"anthropic\"\nmodel = \"m\"\n"))
		require.NoError(t, err)
		t.Setenv("VOYAGE_API_KEY", "")

		fetch := catalogOf(map[string][]*knowledgev1.GraphInfo{
			"code": {{Name: "repoX", EmbedIdentity: &knowledgev1.EmbedIdentity{
				Provider: "voyage", Model: "voyage-code-4", Dimension: 256, Dtype: "ubinary",
			}}},
		}, nil)

		_, err = resolveQueryEmbedders(ctx, fetch,
			[]graphTarget{{GraphType: "code", Name: "repoX"}})
		require.Error(t, err, "an identity this client cannot construct must ERROR, never degrade silently")
		assert.Contains(t, err.Error(), "voyage", "the error names the provider it cannot construct")
		assert.Contains(t, strings.ToLower(err.Error()), "credential",
			"and says what is missing, so an operator can supply it")

		// KNOWN-POSITIVE, same run: an identity that CAN be constructed still
		// resolves, so the error above is about this identity rather than about
		// resolution being broken for everything.
		ok := catalogOf(map[string][]*knowledgev1.GraphInfo{
			"code": {{Name: "repoY", EmbedIdentity: wireID("m", 256)}},
		}, nil)
		got, err := resolveQueryEmbedders(ctx, ok,
			[]graphTarget{{GraphType: "code", Name: "repoY"}})
		require.NoError(t, err)
		assert.Equal(t, 1, got.DistinctIdentities())
	})
}

// mustProfile resolves a named profile or fails the test.
func mustProfile(t *testing.T, cfg *config.Config, name string) config.EmbedProfile {
	t.Helper()
	p, err := cfg.EmbedProfileByName(name)
	require.NoError(t, err)
	return p
}
