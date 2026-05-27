// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// listGraphsResultFor builds a pipeline_list_graphs ToolResult covering the given
// (graph_type, graph_name) pairs — the resolveListGraphsResp wire shape
// listForeignGraphs decodes.
func listGraphsResultFor(t *testing.T, pairs ...[2]string) *kgtools.ToolResult {
	t.Helper()
	type entry struct {
		GraphType string `json:"graph_type"`
		GraphName string `json:"graph_name"`
	}
	body := struct {
		Graphs []entry `json:"graphs"`
	}{}
	for _, p := range pairs {
		body.Graphs = append(body.Graphs, entry{GraphType: p[0], GraphName: p[1]})
	}
	b, err := json.Marshal(body)
	require.NoError(t, err)
	return &kgtools.ToolResult{Content: []kgtools.ContentBlock{{Type: "text", Text: string(b)}}}
}

// slugLessPracticeProxyNode builds a slug-less practice proxy node
// (proxy:practice:<foreignID>, foreign_graph=practice, foreign_id set, NO
// language) — the legacy shape the migration retires.
func slugLessPracticeProxyNode(foreignID string) *knowledgev1.Node {
	n := &knowledgev1.Node{Id: "proxy:practice:" + foreignID, Type: string(kgtypes.NodeProxy), SymbolName: "Pat"}
	kgtypes.SetValue(n, "foreign_graph", "practice")
	kgtypes.SetValue(n, "foreign_id", foreignID)
	return n
}

