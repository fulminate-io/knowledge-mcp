// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// TestFakeLogGraphCaller_Dispatch is the behavioral contract the Phase 2
// migrations depend on. Each arm asserts SELECTION FIDELITY (the correct
// subset), not merely the decoded shape — a fake that returned the whole corpus
// regardless of selection would pass a shape-only check but silently break
// every drill-down / hydrate / edges-union test downstream.
func TestFakeLogGraphCaller_Dispatch(t *testing.T) {
	const queryID = "q-dispatch"
	fake := newFakeLogGraphCaller()
	nodes, edges := buildLogCorpus(t, queryID)
	require.NotEmpty(t, nodes, "corpus must carry nodes")
	require.NotEmpty(t, edges, "corpus must carry edges")
	fake.seedLogGraph(queryID, nodes, edges)
	ctx := context.Background()

	// Partition the seeded corpus by type for expectation building.
	byType := map[kgtypes.NodeType][]*knowledgev1.Node{}
	for _, n := range nodes {
		byType[kgtypes.NodeType(n.Type)] = append(byType[kgtypes.NodeType(n.Type)], n)
	}
	require.NotEmpty(t, byType[kgtypes.NodeLogTemplate], "expected template nodes")
	require.NotEmpty(t, byType[kgtypes.NodeLogStream], "expected stream nodes")
	require.NotEmpty(t, byType[kgtypes.NodeLogChunk], "expected chunk nodes")

	t.Run("ids_arm_returns_exactly_requested", func(t *testing.T) {
		// Pick one template id out of a corpus of many; the fake must return
		// EXACTLY that id — not the whole corpus, not any sibling.
		wantID := byType[kgtypes.NodeLogTemplate][0].Id
		resp, err := fake.Execute(ctx, &knowledgev1.ExecuteRequest{
			Target: logsSelector(),
			Plan:   &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{Ids: []string{wantID}}},
		})
		require.NoError(t, err)
		got, err := engine.DecodeNodes(resp)
		require.NoError(t, err)
		require.Len(t, got, 1, "ids:[one] must return exactly one node")
		assert.Equal(t, wantID, got[0].Id)
	})

	t.Run("nodetype_browse_returns_only_that_type", func(t *testing.T) {
		resp, err := fake.Execute(ctx, &knowledgev1.ExecuteRequest{
			Target: logsSelector(),
			Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
				Selection: &knowledgev1.Selection{NodeType: string(kgtypes.NodeLogTemplate)},
			}},
		})
		require.NoError(t, err)
		got, err := engine.DecodeNodes(resp)
		require.NoError(t, err)
		require.Len(t, got, len(byType[kgtypes.NodeLogTemplate]),
			"browse must return every template and ONLY templates")
		for _, n := range got {
			assert.Equal(t, string(kgtypes.NodeLogTemplate), n.Type,
				"no NodeLogStream / NodeLogChunk may leak through a template browse")
		}
	})

	t.Run("content_b64_round_trips_chunk_bytes", func(t *testing.T) {
		// Find a chunk with non-empty Content (raw zstd bytes).
		var rawContent string
		var chunkID string
		for _, c := range byType[kgtypes.NodeLogChunk] {
			if c.Content != "" {
				rawContent = c.Content
				chunkID = c.Id
				break
			}
		}
		require.NotEmpty(t, chunkID, "expected a chunk with non-empty Content")

		req := &knowledgev1.ExecuteRequest{
			Target: logsSelector(),
			Plan:   &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{Ids: []string{chunkID}, ContentB64: true}},
		}
		resp, err := fake.Execute(ctx, req)
		require.NoError(t, err)
		got, err := engine.DecodeNodesContentB64(resp)
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, rawContent, got[0].Content,
			"content_b64 must round-trip to byte-identical raw chunk bytes")

		// And the plain DecodeNodes path mangles it (the base64 string is NOT
		// the raw bytes) — proving the carrier is load-bearing. Re-issue the
		// Execute: the typed carrier shares node pointers, and
		// DecodeNodesContentB64 decodes Content IN PLACE — a fresh response
		// isolates the plain-decode read from the prior in-place decode.
		resp2, err := fake.Execute(ctx, req)
		require.NoError(t, err)
		plain, err := engine.DecodeNodes(resp2)
		require.NoError(t, err)
		require.Len(t, plain, 1)
		assert.NotEqual(t, rawContent, plain[0].Content,
			"plain DecodeNodes must leave the base64 string un-decoded")
	})

	t.Run("edges_union_returns_only_source_edges_of_type", func(t *testing.T) {
		// CONTAINS edges originate FROM templates (template → chunk, see
		// AssembleGraphBatch). Pick one template that has an outgoing CONTAINS
		// edge and union over just that id with edge_type=CONTAINS.
		var srcID string
		wantTargets := map[string]bool{}
		for i := range edges {
			if kgtypes.EdgeType(edges[i].Type) == kgtypes.EdgeContains {
				srcID = edges[i].FromId
				break
			}
		}
		require.NotEmpty(t, srcID, "expected a CONTAINS edge in the corpus")
		// Compute the expected target set: srcID's OUTGOING CONTAINS edges only.
		for i := range edges {
			if edges[i].FromId == srcID && kgtypes.EdgeType(edges[i].Type) == kgtypes.EdgeContains {
				wantTargets[edges[i].ToId] = true
			}
		}

		resp, err := fake.Execute(ctx, &knowledgev1.ExecuteRequest{
			Target: logsSelector(),
			Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
				ReturnMode: knowledgev1.ReturnMode_RETURN_MODE_EDGES,
				Selection: &knowledgev1.Selection{
					Ids:       []string{srcID},
					EdgeTypes: []string{string(kgtypes.EdgeContains)},
				},
			}},
		})
		require.NoError(t, err)
		got, err := engine.DecodeEdges(resp)
		require.NoError(t, err)
		require.NotEmpty(t, got, "union over a CONTAINS source must return its edges")
		for i := range got {
			e := &got[i]
			assert.Equal(t, srcID, e.FromId,
				"edges from sibling nodes must be excluded")
			assert.Equal(t, string(kgtypes.EdgeContains), e.Type,
				"edges of other types must be excluded")
			assert.True(t, wantTargets[e.ToId], "unexpected edge target %q", e.ToId)
		}
		assert.Len(t, got, len(wantTargets), "must return every matching edge, deduped")
	})

	t.Run("graph_names_returns_seeded_names", func(t *testing.T) {
		// Seed a second graph so the result is a genuine set, then assert the
		// names come back exactly.
		fake.seedLogGraph("q-other", nil, nil)
		t.Cleanup(func() { delete(fake.graphs, "q-other") })
		resp := fake.execGraphNames(&knowledgev1.GraphSelector{Graph: "logs"})
		infos, err := engine.DecodeGraphNames(resp)
		require.NoError(t, err)
		names := make([]string, 0, len(infos))
		for _, gi := range infos {
			names = append(names, gi.Name)
		}
		assert.Contains(t, names, queryID)
		assert.Contains(t, names, "q-other")
	})

	t.Run("drop_graph_removes_named_graph", func(t *testing.T) {
		fake.seedLogGraph("q-droppable", nil, nil)
		// Confirm it lists first.
		before, err := engine.DecodeGraphNames(fake.execGraphNames(&knowledgev1.GraphSelector{Graph: "logs"}))
		require.NoError(t, err)
		beforeNames := make([]string, 0, len(before))
		for _, gi := range before {
			beforeNames = append(beforeNames, gi.Name)
		}
		require.Contains(t, beforeNames, "q-droppable")

		resp, err := fake.Execute(ctx, &knowledgev1.ExecuteRequest{
			Target: &knowledgev1.GraphSelector{Graph: "logs", Name: "q-droppable"},
			Plan: &knowledgev1.ExecuteRequest_Mutation{Mutation: &knowledgev1.MutationPlan{
				Kind: knowledgev1.MutationPlan_MUTATION_KIND_DROP_GRAPH,
			}},
		})
		require.NoError(t, err)
		assert.Equal(t, int64(1), resp.GetAffectedCount())

		after, err := engine.DecodeGraphNames(fake.execGraphNames(&knowledgev1.GraphSelector{Graph: "logs"}))
		require.NoError(t, err)
		for _, gi := range after {
			assert.NotEqual(t, "q-droppable", gi.Name,
				"a dropped graph must no longer be listed")
		}
	})
}
