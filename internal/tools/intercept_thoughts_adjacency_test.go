// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/paging"
)

// adjFakeCaller is a scripted GraphCaller for the thoughts(adjacency) tests at
// scope="all". It answers the two read shapes fetchAdjacency issues:
//   - a type=thought browse (Selection.NodeTypes carries "thought") → the seeded
//     thought nodes on the first page, empty thereafter so the drain terminates.
//   - RETURN_MODE_EDGES → the edges matching the requested type set. scope="all"
//     now issues ONE unified read covering both the clustering types and
//     kg-contains, so there is no longer a separate session-expansion read; no
//     sessions are seeded, so adjacency is exactly the seeded edge set either way.
type adjFakeCaller struct {
	thoughts []*knowledgev1.Node
	edges    []*knowledgev1.Edge
	// edgeCalls records the requested edge-type filter of every RETURN_MODE_EDGES
	// request, so a test can assert HOW MANY wire reads the handler issued rather
	// than only what it rendered.
	edgeCalls [][]string
}

func (c *adjFakeCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	if q == nil {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		ets := q.GetSelection().GetEdgeTypes()
		c.edgeCalls = append(c.edgeCalls, append([]string(nil), ets...))
		// UNION, not first-match: the wire returns every edge matching ANY requested
		// type (an empty set means every type). No sessions are seeded, so the
		// kg-contains membership contributes nothing — but bailing out as soon as the
		// request MENTIONED kg-contains was adequate only while each caller asked for
		// a single type, and the unified pivot read asks for seven at once.
		want := make(map[string]bool, len(ets))
		for _, et := range ets {
			want[et] = true
		}
		var out []*knowledgev1.Edge
		for _, e := range c.edges {
			if len(want) == 0 || want[e.GetType()] {
				out = append(out, e)
			}
		}
		return &knowledgev1.ExecuteResponse{Edges: bandNarrow(out, q)}, nil
	}
	// type=thought browse: first page carries the seeded thoughts, later
	// offset pages are empty (the drain stops on the first short page anyway).
	if q.GetOffset() > 0 {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	return &knowledgev1.ExecuteResponse{Nodes: c.thoughts}, nil
}

// seededAdjFixture returns two thoughts joined by one relates-to edge.
func seededAdjFixture() *adjFakeCaller {
	return &adjFakeCaller{
		thoughts: []*knowledgev1.Node{
			{Id: "tA", Type: string(kgtypes.NodeThought), SymbolName: "ThoughtA"},
			{Id: "tB", Type: string(kgtypes.NodeThought), SymbolName: "ThoughtB"},
		},
		edges: []*knowledgev1.Edge{
			{Type: string(kgtypes.EdgeRelatesTo), FromId: "tA", ToId: "tB"},
		},
	}
}

func callAdjacency(t *testing.T, deps ClientDeps, args map[string]any) (bool, kgtools.ToolResult) {
	t.Helper()
	raw, err := json.Marshal(args)
	require.NoError(t, err)
	return InterceptThoughts(opCtx(), deps, kgtools.CallToolParams{Name: "thoughts", Arguments: raw})
}

// TestAdjacency_DispatchClaimed is the core QA repro: a schema-advertised op
// must be CLAIMED client-side, not fall through to the engine deny. Before the
// dispatch case landed, InterceptThoughts returned handled==false and the call
// reached the engine "is not a recognized engine-reducible shape" deny.
func TestAdjacency_DispatchClaimed(t *testing.T) {
	t.Parallel()
	deps := ctxPackDeps{gc: seededAdjFixture()}
	handled, res := callAdjacency(t, deps, map[string]any{"operation": "adjacency", "scope": "all"})
	require.True(t, handled, "thoughts(adjacency) must be claimed by the client intercept")
	assert.False(t, res.IsError, "a valid scope=all adjacency must not error: %s", toolResultText(res))
	assert.NotContains(t, toolResultText(res), "is not a recognized engine-reducible shape",
		"adjacency must not fall through to the engine deny")
}

// TestAdjacency_ScopeEmptyErrorsLoudly: an empty scope is a loud error (the wire
// validation surfaces "'scope' is required").
func TestAdjacency_ScopeEmptyErrorsLoudly(t *testing.T) {
	t.Parallel()
	deps := ctxPackDeps{gc: seededAdjFixture()}
	handled, res := callAdjacency(t, deps, map[string]any{"operation": "adjacency"})
	require.True(t, handled)
	assert.True(t, res.IsError, "empty scope must be a loud error")
	assert.Contains(t, toolResultText(res), "'scope' is required")
}

// TestAdjacency_ScopeUnknownErrorsLoudly: an unrecognized scope is a loud error.
func TestAdjacency_ScopeUnknownErrorsLoudly(t *testing.T) {
	t.Parallel()
	deps := ctxPackDeps{gc: seededAdjFixture()}
	handled, res := callAdjacency(t, deps, map[string]any{"operation": "adjacency", "scope": "bogus"})
	require.True(t, handled)
	assert.True(t, res.IsError, "unknown scope must be a loud error")
	assert.Contains(t, toolResultText(res), "unknown scope")
}

// adjJSON is the decoded shape of the json render arm.
type adjJSON struct {
	NodeIDs   []string            `json:"node_ids"`
	Adjacency map[string][]string `json:"adjacency"`
}

// TestAdjacency_JSONShape: format=json on scope=all returns top-level node_ids
// (array) and adjacency (object) carrying the seeded edge.
func TestAdjacency_JSONShape(t *testing.T) {
	t.Parallel()
	deps := ctxPackDeps{gc: seededAdjFixture()}
	handled, res := callAdjacency(t, deps, map[string]any{"operation": "adjacency", "scope": "all", "format": "json"})
	require.True(t, handled)
	require.False(t, res.IsError, "json adjacency errored: %s", toolResultText(res))

	var got adjJSON
	require.NoError(t, json.Unmarshal([]byte(toolResultText(res)), &got))
	assert.ElementsMatch(t, []string{"tA", "tB"}, got.NodeIDs)
	require.NotNil(t, got.Adjacency)
	assert.Contains(t, got.Adjacency["tA"], "tB", "adjacency must carry the seeded tA↔tB edge")
}

// TestAdjacency_ThoughtIDsProjection: scope=all + thought_ids:[tA] projects
// node_ids down to just the requested subset.
func TestAdjacency_ThoughtIDsProjection(t *testing.T) {
	t.Parallel()
	deps := ctxPackDeps{gc: seededAdjFixture()}
	handled, res := callAdjacency(t, deps, map[string]any{
		"operation": "adjacency", "scope": "all", "format": "json", "thought_ids": []string{"tA"},
	})
	require.True(t, handled)
	require.False(t, res.IsError, "projected adjacency errored: %s", toolResultText(res))

	var got adjJSON
	require.NoError(t, json.Unmarshal([]byte(toolResultText(res)), &got))
	assert.Equal(t, []string{"tA"}, got.NodeIDs, "node_ids must be projected to the requested subset")
}

// TestAdjacency_TextArmSummarizesByCount: the default (text) arm returns
// a non-error summary that carries the seeded node COUNT and does NOT dump the
// adjacency map — a seeded neighbor id ("tB") appears only as a per-node count
// line for tA, never as a value inside a marshaled "tB" map entry. We assert
// the summary contains the node count and the per-node neighbor-count phrasing,
// and that it does NOT contain a JSON map dump (no "adjacency" key, no "[\"tB\"]"
// array literal).
func TestAdjacency_TextArmSummarizesByCount(t *testing.T) {
	t.Parallel()
	deps := ctxPackDeps{gc: seededAdjFixture()}
	handled, res := callAdjacency(t, deps, map[string]any{"operation": "adjacency", "scope": "all"})
	require.True(t, handled)
	require.False(t, res.IsError, "text adjacency errored: %s", toolResultText(res))

	body := toolResultText(res)
	assert.Contains(t, body, "2 node_ids", "text summary must report the seeded node count")
	assert.Contains(t, body, "neighbors", "text summary must use per-node neighbor-count phrasing")
	// The full map is never marshaled in the text arm: a dumped map would carry
	// a JSON array literal of neighbor ids; the count summary never does.
	assert.False(t, strings.Contains(body, `["tB"]`) || strings.Contains(body, `"adjacency"`),
		"text arm must NOT dump the adjacency map, only counts: %s", body)
}

// TestAdjacency_ColdPathIssuesOneEdgeRead pins the WIDTH of the on-demand surface's
// wire traffic, which nothing previously observed.
//
// This handler is the cold path: corpusSourceFromDeps returns the ClusterProvider or
// nil, never a *passReads, so without an explicit per-call read memo the unified
// pivot-edge read memoizes NOTHING and the session-sibling expansion issues a SECOND
// wire read over the very same pivot. Wrapping the source in NewReadMemo makes one
// read serve both — strictly FEWER READS than the two this surface issued before the
// collapse. Rows are a separate axis and are NOT claimed to improve: the unified type
// set is shared with the loop deliberately, so this path reads charged-by rows it
// then discards. Reads are what this gate measures.
//
// The assertion is on the WIRE CALLS, not the rendered output: every rendering
// assertion in this file passes at one read and at two, which is exactly why the
// regression was invisible here.
func TestAdjacency_ColdPathIssuesOneEdgeRead(t *testing.T) {
	t.Parallel()
	fake := seededAdjFixture()
	deps := ctxPackDeps{gc: fake}

	handled, res := callAdjacency(t, deps,
		map[string]any{"operation": "adjacency", "scope": "all", "format": "json"})
	require.True(t, handled)
	require.False(t, res.IsError, "adjacency errored: %s", toolResultText(res))

	// ONE LOGICAL read, issued as exactly paging.EdgeBandCount banded Executes: the
	// unified read is a banded match-all sweep now and this counter counts round-trips.
	// Asserted against the CONSTANT rather than a literal so the two cannot drift.
	require.Len(t, fake.edgeCalls, paging.EdgeBandCount,
		"scope=all must issue exactly ONE edge read for the whole call (EdgeBandCount banded "+
			"Executes) — the unified read serving both the adjacency and the session-membership expansion")

	// KNOWN-POSITIVE: the one read is the UNIFIED set, not a narrowed leftover. A
	// single read carrying only kg-contains would satisfy the count above while
	// having lost the adjacency edges entirely.
	assert.Contains(t, fake.edgeCalls[0], string(kgtypes.EdgeRelatesTo),
		"the single read carries the clustering types")
	assert.Contains(t, fake.edgeCalls[0], string(kgtypes.EdgeKGContains),
		"and the session-membership type it now also serves")

	// WORK STILL HAPPENED: the seeded edge is still in the rendered adjacency.
	var got adjJSON
	require.NoError(t, json.Unmarshal([]byte(toolResultText(res)), &got))
	assert.Contains(t, got.Adjacency["tA"], "tB",
		"one read, same answer — the seeded edge still reaches the caller")
}