// TestMigrateSlugLessPracticeProxy covers criterion b726880a: CASE A re-keys a
// metadata-FREE incident edge (UPSERT slug-ful proxy, re-point LINK before
// DELETE, 2nd run no-op); CASE B re-keys a metadata-BEARING edge with the
// metadata PRESERVED on the re-point LINK's EdgeSpec (the Phase 0 carrier proof —
// the prior fail-loud refusal is gone). Both are successful re-keys.
func TestMigrateSlugLessPracticeProxy(t *testing.T) {
	t.Run("CASE A: metadata-free edge re-keys, 2nd run no-op", func(t *testing.T) {
		slugLess := slugLessPracticeProxyNode("pat-1")
		fc := &fakeGraphCaller{
			// scanSlugLessPracticeProxies: NodeProxy Match scan against knowledge.
			nodeMatchResults: map[graphKey][]*knowledgev1.Node{
				{Type: "knowledge"}: {slugLess},
			},
			// listForeignGraphs: practice/go is the loaded foreign graph.
			listGraphsResult: listGraphsResultFor(t, [2]string{"practice", "go"}),
			// locateForeignNode: pat-1 resolves in practice/go → slug=go.
			queryResponsesByGraphName: map[graphKey]map[string]kgtools.ToolResult{
				{Type: "practice", Name: "go"}: {"pat-1": graphNodeResult(t, "pat-1", "pattern", "Pat", "a pattern")},
			},
			// render.IterEdges: one metadata-FREE incident edge dec-1 -[uses]-> proxy.
			edgesByID: map[string][]*knowledgev1.Edge{
				"proxy:practice:pat-1": {{FromId: "dec-1", ToId: "proxy:practice:pat-1", Type: string(kgtypes.EdgeType("uses"))}},
			},
		}

		migrated, err := migratePracticeProxies(context.Background(), fc)
		require.NoError(t, err)
		assert.Equal(t, 1, migrated, "one slug-less proxy re-keyed")

		// Three mutation Executes: UPSERT slug-ful proxy, re-point LINK, DELETE
		// slug-less — in that order (re-point STRICTLY before delete).
		require.Len(t, fc.execMutations, 3, "UPSERT + LINK + DELETE")

		upsert := fc.execMutations[0]
		assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_UPSERT, upsert.GetKind())
		require.Len(t, upsert.GetNodeBodies(), 1)
		assert.Equal(t, "proxy:practice:go:pat-1", upsert.GetNodeBodies()[0].GetId(), "slug-ful proxy id")

		link := fc.execMutations[1]
		assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_LINK, link.GetKind())
		assert.Equal(t, []string{"dec-1"}, link.GetSelection().GetIds(), "from preserved")
		assert.Equal(t, "proxy:practice:go:pat-1", link.GetEdgeSpec().GetToId(), "re-pointed onto slug-ful proxy")
		assert.Equal(t, "uses", link.GetEdgeSpec().GetRelationship())

		del := fc.execMutations[2]
		assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_DELETE, del.GetKind())
		assert.Equal(t, []string{"proxy:practice:pat-1"}, del.GetSelection().GetIds(), "slug-less proxy deleted")

		// Idempotency: a 2nd run with the slug-less proxy now gone from the scan
		// seed is a no-op (this is what makes the once-per-session trigger safe).
		fc2 := &fakeGraphCaller{
			nodeMatchResults: map[graphKey][]*knowledgev1.Node{
				{Type: "knowledge"}: {},
			},
			listGraphsResult: listGraphsResultFor(t, [2]string{"practice", "go"}),
		}
		migrated2, err2 := migratePracticeProxies(context.Background(), fc2)
		require.NoError(t, err2)
		assert.Equal(t, 0, migrated2, "2nd run: no slug-less proxies → no-op")
		assert.Empty(t, fc2.execMutations, "no UPSERT/LINK/DELETE on the no-op run")
	})

	t.Run("CASE B: metadata-bearing edge re-keys WITH metadata preserved", func(t *testing.T) {
		slugLess := slugLessPracticeProxyNode("pat-1")
		// Sub-second LastValidated (T3-1): proves nanosecond precision survives the
		// LinkOneWithMeta RFC3339Nano marshal → engine int64 unix-nanos round-trip.
		lastVal := time.Unix(1717000000, 123456789).UTC()
		fc := &fakeGraphCaller{
			nodeMatchResults: map[graphKey][]*knowledgev1.Node{
				{Type: "knowledge"}: {slugLess},
			},
			listGraphsResult: listGraphsResultFor(t, [2]string{"practice", "go"}),
			queryResponsesByGraphName: map[graphKey]map[string]kgtools.ToolResult{
				{Type: "practice", Name: "go"}: {"pat-1": graphNodeResult(t, "pat-1", "pattern", "Pat", "a pattern")},
			},
			// One metadata-BEARING incident edge dec-1 -[uses]-> proxy, constructed
			// as a fresh slice-literal element (no value-copy of a lock-holding
			// store.Edge var into the slice).
			edgesByID: map[string][]*knowledgev1.Edge{
				"proxy:practice:pat-1": {{
					FromId:        "dec-1",
					ToId:          "proxy:practice:pat-1",
					Type:          string(kgtypes.EdgeType("uses")),
					Weight:        0.7,
					Confidence:    0.9,
					Method:        "linker:helm",
					Evidence:      "chart.yaml",
					LastValidated: lastVal.UnixNano(),
				}},
			},
		}

		migrated, err := migratePracticeProxies(context.Background(), fc)
		require.NoError(t, err)
		assert.Equal(t, 1, migrated, "metadata-bearing proxy re-keys (no refusal)")

		require.Len(t, fc.execMutations, 3, "UPSERT + LINK + DELETE")
		assert.Equal(t, "proxy:practice:go:pat-1", fc.execMutations[0].GetNodeBodies()[0].GetId())

		// The re-point LINK carries the edge metadata onto its EdgeSpec — the
		// wire-level proof LinkOneWithMeta transports it through the Phase 0 carrier.
		link := fc.execMutations[1]
		assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_LINK, link.GetKind())
		spec := link.GetEdgeSpec()
		assert.Equal(t, "proxy:practice:go:pat-1", spec.GetToId())
		assert.InDelta(t, 0.7, spec.GetWeight(), 1e-9, "weight preserved on re-point")
		assert.InDelta(t, 0.9, spec.GetConfidence(), 1e-9, "confidence preserved on re-point")
		assert.Equal(t, "linker:helm", spec.GetMethod(), "method preserved on re-point")
		assert.Equal(t, "chart.yaml", spec.GetEvidence(), "evidence preserved on re-point")
		assert.Equal(t, lastVal.UnixNano(), spec.GetLastValidated(),
			"last_validated preserved with nanosecond precision (sub-second survives)")

		// LINK precedes DELETE (re-point strictly before delete → zero orphans).
		assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_DELETE, fc.execMutations[2].GetKind())
		assert.Equal(t, []string{"proxy:practice:pat-1"}, fc.execMutations[2].GetSelection().GetIds())
	})
}
