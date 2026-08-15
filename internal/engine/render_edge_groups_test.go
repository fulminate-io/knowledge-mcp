// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestRenderTraversal_JSONEdgeGroups pins the traverse JSON arm: the locked
// edge_groups vocabulary, the exclusion of member edges from the flat edges
// array, and the zero-group payload staying exactly as it is today.
func TestRenderTraversal_JSONEdgeGroups(t *testing.T) {
	const start = "s/start.go:Start"
	const key = "s/start.go:12:CALLS:Run"

	cand := func(id, file string, line int32, sig string) *knowledgev1.Node {
		return &knowledgev1.Node{Id: id, SymbolName: "Run", Type: "function", FilePath: file, StartLine: line, Signature: sig}
	}
	member := func(to, method string, conf float64) knowledgev1.Edge {
		return knowledgev1.Edge{FromId: start, ToId: to, Type: "CALLS", Method: method, Evidence: key, Confidence: conf}
	}

	renderJSON := func(t *testing.T, results []TraversalResult, edges []knowledgev1.Edge, truncated bool) map[string]any {
		t.Helper()
		resp := &knowledgev1.ExecuteResponse{
			TraversalResults: traversalResultsToProtoForTest(results),
			TraversalEdges:   edgesToProtoForTest(edges),
			Truncated:        truncated,
		}
		out, rerr := renderTraversalResponse(resp, traverseContext{Start: start, GraphName: "code", Direction: "out", Format: "json"})
		require.NoError(t, rerr)
		var payload map[string]any
		require.NoError(t, json.Unmarshal([]byte(out.Content[0].Text), &payload))
		return payload
	}

	startResult := TraversalResult{Distance: 0, Node: &knowledgev1.Node{Id: start, SymbolName: "Start", Type: "function"}}
	candA := cand("p/a.go:Run", "p/a.go", 10, "func Run(ctx context.Context) error")
	candB := cand("p/b.go:Run", "p/b.go", 20, "func Run(n int) string")

	groupResults := []TraversalResult{
		startResult,
		{Distance: 1, Node: candA},
		{Distance: 1, Node: candB},
	}

	t.Run("closed_group_semantics", func(t *testing.T) {
		payload := renderJSON(t, groupResults, []knowledgev1.Edge{
			member("p/a.go:Run", kgtypes.EdgeMethodAmbiguousName, 0.5),
			member("p/b.go:Run", kgtypes.EdgeMethodAmbiguousName, 0.5),
		}, false)

		rows, ok := payload["edge_groups"].([]any)
		require.True(t, ok, "edge_groups must be present")
		require.Len(t, rows, 1)
		row := rows[0].(map[string]any)
		assert.Equal(t, "exactly-one-of", row["semantics"])
		assert.Equal(t, key, row["group_key"])
		assert.Equal(t, true, row["frontier"])
		assert.InDelta(t, 2, row["declared_candidates"], 0.001)
		assert.Equal(t, true, row["complete"])

		cands := row["candidates"].([]any)
		require.Len(t, cands, 2)
		first := cands[0].(map[string]any)
		assert.Equal(t, "p/a.go:Run", first["id"])
		assert.Equal(t, "p/a.go", first["file"])
		assert.InDelta(t, 10, first["line"], 0.001)
		assert.Equal(t, "func Run(ctx context.Context) error", first["signature"])
	})

	t.Run("open_group_semantics", func(t *testing.T) {
		payload := renderJSON(t, groupResults, []knowledgev1.Edge{
			member("p/a.go:Run", kgtypes.EdgeMethodDynamic, 0.5),
			member("p/b.go:Run", kgtypes.EdgeMethodDynamic, 0.5),
		}, false)
		rows := payload["edge_groups"].([]any)
		require.Len(t, rows, 1)
		assert.Equal(t, "one-of-these-or-beyond", rows[0].(map[string]any)["semantics"])
	})

	t.Run("members_excluded_from_flat_edges", func(t *testing.T) {
		// BOTH HALVES REQUIRED: asserting only the absence is satisfied by an
		// implementation that drops every edge.
		results := append([]TraversalResult{}, groupResults...)
		results = append(results, TraversalResult{Distance: 1, Node: &knowledgev1.Node{Id: "z/Z.go:Z", SymbolName: "Z", Type: "function"}})

		payload := renderJSON(t, results, []knowledgev1.Edge{
			member("p/a.go:Run", kgtypes.EdgeMethodAmbiguousName, 0.5),
			member("p/b.go:Run", kgtypes.EdgeMethodAmbiguousName, 0.5),
			{FromId: start, ToId: "z/Z.go:Z", Type: "CALLS"}, // the bound control
		}, false)

		edges := payload["edges"].([]any)
		seen := map[string]bool{}
		for _, e := range edges {
			seen[e.(map[string]any)["to"].(string)] = true
		}
		assert.True(t, seen["z/Z.go:Z"], "the bound edge is still listed in the flat edges array")
		assert.False(t, seen["p/a.go:Run"], "group members appear only under edge_groups")
		assert.False(t, seen["p/b.go:Run"], "group members appear only under edge_groups")
	})

	t.Run("truncated_zero_group_payload_is_unchanged", func(t *testing.T) {
		// THE JSON-SIDE CATCHER: code traversals now always request edge
		// metadata, so ordinary big walks come back truncated. Neither group key
		// may appear on a payload that reconstructed nothing.
		payload := renderJSON(t,
			[]TraversalResult{startResult, {Distance: 1, Node: &knowledgev1.Node{Id: "z/Z.go:Z", SymbolName: "Z", Type: "function"}}},
			[]knowledgev1.Edge{{FromId: start, ToId: "z/Z.go:Z", Type: "CALLS"}},
			true)

		_, hasGroups := payload["edge_groups"]
		_, hasIncomplete := payload["group_reconstruction_incomplete"]
		assert.False(t, hasGroups, "no edge_groups key on a zero-group payload")
		assert.False(t, hasIncomplete, "no group_reconstruction_incomplete key on a zero-group payload")
	})
}

