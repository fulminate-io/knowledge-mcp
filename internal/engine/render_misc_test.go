// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// candidateNode builds a hydrated candidate with DISTINCT file, line and
// signature, so a renderer that read one node's facts for every candidate cannot
// pass candidate_carries_three_facts.
func candidateNode(id, file string, line int32, sig string) *knowledgev1.Node {
	return &knowledgev1.Node{
		Id: id, SymbolName: "Run", Type: "function",
		FilePath: file, StartLine: line, Signature: sig,
	}
}

// groupTraversalResp seeds a traversal response whose edges form one candidate
// group from caller, plus whichever candidate nodes the walk reached.
func groupTraversalResp(reached []*knowledgev1.Node, edges []knowledgev1.Edge) *knowledgev1.ExecuteResponse {
	results := []TraversalResult{{Distance: 0, Node: &knowledgev1.Node{Id: "a/x.go:Caller", SymbolName: "Caller", Type: "function"}}}
	for _, n := range reached {
		results = append(results, TraversalResult{Distance: 1, Node: n})
	}
	return &knowledgev1.ExecuteResponse{
		TraversalResults: traversalResultsToProtoForTest(results),
		TraversalEdges:   edgesToProtoForTest(edges),
	}
}

// TestRenderTraversal_CandidateGroups pins the locked group block on the
// traverse TEXT arm: header semantics, the three per-candidate facts, group-level
// confidence, the reached marker, and the declared-zero fallback.
func TestRenderTraversal_CandidateGroups(t *testing.T) {
	const caller = "a/x.go:Caller"
	const key = "a/x.go:1042:CALLS:Run"

	candA := candidateNode("p/a.go:Run", "p/a.go", 10, "func Run(ctx context.Context) error")
	candB := candidateNode("p/b.go:Run", "p/b.go", 20, "func Run(n int) string")
	candC := candidateNode("p/c.go:Run", "p/c.go", 30, "func Run()")

	groupEdges := func(method string, conf float64, tos ...string) []knowledgev1.Edge {
		out := make([]knowledgev1.Edge, 0, len(tos))
		for _, to := range tos {
			out = append(out, knowledgev1.Edge{
				FromId: caller, ToId: to, Type: "CALLS",
				Method: method, Evidence: key, Confidence: conf,
			})
		}
		return out
	}

	render := func(t *testing.T, resp *knowledgev1.ExecuteResponse) string {
		t.Helper()
		out, rerr := renderTraversalResponse(resp, traverseContext{Start: caller, GraphName: "code", Direction: "out", Format: "text"})
		require.NoError(t, rerr)
		return out.Content[0].Text
	}

	t.Run("closed_group_header", func(t *testing.T) {
		text := render(t, groupTraversalResp(
			[]*knowledgev1.Node{candA, candB, candC},
			groupEdges(kgtypes.EdgeMethodAmbiguousName, 1.0/3.0, "p/a.go:Run", "p/b.go:Run", "p/c.go:Run"),
		))
		assert.Contains(t, text, "one of 3 candidates")
		assert.Contains(t, text, "exactly one is the real target")
		assert.NotContains(t, text, "or beyond")
	})

	t.Run("open_group_header", func(t *testing.T) {
		text := render(t, groupTraversalResp(
			[]*knowledgev1.Node{candA, candB},
			groupEdges(kgtypes.EdgeMethodDynamic, 0.5, "p/a.go:Run", "p/b.go:Run"),
		))
		assert.Contains(t, text, "one of 2 candidates")
		assert.Contains(t, text, "or beyond")
		assert.NotContains(t, text, "exactly one is the real target")
	})

	t.Run("candidate_carries_three_facts", func(t *testing.T) {
		text := render(t, groupTraversalResp(
			[]*knowledgev1.Node{candA, candB, candC},
			groupEdges(kgtypes.EdgeMethodAmbiguousName, 1.0/3.0, "p/a.go:Run", "p/b.go:Run", "p/c.go:Run"),
		))
		for _, want := range []string{
			"`p/a.go:Run` - p/a.go:10 - func Run(ctx context.Context) error",
			"`p/b.go:Run` - p/b.go:20 - func Run(n int) string",
			"`p/c.go:Run` - p/c.go:30 - func Run()",
		} {
			assert.Contains(t, text, want)
		}
	})

	t.Run("confidence_once_per_group", func(t *testing.T) {
		text := render(t, groupTraversalResp(
			[]*knowledgev1.Node{candA, candB, candC},
			groupEdges(kgtypes.EdgeMethodAmbiguousName, 1.0/3.0, "p/a.go:Run", "p/b.go:Run", "p/c.go:Run"),
		))
		assert.Equal(t, 1, strings.Count(text, "confidence"),
			"confidence is shown once at group level, never per candidate line")
	})

	t.Run("reached_marker", func(t *testing.T) {
		// A REVERSE walk, which is where exactly one candidate is reached: the
		// reader started AT candB and the group's source is only reachable across
		// a group edge. (On a forward walk from the source every candidate is at
		// the frontier and all are marked — the vocabulary states both cases.)
		// The other two are candidates the reader has not arrived at, and stay
		// unmarked and unhydrated.
		resp := &knowledgev1.ExecuteResponse{
			TraversalResults: traversalResultsToProtoForTest([]TraversalResult{
				{Distance: 0, Node: candB},
				{Distance: 1, Node: &knowledgev1.Node{Id: caller, SymbolName: "Caller", Type: "function"}},
			}),
			TraversalEdges: edgesToProtoForTest(
				groupEdges(kgtypes.EdgeMethodAmbiguousName, 1.0/3.0, "p/a.go:Run", "p/b.go:Run", "p/c.go:Run")),
		}
		out, rerr := renderTraversalResponse(resp, traverseContext{Start: "p/b.go:Run", GraphName: "code", Direction: "in", Format: "text"})
		require.NoError(t, rerr)
		text := out.Content[0].Text

		assert.Equal(t, 1, strings.Count(text, "the node this walk reached"))
		bLine := ""
		for l := range strings.SplitSeq(text, "\n") {
			if strings.Contains(l, "`p/b.go:Run`") {
				bLine = l
			}
		}
		require.NotEmpty(t, bLine)
		assert.Contains(t, bLine, "the node this walk reached")
		assert.Contains(t, text, "(unhydrated)", "unreached candidates are still listed, never dropped")
	})

	t.Run("declared_zero_uses_observed_count", func(t *testing.T) {
		// THE CATCHER: Confidence 0 makes Declared unrecoverable. The header must
		// fall back to the OBSERVED count and must never print "one of 0" above a
		// list of three real entries.
		text := render(t, groupTraversalResp(
			[]*knowledgev1.Node{candA, candB, candC},
			groupEdges(kgtypes.EdgeMethodAmbiguousName, 0, "p/a.go:Run", "p/b.go:Run", "p/c.go:Run"),
		))
		assert.Contains(t, text, "one of 3 candidates")
		assert.Contains(t, text, "declared count unknown")
		assert.NotContains(t, text, "one of 0")
	})
}

