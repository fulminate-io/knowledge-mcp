// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// Parity tests. Each intercept's parity check seeds an
// in-memory node+edge fixture, calls the intercept, and asserts byte
// equality against the pre-relocation golden captured in Phase 1.
// The scrub regexes match testdata_capture_test.go (build-tag
// goldengen, deleted in Phase 5).

// Scrubbers — must match scrubAll() in testdata_capture_test.go so
// the parity diff is meaningful. Duplicated here because the capture
// file is build-tagged out of the default test binary.
var (
	parityIDRegex            = regexp.MustCompile(`[0-9a-f]{32}`)
	parityUUIDRegex          = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	parityRFC3339NanoRegex   = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})`)
	parityTimeRegex          = regexp.MustCompile(`\d{4}-\d{2}-\d{2} \d{2}:\d{2}`)
	parityShortIDInLineRegex = regexp.MustCompile(`id: [0-9a-f]{12}\)`)
)

// scrubForParity applies the same scrubbers the goldengen capture
// uses so re-runs produce stable bytes for the byte-equality assert.
func scrubForParity(s string) string {
	s = parityUUIDRegex.ReplaceAllString(s, "<UUID>")
	s = parityRFC3339NanoRegex.ReplaceAllString(s, "<TIMENANO>")
	s = parityIDRegex.ReplaceAllString(s, "<ID>")
	s = parityShortIDInLineRegex.ReplaceAllString(s, "id: <SHORTID>)")
	s = parityTimeRegex.ReplaceAllString(s, "<TIME>")
	return s
}

// readGolden loads testdata/<name>.golden.
func readGolden(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	b, err := os.ReadFile(path)
	require.NoError(t, err, "read golden %s", path)
	return string(b)
}

// mustMarshal panics on json.Marshal failure. Test-only helper that
// suppresses the errchkjson lint on test payloads we know are pure
// string-keyed maps. Failure here would be a test bug, not a
// production concern.
func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v) //nolint:errchkjson // test payloads; failure = test bug
	require.NoError(t, err)
	return b
}

// parityGraphFixture is the in-tools parity-test fixture.
// Modeled on cmd/knowledge/internal/projects/render/testutil_test.go's
// graphFixture but in-package (test file → can't import the render
// test helpers). Answers the wire shapes the intercepts emit:
//   - query(id) → return the bare node JSON for FetchNode.
//   - query(id, include_edges:true) → return {edges:[]} for IterEdges.
type parityGraphFixture struct {
	nodes      map[string]*knowledgev1.Node
	edges      []*knowledgev1.Edge
	tombstoned map[string]bool
	// truncated makes the traversal arm answer with the response's Truncated
	// flag set, standing in for a server ceiling engaging mid-walk.
	truncated bool
}

func newParityFixture() *parityGraphFixture {
	return &parityGraphFixture{nodes: map[string]*knowledgev1.Node{}, tombstoned: map[string]bool{}}
}

func (f *parityGraphFixture) add(n *knowledgev1.Node) {
	f.nodes[n.Id] = n
}

func (f *parityGraphFixture) link(from, to string) {
	f.edges = append(f.edges, &knowledgev1.Edge{FromId: from, ToId: to, Type: string(kgtypes.EdgeKGContains)})
}

// tombstone marks a node id as tombstoned so the traversal arm drops edges
// whose peer is tombstoned (mirroring the server's unconditional
// edgeFilteredByTombstone) and excludes the node from the descendant set.
func (f *parityGraphFixture) tombstone(id string) {
	f.tombstoned[id] = true
}

// gc returns a GraphCaller backed by the fixture.
func (f *parityGraphFixture) gc() GraphCaller { return &parityCaller{f: f} }

type parityCaller struct{ f *parityGraphFixture }

func (g *parityCaller) Call(_ context.Context, tool string, args json.RawMessage) (kgtools.ToolResult, error) {
	if tool != "query" {
		return kgtools.ErrorResult("parityCaller: unsupported tool " + tool), nil
	}
	var req struct {
		ID           string `json:"id"`
		IncludeEdges bool   `json:"include_edges"`
	}
	_ = json.Unmarshal(args, &req)
	if req.ID == "" {
		return kgtools.ErrorResult("parityCaller: empty id"), nil
	}
	if req.IncludeEdges {
		return g.renderEdges(req.ID), nil
	}
	n, ok := g.f.nodes[req.ID]
	if !ok {
		return kgtools.ErrorResult("not found"), nil
	}
	return renderNodeWireJSON(n), nil
}

// Execute satisfies render.Executor — the carrier seam the repointed
// render.FetchNode / IterEdges use. Answers ByID /
// RETURN_MODE_EDGES from the fixture as nodes_json / edges_json carriers.
func (g *parityCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_TRAVERSAL {
		return g.answerTraversal(q), nil
	}
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		// node-SET form (Ids[]) → union of each pivot's edges filtered by
		// Selection.EdgeTypes. Forward=true selects OUTGOING only (the
		// depends-on batch); an UNSET Forward is the both-direction union the
		// server's node-SET carrier returns, which is what a paged pivot drain
		// over a single node sends. The per-node ById form keeps the legacy
		// union for any direct caller.
		if ids := q.GetIds(); len(ids) > 0 {
			return &knowledgev1.ExecuteResponse{
				Edges: g.nodeSetEdges(ids, q.GetSelection().GetEdgeTypes(), q.GetForward()),
			}, nil
		}
		var out []*knowledgev1.Edge
		for i := range g.f.edges {
			e := g.f.edges[i]
			if e.FromId == q.GetById() || e.ToId == q.GetById() {
				out = append(out, &knowledgev1.Edge{
					FromId:        e.FromId,
					ToId:          e.ToId,
					Type:          e.Type,
					Weight:        e.Weight,
					Confidence:    e.Confidence,
					Method:        e.Method,
					Evidence:      e.Evidence,
					LastValidated: e.LastValidated,
				})
			}
		}
		return &knowledgev1.ExecuteResponse{Edges: out}, nil
	}
	if n, ok := g.f.nodes[q.GetById()]; ok {
		resp := enginetest.ResponseWithNodes([]*knowledgev1.Node{n}...)
		return resp, nil
	}
	return &knowledgev1.ExecuteResponse{}, nil
}

// answerTraversal computes the contains-descendant set of the root (BFS up to
// MaxHops) and, when IncludeEdgeMetadata is set, the contains edges among that
// set — mirroring the server's traversal + CollectEdgesAlongWalk. It DROPS any
// edge whose child peer is tombstoned and excludes tombstoned nodes from the
// descendant set, replicating the server's unconditional edgeFilteredByTombstone
// (OutgoingEdges) so a tombstoned child never reaches the index.
func (g *parityCaller) answerTraversal(q *knowledgev1.QueryPlan) *knowledgev1.ExecuteResponse {
	root := q.GetSelection().GetFromId()[0]
	maxHops := int(q.GetMaxHops())
	if maxHops <= 0 {
		maxHops = 1 << 30
	}
	childrenOf := map[string][]string{}
	for _, e := range g.f.edges {
		if e.Type == string(kgtypes.EdgeKGContains) {
			childrenOf[e.FromId] = append(childrenOf[e.FromId], e.ToId)
		}
	}
	var results []engine.TraversalResult
	var containsEdges []knowledgev1.Edge
	visited := map[string]bool{root: true}
	type frontier struct {
		id   string
		dist int
	}
	queue := []frontier{{root, 0}}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.dist >= maxHops {
			continue
		}
		for _, childID := range childrenOf[cur.id] {
			if g.f.tombstoned[childID] {
				continue // edgeFilteredByTombstone: peer tombstoned → edge dropped
			}
			containsEdges = append(containsEdges, knowledgev1.Edge{
				FromId: cur.id, ToId: childID, Type: string(kgtypes.EdgeKGContains),
			})
			if visited[childID] {
				continue
			}
			visited[childID] = true
			if n, ok := g.f.nodes[childID]; ok {
				results = append(results, engine.TraversalResult{Node: n, Distance: cur.dist + 1})
			}
			queue = append(queue, frontier{childID, cur.dist + 1})
		}
	}
	resp := &knowledgev1.ExecuteResponse{
		TraversalResults: traversalResultsToProtoForTest(results),
		Truncated:        g.f.truncated,
	}
	if q.GetIncludeEdgeMetadata() {
		resp.TraversalEdges = edgePtrsForTest(containsEdges)
	}
	return resp
}

// nodeSetEdges unions each pivot's OUTGOING edges (Forward=&true semantics)
// filtered to the requested edge types.
func (g *parityCaller) nodeSetEdges(ids, edgeTypes []string, outgoingOnly bool) []*knowledgev1.Edge {
	want := map[string]bool{}
	for _, et := range edgeTypes {
		want[et] = true
	}
	pivots := map[string]bool{}
	for _, id := range ids {
		pivots[id] = true
	}
	var out []*knowledgev1.Edge
	for i := range g.f.edges {
		e := g.f.edges[i]
		incident := pivots[e.FromId] || (!outgoingOnly && pivots[e.ToId])
		if !incident {
			continue
		}
		if len(want) > 0 && !want[e.Type] {
			continue
		}
		out = append(out, &knowledgev1.Edge{FromId: e.FromId, ToId: e.ToId, Type: e.Type})
	}
	return out
}

func renderNodeWireJSON(n *knowledgev1.Node) kgtools.ToolResult {
	// CreatedAt/UpdatedAt are int64 unix-nanos on the value-embed node; map 0 →
	// the zero time.Time so the omitzero JSON tag drops unset timestamps.
	nanosToTime := func(nanos int64) time.Time {
		if nanos == 0 {
			return time.Time{}
		}
		return time.Unix(0, nanos)
	}
	payload := struct {
		ID          string            `json:"id"`
		Type        string            `json:"type"`
		SymbolName  string            `json:"symbol_name"`
		Description string            `json:"description"`
		Summary     string            `json:"summary"`
		Content     string            `json:"content"`
		Status      string            `json:"status"`
		Keywords    string            `json:"keywords"`
		Source      string            `json:"source"`
		Metadata    map[string]string `json:"metadata"`
		CreatedAt   time.Time         `json:"created_at,omitzero"`
		UpdatedAt   time.Time         `json:"updated_at,omitzero"`
	}{
		ID: n.Id, Type: n.Type, SymbolName: n.SymbolName,
		Description: n.Description, Summary: n.Summary, Content: n.Content,
		Status: n.Status, Keywords: n.Keywords, Source: n.Source,
		Metadata:  n.Metadata,
		CreatedAt: nanosToTime(n.CreatedAt), UpdatedAt: nanosToTime(n.UpdatedAt),
	}
	b, _ := json.Marshal(payload) //nolint:errchkjson // typed struct, cannot fail
	return kgtools.TextResult(string(b))
}

func (g *parityCaller) renderEdges(nodeID string) kgtools.ToolResult {
	type row struct {
		PeerID       string `json:"peer_id"`
		Relationship string `json:"relationship"`
		Direction    string `json:"direction"`
	}
	var rows []row
	for i := range g.f.edges {
		e := g.f.edges[i]
		if e.FromId == nodeID {
			rows = append(rows, row{PeerID: e.ToId, Relationship: e.Type, Direction: "outgoing"})
		}
		if e.ToId == nodeID {
			rows = append(rows, row{PeerID: e.FromId, Relationship: e.Type, Direction: "incoming"})
		}
	}
	// Stable ordering for deterministic test output (the wire layer's
	// natural order is store-internal and not part of the contract).
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Direction != rows[j].Direction {
			return rows[i].Direction < rows[j].Direction
		}
		return rows[i].PeerID < rows[j].PeerID
	})
	body, _ := json.Marshal(struct { //nolint:errchkjson // typed struct, cannot fail
		Edges []row `json:"edges"`
	}{Edges: rows})
	return kgtools.TextResult(string(body))
}

// parityDeps reuses logE2EDeps (intercept_logs_e2e_test.go) — a
// minimal ClientDeps that exposes only GraphCaller. All other
// accessors return nil/empty; the plan-tree intercept does not
// exercise them. Defined as a type alias here so the per-test
// constructor stays local.
type parityDeps = logE2EDeps

// seedPlanTreeFixture builds the same plan→2 phases × 3 steps shape
// the goldengen capture used. The IDs use a deterministic prefix so
// scrubAll's 32-char hex regex matches them.
func seedPlanTreeFixture(f *parityGraphFixture) string {
	// Deterministic 32-char hex IDs (so scrubbing replaces them).
	planID := "00000000000000000000000000000001"
	f.add(&knowledgev1.Node{Id: planID, Type: string(kgtypes.NodePlan), SymbolName: "capture-plan",
		Status: "active", Description: "plan goal"})

	phase1 := "00000000000000000000000000000010"
	phase2 := "00000000000000000000000000000020"
	f.add(&knowledgev1.Node{Id: phase1, Type: string(kgtypes.NodePhase), SymbolName: "phase-1",
		Status: "pending", Description: "p1 over"})
	f.add(&knowledgev1.Node{Id: phase2, Type: string(kgtypes.NodePhase), SymbolName: "phase-2",
		Status: "pending", Description: "p2 over"})
	f.link(planID, phase1)
	f.link(planID, phase2)

	// 3 steps per phase.
	stepNames1 := []string{"step-1a", "step-1b", "step-1c"}
	stepNames2 := []string{"step-2a", "step-2b", "step-2c"}
	for i, name := range stepNames1 {
		id := "0000000000000000000000000000010" + string(rune('0'+i))
		f.add(&knowledgev1.Node{Id: id, Type: string(kgtypes.NodeStep), SymbolName: name,
			Status: "pending", Description: "desc 1" + string(rune('a'+i))})
		f.link(phase1, id)
	}
	for i, name := range stepNames2 {
		id := "0000000000000000000000000000020" + string(rune('0'+i))
		f.add(&knowledgev1.Node{Id: id, Type: string(kgtypes.NodeStep), SymbolName: name,
			Status: "pending", Description: "desc 2" + string(rune('a'+i))})
		f.link(phase2, id)
	}
	return planID
}

func TestInterceptQueryPlanTree_TextFormat_ByteIdentical_ToGolden(t *testing.T) {
	f := newParityFixture()
	planID := seedPlanTreeFixture(f)
	deps := &parityDeps{gc: f.gc()}

	args, err := json.Marshal(map[string]any{"mode": "plan_tree", "id": planID})
	require.NoError(t, err)

	handled, res := InterceptQueryPlanTree(opCtx(), deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	require.True(t, handled)
	require.False(t, res.IsError, "intercept produced error: %v", res.Content)

	got := scrubForParity(extractText(res))
	want := readGolden(t, "plan_tree")
	assert.Equal(t, want, got, "plan_tree text output diverged from golden")
}

func TestInterceptQueryPlanTree_JSONFormat_ByteIdentical_ToGolden(t *testing.T) {
	f := newParityFixture()
	planID := seedPlanTreeFixture(f)
	deps := &parityDeps{gc: f.gc()}

	args, err := json.Marshal(map[string]any{"mode": "plan_tree", "id": planID, "format": "json"})
	require.NoError(t, err)

	handled, res := InterceptQueryPlanTree(opCtx(), deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	require.True(t, handled)
	require.False(t, res.IsError, "intercept produced error: %v", res.Content)

	got := scrubForParity(extractText(res))
	want := readGolden(t, "plan_tree.json")
	assert.Equal(t, want, got, "plan_tree.json output diverged from golden")
}

// TestInterceptQueryPlanTree_TombstonedChild_DroppedFromBothPaths is the
// fails-when-absent test for tombstone parity: a tombstoned child must render in
// NEITHER the text nor the json output. The parityCaller traversal arm drops
// edges whose peer is tombstoned (mirroring the server's unconditional
// edgeFilteredByTombstone), so a regression in BuildChildIndex or the traversal
// that let a tombstoned peer through would surface the child here and fail.
func TestInterceptQueryPlanTree_TombstonedChild_DroppedFromBothPaths(t *testing.T) {
	f := newParityFixture()
	planID := seedPlanTreeFixture(f)

	// Add one extra step under phase-1 and tombstone it.
	const phase1 = "00000000000000000000000000000010"
	const tombStep = "00000000000000000000000000000099"
	f.add(&knowledgev1.Node{Id: tombStep, Type: string(kgtypes.NodeStep), SymbolName: "tombstoned-step",
		Status: "pending", Description: "should never render"})
	f.link(phase1, tombStep)
	f.tombstone(tombStep)

	deps := &parityDeps{gc: f.gc()}

	textArgs, err := json.Marshal(map[string]any{"mode": "plan_tree", "id": planID})
	require.NoError(t, err)
	_, textRes := InterceptQueryPlanTree(opCtx(), deps, kgtools.CallToolParams{Name: "query", Arguments: textArgs})
	require.False(t, textRes.IsError)
	require.NotContains(t, extractText(textRes), "tombstoned-step", "tombstoned child must not render in text")

	jsonArgs, err := json.Marshal(map[string]any{"mode": "plan_tree", "id": planID, "format": "json"})
	require.NoError(t, err)
	_, jsonRes := InterceptQueryPlanTree(opCtx(), deps, kgtools.CallToolParams{Name: "query", Arguments: jsonArgs})
	require.False(t, jsonRes.IsError)
	require.NotContains(t, extractText(jsonRes), "tombstoned-step", "tombstoned child must not render in json")
}

// TestBuildPlanTreeJSON_NoChildIndexEntry_OmitsChildrenKey pins the accepted
// post-fix contract: a node with no childIndex entry (its only structure edge
// pointed at a tombstoned/absent node, so nothing was indexed under it) yields a
// JSON row with NO "children" key — not an empty "children":[] array.
func TestBuildPlanTreeJSON_NoChildIndexEntry_OmitsChildrenKey(t *testing.T) {
	node := &knowledgev1.Node{Id: "leaf", Type: string(kgtypes.NodeStep), SymbolName: "leaf", Status: "pending"}
	// Empty index → no entry for "leaf".
	row := buildPlanTreeJSON(node, 0, 10, map[string][]*knowledgev1.Node{})

	_, hasChildren := row["children"]
	assert.False(t, hasChildren, "a node with no indexed children must omit the children key entirely")
	assert.Equal(t, "leaf", row["id"])
}

func TestInterceptQueryPlanTree_WrongTool_FallsThrough(t *testing.T) {
	deps := &parityDeps{gc: newParityFixture().gc()}
	args := mustMarshal(t, map[string]any{"mode": "plan_tree", "id": "anything"})
	handled, _ := InterceptQueryPlanTree(opCtx(), deps, kgtools.CallToolParams{Name: "search", Arguments: args})
	assert.False(t, handled, "wrong tool must fall through")
}

func TestInterceptQueryPlanTree_WrongMode_FallsThrough(t *testing.T) {
	deps := &parityDeps{gc: newParityFixture().gc()}
	args := mustMarshal(t, map[string]any{"mode": "examine", "id": "anything"})
	handled, _ := InterceptQueryPlanTree(opCtx(), deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	assert.False(t, handled, "wrong mode must fall through")
}

func TestInterceptQueryPlanTree_MissingID_Errors(t *testing.T) {
	deps := &parityDeps{gc: newParityFixture().gc()}
	args := mustMarshal(t, map[string]any{"mode": "plan_tree"})
	handled, res := InterceptQueryPlanTree(opCtx(), deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	require.True(t, handled)
	require.True(t, res.IsError)
	assert.Contains(t, extractText(res), "plan_tree mode requires 'id' parameter")
}

// (extractText lives in intercept_add_criterion.go and is reused
// by every parity test in this package.)
//
// The plan_tree read-time-provenance test lives in
// intercept_query_plan_tree_updated_at_test.go — split out to keep this
// file under the repo's hard 500-line commit gate.