// TestEdgeMetadataAnnotation_GroupKeyNotLeaked pins the property-keyed
// suppression rule across all three emitters that print Edge.Evidence.
//
// The fixture pair is the same throughout: a GROUP edge, whose Evidence is an
// internal join key already carried by its group block, and a CLOUD edge, whose
// Evidence is a genuine human-readable citation that must keep rendering.
func TestEdgeMetadataAnnotation_GroupKeyNotLeaked(t *testing.T) {
	const groupKey = "a/x.go:1042:CALLS:Run"
	groupEdge := knowledgev1.Edge{
		FromId: "a/x.go:Caller", ToId: "p/a.go:Run", Type: "CALLS",
		Method: kgtypes.EdgeMethodAmbiguousName, Evidence: groupKey, Confidence: 0.5, Weight: 1,
	}
	cloudEdge := knowledgev1.Edge{
		FromId: "a/x.go:Caller", ToId: "img/base:latest", Type: "BUILDS",
		Method: "tier1-dockerfile", Evidence: "Dockerfile:14 COPY src", Confidence: 0.9, Weight: 1,
	}

	t.Run("group_edge_hides_key_and_method", func(t *testing.T) {
		got := edgeMetadataAnnotation(&groupEdge)
		assert.Contains(t, got, "confidence=")
		assert.Contains(t, got, "weight=")
		assert.NotContains(t, got, "evidence=")
		assert.NotContains(t, got, "method=")
		assert.NotContains(t, got, groupKey)
	})

	t.Run("non_group_edge_still_shows_evidence", func(t *testing.T) {
		// THE REQUIRED OTHER HALF: without it, an implementation deleting the
		// branch outright passes the first leg while regressing every cloud edge.
		got := edgeMetadataAnnotation(&cloudEdge)
		assert.Contains(t, got, "evidence=")
		assert.Contains(t, got, "method=")
		assert.Contains(t, got, "Dockerfile:14 COPY src")
	})

	t.Run("graph_wide_map_obeys_the_same_rule", func(t *testing.T) {
		gm := graphWideEdgeMetadata(&groupEdge)
		_, hasEvidence := gm["evidence"]
		_, hasMethod := gm["method"]
		assert.False(t, hasEvidence)
		assert.False(t, hasMethod)
		assert.Contains(t, gm, "confidence", "the non-suppressed fields still ride")

		cm := graphWideEdgeMetadata(&cloudEdge)
		assert.Equal(t, "Dockerfile:14 COPY src", cm["evidence"])
		assert.Equal(t, "tier1-dockerfile", cm["method"])
	})

	t.Run("explain_block_obeys_the_same_rule", func(t *testing.T) {
		groupOut := RenderExplainEdges("code", []knowledgev1.Edge{copyGroupEdge(&groupEdge)}, nil, nil)
		assert.NotContains(t, groupOut, "Evidence (raw)")
		assert.NotContains(t, groupOut, "- Method:")
		assert.NotContains(t, groupOut, groupKey)

		cloudOut := RenderExplainEdges("code", []knowledgev1.Edge{copyGroupEdge(&cloudEdge)}, nil, nil)
		assert.Contains(t, cloudOut, "- Evidence (raw): Dockerfile:14 COPY src")
		assert.Contains(t, cloudOut, "- Method: tier1-dockerfile")
	})
}