// TestRenderTraversal_GroupFrontierStopsExpansion pins the uniform short-circuit:
// a walk never expands THROUGH a candidate group, while everything reachable by a
// bound path is untouched and a group-free response is byte-for-byte unchanged.
func TestRenderTraversal_GroupFrontierStopsExpansion(t *testing.T) {
	const start = "s/start.go:Start"

	node := func(id string) *knowledgev1.Node {
		return &knowledgev1.Node{Id: id, SymbolName: id, Type: "function"}
	}
	plain := func(from, to string) knowledgev1.Edge {
		return knowledgev1.Edge{FromId: from, ToId: to, Type: "CALLS"}
	}
	member := func(to string) knowledgev1.Edge {
		return knowledgev1.Edge{
			FromId: start, ToId: to, Type: "CALLS",
			Method: kgtypes.EdgeMethodAmbiguousName, Evidence: "s/start.go:12:CALLS:Run", Confidence: 0.5,
		}
	}
	// A, B are the two candidates of one group hanging off start; Z sits BEHIND
	// candidate A via an ordinary edge.
	resultsOf := func(ids ...string) []TraversalResult {
		out := []TraversalResult{{Distance: 0, Node: node(start)}}
		for _, id := range ids {
			out = append(out, TraversalResult{Distance: 1, Node: node(id)})
		}
		return out
	}
	renderText := func(t *testing.T, resp *knowledgev1.ExecuteResponse) string {
		t.Helper()
		out, rerr := renderTraversalResponse(resp, traverseContext{Start: start, GraphName: "code", Direction: "out", Format: "text"})
		require.NoError(t, rerr)
		return out.Content[0].Text
	}

	t.Run("no_groups_keeps_every_node", func(t *testing.T) {
		// THE ZERO-CHANGE PROOF, asserted as set equality against the input.
		results := resultsOf("a/A.go:A", "z/Z.go:Z")
		edges := []knowledgev1.Edge{plain(start, "a/A.go:A"), plain("a/A.go:A", "z/Z.go:Z")}
		kept, _, incomplete := FrontierFilter(start, results, edges, nil)
		require.Len(t, kept, len(results))
		for i := range results {
			assert.Equal(t, results[i].Node.Id, kept[i].Node.Id)
			assert.Equal(t, results[i].Distance, kept[i].Distance)
		}
		assert.False(t, incomplete)
	})

	t.Run("depth2_through_a_group_is_dropped", func(t *testing.T) {
		// THE NAMED CATCHER: an implementation that renders the group block but
		// still lists everything behind it fails here.
		results := resultsOf("a/A.go:A", "b/B.go:B", "z/Z.go:Z")
		edges := []knowledgev1.Edge{member("a/A.go:A"), member("b/B.go:B"), plain("a/A.go:A", "z/Z.go:Z")}
		groups, _ := GroupCandidateEdges(edges)
		require.Len(t, groups, 1)
		kept, _, _ := FrontierFilter(start, results, edges, groups)

		ids := map[string]bool{}
		for _, r := range kept {
			ids[r.Node.Id] = true
		}
		assert.True(t, ids["a/A.go:A"], "candidate A is AT the frontier and stays")
		assert.True(t, ids["b/B.go:B"], "candidate B is AT the frontier and stays")
		assert.False(t, ids["z/Z.go:Z"], "Z is only reachable THROUGH the group and must be dropped")
	})

	t.Run("node_reachable_by_a_bound_path_survives", func(t *testing.T) {
		// Without this leg, an implementation that drops everything at distance
		// >= 2 passes the previous leg while destroying ordinary traversal.
		results := resultsOf("a/A.go:A", "b/B.go:B", "z/Z.go:Z")
		edges := []knowledgev1.Edge{
			member("a/A.go:A"), member("b/B.go:B"),
			plain("a/A.go:A", "z/Z.go:Z"),
			plain(start, "z/Z.go:Z"), // the bound path to Z
		}
		groups, _ := GroupCandidateEdges(edges)
		kept, _, _ := FrontierFilter(start, results, edges, groups)
		ids := map[string]bool{}
		for _, r := range kept {
			ids[r.Node.Id] = true
		}
		assert.True(t, ids["z/Z.go:Z"], "Z has a bound path from start and must survive")
	})

	t.Run("unexplained_node_is_kept_and_flagged", func(t *testing.T) {
		results := resultsOf("a/A.go:A", "b/B.go:B", "u/U.go:Unexplained")
		edges := []knowledgev1.Edge{member("a/A.go:A"), member("b/B.go:B")}
		groups, _ := GroupCandidateEdges(edges)
		kept, _, incomplete := FrontierFilter(start, results, edges, groups)
		ids := map[string]bool{}
		for _, r := range kept {
			ids[r.Node.Id] = true
		}
		assert.True(t, ids["u/U.go:Unexplained"], "reachability unknown, not disproven — keep it")
		assert.True(t, incomplete)
	})

	t.Run("truncated_zero_group_response_is_not_flagged", func(t *testing.T) {
		// THE CATCHER for a flag driven by truncation alone. Code traversals now
		// always request edge metadata, so ordinary big walks come back truncated
		// and must stay byte-identical to today.
		resp := &knowledgev1.ExecuteResponse{
			TraversalResults: traversalResultsToProtoForTest(resultsOf("a/A.go:A")),
			TraversalEdges:   edgesToProtoForTest([]knowledgev1.Edge{plain(start, "a/A.go:A")}),
			Truncated:        true,
		}
		text := renderText(t, resp)
		assert.NotContains(t, text, "group reconstruction incomplete")
	})

	t.Run("truncated_with_group_is_flagged", func(t *testing.T) {
		// Paired with the leg above so the flag cannot be satisfied by hard-coding
		// false: same truncated response, one group present.
		resp := &knowledgev1.ExecuteResponse{
			TraversalResults: traversalResultsToProtoForTest(resultsOf("a/A.go:A", "b/B.go:B")),
			TraversalEdges:   edgesToProtoForTest([]knowledgev1.Edge{member("a/A.go:A"), member("b/B.go:B")}),
			Truncated:        true,
		}
		text := renderText(t, resp)
		assert.Contains(t, text, "group reconstruction incomplete")
	})
}

