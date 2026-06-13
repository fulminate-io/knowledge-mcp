// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// listGraphsResultJSON builds the listGraphsResult body the fake decodes into
// per-type RETURN_MODE_GRAPH_NAMES reads: a {graphs:[{graph_type,graph_name}]}
// payload. Each pair seeds one loaded graph of that type.
func listGraphsResultJSON(t *testing.T, pairs ...[2]string) *kgtools.ToolResult {
	t.Helper()
	graphs := make([]map[string]string, 0, len(pairs))
	for _, p := range pairs {
		graphs = append(graphs, map[string]string{"graph_type": p[0], "graph_name": p[1]})
	}
	b, err := json.Marshal(map[string]any{"graphs": graphs})
	require.NoError(t, err)
	return &kgtools.ToolResult{Content: []kgtools.ContentBlock{{Type: "text", Text: string(b)}}}
}

// TestResolveCodeReferents_Resolves asserts a referent matching a seeded
// code-graph node resolves to the deterministic code proxy id, and the proxy is
// upserted into the KNOWLEDGE graph specifically (the targetGraph="knowledge"
// pin) — readable via the knowledge-scoped upsert Target.
func TestResolveCodeReferents_Resolves(t *testing.T) {
	const repo = "knowledge"
	const ref = "tools/wire.go:PersistBatch"
	fc := &fakeGraphCaller{
		listGraphsResult: listGraphsResultJSON(t, [2]string{"code", repo}),
		// The referent resolves only in the (code, repo) graph.
		queryResponsesByGraphName: map[graphKey]map[string]kgtools.ToolResult{
			{Type: "code", Name: repo}: {
				ref: nodeResultJSON(t, ref, "function_declaration", nil),
			},
		},
	}
	got := resolveCodeReferents(context.Background(), fc, []string{ref})

	wantProxyID := "proxy:" + repo + ":" + ref
	require.Equal(t, []string{wantProxyID}, got, "resolves to the deterministic code proxy id")

	// The proxy upsert must target the KNOWLEDGE graph (the pin). Find the
	// upsert Mutation ExecuteRequest and assert its Target graph is knowledge.
	var upserts int
	for _, req := range fc.execRequests {
		if req.GetMutation() == nil {
			continue
		}
		upserts++
		assert.Equal(t, "knowledge", req.GetTarget().GetGraph(),
			"proxy must be upserted into the knowledge graph (targetGraph=knowledge pin)")
	}
	require.Equal(t, 1, upserts, "exactly one proxy upsert for the one resolvable referent")
}

// TestResolveCodeReferents_CodeOnly asserts only code graphs are probed: with
// cloud and practice graphs ALSO seeded, resolution issues no fetch against
// them — the thin ListForeignGraphsOfType(code) enumerates code names only, so
// LocateForeignNode never fans out to non-code graphs.
func TestResolveCodeReferents_CodeOnly(t *testing.T) {
	const repo = "knowledge"
	const ref = "tools/wire.go:PersistBatch"
	fc := &fakeGraphCaller{
		listGraphsResult: listGraphsResultJSON(t,
			[2]string{"code", repo},
			[2]string{"cloud", "acct-1"},
			[2]string{"practice", "go"},
		),
		queryResponsesByGraphName: map[graphKey]map[string]kgtools.ToolResult{
			{Type: "code", Name: repo}: {
				ref: nodeResultJSON(t, ref, "function_declaration", nil),
			},
		},
	}
	got := resolveCodeReferents(context.Background(), fc, []string{ref})
	require.Len(t, got, 1, "the code referent still resolves")

	// No ByID query may target cloud or practice. Walk every recorded query
	// ExecuteRequest and assert its Target graph is never cloud/practice.
	for _, req := range fc.execRequests {
		q := req.GetQuery()
		if q == nil || q.GetById() == "" {
			continue
		}
		g := req.GetTarget().GetGraph()
		assert.NotEqual(t, "cloud", g, "no by-id fetch may target the cloud graph")
		assert.NotEqual(t, "practice", g, "no by-id fetch may target the practice graph")
	}
}

// TestResolveCodeReferents_DropsUnresolvable asserts a referent absent from every
// code graph is dropped — not returned, no raw id appended, never an edge target.
func TestResolveCodeReferents_DropsUnresolvable(t *testing.T) {
	const repo = "knowledge"
	fc := &fakeGraphCaller{
		listGraphsResult: listGraphsResultJSON(t, [2]string{"code", repo}),
		// (code, repo) graph is configured but the referent is absent from it.
		queryResponsesByGraphName: map[graphKey]map[string]kgtools.ToolResult{
			{Type: "code", Name: repo}: {},
		},
	}
	got := resolveCodeReferents(context.Background(), fc, []string{"tools/ghost.go:Missing"})
	assert.Empty(t, got, "an unresolvable referent is dropped, never returned as a raw id")

	// And no proxy upsert was issued (nothing resolved → nothing to upsert).
	for _, req := range fc.execRequests {
		assert.Nil(t, req.GetMutation(), "no proxy upsert for a dropped referent")
	}
}
