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
	nodes map[string]*knowledgev1.Node
	edges []*knowledgev1.Edge
}

func newParityFixture() *parityGraphFixture {
	return &parityGraphFixture{nodes: map[string]*knowledgev1.Node{}}
}

func (f *parityGraphFixture) add(n *knowledgev1.Node) {
	f.nodes[n.Id] = n
}

func (f *parityGraphFixture) link(from, to string) {
	f.edges = append(f.edges, &knowledgev1.Edge{FromId: from, ToId: to, Type: string(kgtypes.EdgeKGContains)})
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
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
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

	handled, res := InterceptQueryPlanTree(deps, kgtools.CallToolParams{Name: "query", Arguments: args})
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

	handled, res := InterceptQueryPlanTree(deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	require.True(t, handled)
	require.False(t, res.IsError, "intercept produced error: %v", res.Content)

	got := scrubForParity(extractText(res))
	want := readGolden(t, "plan_tree.json")
	assert.Equal(t, want, got, "plan_tree.json output diverged from golden")
}

func TestInterceptQueryPlanTree_WrongTool_FallsThrough(t *testing.T) {
	deps := &parityDeps{gc: newParityFixture().gc()}
	args := mustMarshal(t, map[string]any{"mode": "plan_tree", "id": "anything"})
	handled, _ := InterceptQueryPlanTree(deps, kgtools.CallToolParams{Name: "search", Arguments: args})
	assert.False(t, handled, "wrong tool must fall through")
}

func TestInterceptQueryPlanTree_WrongMode_FallsThrough(t *testing.T) {
	deps := &parityDeps{gc: newParityFixture().gc()}
	args := mustMarshal(t, map[string]any{"mode": "examine", "id": "anything"})
	handled, _ := InterceptQueryPlanTree(deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	assert.False(t, handled, "wrong mode must fall through")
}

func TestInterceptQueryPlanTree_MissingID_Errors(t *testing.T) {
	deps := &parityDeps{gc: newParityFixture().gc()}
	args := mustMarshal(t, map[string]any{"mode": "plan_tree"})
	handled, res := InterceptQueryPlanTree(deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	require.True(t, handled)
	require.True(t, res.IsError)
	assert.Contains(t, extractText(res), "plan_tree mode requires 'id' parameter")
}

// (extractText lives in intercept_add_criterion.go and is reused
// by every parity test in this package.)