// nodesResp builds the typed-wire node-carrier ExecuteResponse via the shared
// enginetest builder (P2-T5 deleted the nodes_json blob). Total rides native.
func nodesResp(t *testing.T, nodes []*knowledgev1.Node, total int) *knowledgev1.ExecuteResponse {
	t.Helper()
	resp := enginetest.ResponseWithNodes(nodes...)
	resp.Total = int64(total)
	return resp
}

func TestRenderBrowse_NumberedListAndPagination(t *testing.T) {
	nodes := []*knowledgev1.Node{
		{Id: "n1", SymbolName: "First", Status: "open", Description: "desc one"},
		{Id: "n2", SymbolName: "Second", Status: "closed"},
	}
	resp := nodesResp(t, nodes, 5) // total 5 > offset(2)+len(2) → pagination footer.
	out, err := renderBrowseResponse(resp, browseContext{Label: "knowledge", NodeType: "finding", Offset: 2, Format: "text"})
	require.NoError(t, err)
	text := out.Content[0].Text
	assert.Contains(t, text, "## knowledge — 2 finding nodes (offset 2)")
	assert.Contains(t, text, "3. **First** [open]\n   ID: n1\n   desc one")
	assert.Contains(t, text, "4. **Second** [closed]\n   ID: n2")
	assert.Contains(t, text, "_Use offset=4 to see more._")
}

func TestRenderBrowse_EmptyTyped(t *testing.T) {
	out, err := renderBrowseResponse(nodesResp(t, nil, 0), browseContext{Label: "knowledge", NodeType: "finding", Format: "text"})
	require.NoError(t, err)
	assert.Contains(t, out.Content[0].Text, "No finding nodes in knowledge graph.")
}

func TestRenderBrowse_EmptyUntyped(t *testing.T) {
	out, err := renderBrowseResponse(nodesResp(t, nil, 0), browseContext{Label: "knowledge", NodeType: "", Format: "text"})
	require.NoError(t, err)
	assert.Contains(t, out.Content[0].Text, "No nodes in knowledge graph match the requested filters.")
}

func TestRenderBrowse_MetaInline(t *testing.T) {
	nodes := []*knowledgev1.Node{
		{Id: "n1", SymbolName: "X", Metadata: map[string]string{"dsl_pattern": "p1"}},
	}
	out, err := renderBrowseResponse(nodesResp(t, nodes, 1), browseContext{
		Label: "knowledge", NodeType: "finding", Format: "text", MetaKeys: []string{"dsl_pattern"},
	})
	require.NoError(t, err)
	assert.Contains(t, out.Content[0].Text, "\n   dsl_pattern: p1")
}

func TestRenderBrowse_JSON(t *testing.T) {
	nodes := []*knowledgev1.Node{
		{Id: "n1", SymbolName: "X", Type: "finding", Status: "open"},
	}
	out, err := renderBrowseResponse(nodesResp(t, nodes, 1), browseContext{Label: "knowledge", NodeType: "finding", Format: "json"})
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(out.Content[0].Text), &payload))
	assert.Equal(t, "knowledge", payload["graph"])
	assert.Equal(t, "finding", payload["type"])
	assert.InEpsilon(t, float64(1), payload["total"], 0.0001)
}

func TestRenderTraversal_FlatList(t *testing.T) {
	results := []TraversalResult{
		{Distance: 0, Node: &knowledgev1.Node{Id: "n0", SymbolName: "Root", Type: "plan"}},
		{Distance: 1, Node: &knowledgev1.Node{Id: "n1", SymbolName: "Child", Type: "phase"}},
	}
	resp := &knowledgev1.ExecuteResponse{TraversalResults: traversalResultsToProtoForTest(results)}

	out, rerr := renderTraversalResponse(resp, traverseContext{Start: "n0", Direction: "both", Format: "text"})
	require.NoError(t, rerr)
	text := out.Content[0].Text
	assert.Contains(t, text, "## Traversal from n0 (graph=knowledge, direction=both)")
	assert.Contains(t, text, "- [plan] Root (n0) at depth 0")
	assert.Contains(t, text, "- [phase] Child (n1) at depth 1")
}

func TestRenderTraversal_Empty(t *testing.T) {
	out, err := renderTraversalResponse(&knowledgev1.ExecuteResponse{}, traverseContext{Start: "n0", Direction: "out", Format: "text"})
	require.NoError(t, err)
	assert.Contains(t, out.Content[0].Text, "No nodes reached.")
}

func TestRenderTraversal_JSON_EdgesAlwaysEmpty(t *testing.T) {
	results := []TraversalResult{{Distance: 0, Node: &knowledgev1.Node{Id: "n0", SymbolName: "Root", Type: "plan"}}}
	resp := &knowledgev1.ExecuteResponse{TraversalResults: traversalResultsToProtoForTest(results)}
	out, err := renderTraversalResponse(resp, traverseContext{Start: "n0", GraphName: "code", Direction: "out", Format: "json"})
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(out.Content[0].Text), &payload))
	assert.Equal(t, "code", payload["graph"])
	assert.Empty(t, payload["edges"], "include_edge_metadata is denied → edges always empty")
}

func TestRenderNodesByIDs_JSON(t *testing.T) {
	nodes := []*knowledgev1.Node{
		{Id: "a", SymbolName: "A"},
		{Id: "b", SymbolName: "B"},
	}
	out, err := renderNodesByIDsResponse(nodesResp(t, nodes, 2), "knowledge", "json", nil)
	require.NoError(t, err)
	var payload struct {
		Label string              `json:"label"`
		Nodes []*knowledgev1.Node `json:"nodes"`
	}
	require.NoError(t, json.Unmarshal([]byte(out.Content[0].Text), &payload))
	assert.Equal(t, "knowledge", payload.Label)
	require.Len(t, payload.Nodes, 2)
	assert.Equal(t, "a", payload.Nodes[0].Id)
}

func TestRenderNodesByIDs_DefaultIsJSON(t *testing.T) {
	nodes := []*knowledgev1.Node{
		{Id: "a", SymbolName: "A"},
	}
	out, err := renderNodesByIDsResponse(nodesResp(t, nodes, 1), "knowledge", "", nil)
	require.NoError(t, err)
	assert.Contains(t, out.Content[0].Text, `"label":"knowledge"`)
}

func TestRenderMutation_CreateIDs(t *testing.T) {
	t.Run("single text", func(t *testing.T) {
		resp := &knowledgev1.ExecuteResponse{Ids: []string{"new-id"}}
		out := renderMutationResponse(resp, knowledgev1.MutationPlan_MUTATION_KIND_CREATE, "text")
		assert.Contains(t, out.Content[0].Text, "Created → ID: new-id")
	})
	t.Run("batch text", func(t *testing.T) {
		resp := &knowledgev1.ExecuteResponse{Ids: []string{"a", "b"}}
		out := renderMutationResponse(resp, knowledgev1.MutationPlan_MUTATION_KIND_CREATE, "text")
		assert.Contains(t, out.Content[0].Text, "Created 2 nodes → IDs: a, b")
	})
	t.Run("json", func(t *testing.T) {
		resp := &knowledgev1.ExecuteResponse{Ids: []string{"a", "b"}}
		out := renderMutationResponse(resp, knowledgev1.MutationPlan_MUTATION_KIND_CREATE, "json")
		assert.Contains(t, out.Content[0].Text, `"ids":["a","b"]`)
	})
}

func TestRenderMutation_AffectedCount(t *testing.T) {
	cases := []struct {
		kind knowledgev1.MutationPlan_MutationKind
		verb string
	}{
		{knowledgev1.MutationPlan_MUTATION_KIND_UPDATE, "Updated"},
		{knowledgev1.MutationPlan_MUTATION_KIND_DELETE, "Deleted"},
		{knowledgev1.MutationPlan_MUTATION_KIND_LINK, "Linked"},
		{knowledgev1.MutationPlan_MUTATION_KIND_UNLINK, "Unlinked"},
	}
	for _, tc := range cases {
		t.Run(tc.verb, func(t *testing.T) {
			resp := &knowledgev1.ExecuteResponse{AffectedCount: 3}
			out := renderMutationResponse(resp, tc.kind, "text")
			assert.Contains(t, out.Content[0].Text, tc.verb+" 3 node(s)")
		})
	}
}
