// SPDX-License-Identifier: Apache-2.0

package tools

// plan_tree_section_order_test.go covers the FOUR BuildChildIndex consumers that
// live in this package, one test each, per what-to-test R1-e.
//
// WHY ONE TEST PER CONSUMER RATHER THAN ONE FOR THE SORT. The sort itself is
// pinned in projects/render. What these assert is that each consumer actually
// TAKES its child order from the index — and they do not all get there the same
// way. Two of the four never call a tree renderer at all:
//
//   plan_tree TEXT        → RenderTreeFromIndex, after a depends-on topo-sort
//   plan_tree JSON        → buildPlanTreeJSON, which fetches NO depends-on and
//                           walks childIndex directly
//   plan_tree FIELDS      → the same json branch, under a projection
//   create_plan's tree    → renderCreatePlanText → AssembleSubtree
//
// THE TWO JSON PATHS ARE LOAD-BEARING FOR THE ORDERING SETTLEMENT, not two more
// rows: they are the reason the sort lives in BuildChildIndex rather than at the
// AssembleSubtree layer. A change that greens the text path and reds either json
// path has taken the alternative the settlement rejected.

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// sectionOrderFixture seeds a plan whose three children ARRIVE in one order and
// carry positions in another, so arrival order and intended order disagree.
//
// THE DISAGREEMENT IS THE POINT. A fixture whose children arrive already in
// position order cannot tell a working sort from no sort at all: both render the
// same bytes. Here the arrival order is [two, zero, one] and the positions are
// [2, 0, 1], so only a reader that consults the position produces [zero, one,
// two].
//
// THE CHILDREN ARE TYPED AS PHASES, NOT AS PLAN SECTIONS, and that is deliberate
// twice over. The sort ranks by POSITION and never consults the node type, so
// typing them as sections would assert a narrower property than the one that
// holds — and it would make this whole file fail to COMPILE against the tree
// before the vocabulary edit, leaving these four consumers with a build error
// for a red leg instead of the assertion failure each of them actually produces.
func sectionOrderFixture() *parityGraphFixture {
	f := newParityFixture()
	f.add(&knowledgev1.Node{Id: "plan-1", Type: string(kgtypes.NodePlan), SymbolName: "chunked", Status: kgtypes.StatusActive})
	for _, s := range []struct {
		id, name string
		pos      int
	}{
		{"sec-two", "Section two", 2},
		{"sec-zero", "Section zero", 0},
		{"sec-one", "Section one", 1},
	} {
		n := &knowledgev1.Node{
			Id: s.id, Type: string(kgtypes.NodePhase), SymbolName: s.name,
			Description: "body of " + s.name, Status: kgtypes.StatusActive,
		}
		kgtypes.SetValue(n, "position", strconv.Itoa(s.pos))
		f.add(n)
		f.linkPositioned("plan-1", s.id, s.pos)
	}
	return f
}

// linkPositioned adds a containment edge carrying a zero-based POSITION on its
// Evidence, the shape the plan section builder and both raw collectors stamp.
//
// It exists because sibling ordering cannot be exercised through `link`: an
// unpositioned edge set is ordered by arrival, so an assertion taken over one
// passes against a renderer that ignores position entirely.
func (f *parityGraphFixture) linkPositioned(from, to string, position int) {
	f.edges = append(f.edges, &knowledgev1.Edge{
		FromId:   from,
		ToId:     to,
		Type:     string(kgtypes.EdgeKGContains),
		Method:   "plan-section",
		Evidence: `{"position":"` + strconv.Itoa(position) + `"}`,
	})
}

// wantSectionOrder is the order every consumer below must produce.
var wantSectionOrder = []string{"Section zero", "Section one", "Section two"}

// namesInOrder returns the wanted names in the order they first appear in body,
// so an assertion reads as an ORDER rather than as a set of Contains checks.
func namesInOrder(body string, want []string) []string {
	type hit struct {
		name string
		at   int
	}
	var hits []hit
	for _, n := range want {
		if at := strings.Index(body, n); at >= 0 {
			hits = append(hits, hit{n, at})
		}
	}
	for i := 1; i < len(hits); i++ {
		for j := i; j > 0 && hits[j].at < hits[j-1].at; j-- {
			hits[j], hits[j-1] = hits[j-1], hits[j]
		}
	}
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.name)
	}
	return out
}

func planTreeCall(t *testing.T, f *parityGraphFixture, args string) kgtools.ToolResult {
	t.Helper()
	handled, res := InterceptQueryPlanTree(context.Background(), interceptTestDeps{gc: f.gc()},
		kgtools.CallToolParams{Name: "query", Arguments: json.RawMessage(args)})
	require.True(t, handled, "plan_tree is claimed client-side")
	require.False(t, res.IsError, "%s", toolResultText(res))
	return res
}

// R1-e consumer 1: query(mode:"plan_tree") TEXT. RED-FIRST — before the sort the
// sections rendered in arrival order.
func TestInterceptQueryPlanTree_TextOrdersSectionsByPosition(t *testing.T) {
	body := toolResultText(planTreeCall(t, sectionOrderFixture(), `{"mode":"plan_tree","id":"plan-1"}`))
	assert.Equal(t, wantSectionOrder, namesInOrder(body, wantSectionOrder))
}

// R1-e consumer 2: query(mode:"plan_tree", format:"json"). This branch fetches NO
// depends-on edges at all, so its order comes from childIndex ALONE — which is
// exactly why the sort cannot live in a tree renderer.
func TestInterceptQueryPlanTree_JSONOrdersSectionsByPosition(t *testing.T) {
	res := planTreeCall(t, sectionOrderFixture(), `{"mode":"plan_tree","id":"plan-1","format":"json"}`)
	var payload struct {
		Children []struct {
			Name string `json:"name"`
		} `json:"children"`
	}
	require.NoError(t, json.Unmarshal([]byte(toolResultText(res)), &payload))
	got := make([]string, 0, len(payload.Children))
	for _, c := range payload.Children {
		got = append(got, c.Name)
	}
	assert.Equal(t, wantSectionOrder, got,
		"the json branch takes its order from childIndex with no topo-sort")
}

// R1-e consumer 3: query(mode:"plan_tree", fields:[...]) — the same json branch
// under a projection. Asserted separately because a projection selects the json
// render whatever `format` says, so this arm is reachable without format:"json"
// and a fix applied only to the format branch would miss it.
func TestInterceptQueryPlanTree_FieldsOrdersSectionsByPosition(t *testing.T) {
	res := planTreeCall(t, sectionOrderFixture(), `{"mode":"plan_tree","id":"plan-1","fields":["id","name"]}`)
	var payload struct {
		Children []struct {
			Name string `json:"name"`
		} `json:"children"`
	}
	require.NoError(t, json.Unmarshal([]byte(toolResultText(res)), &payload))
	got := make([]string, 0, len(payload.Children))
	for _, c := range payload.Children {
		got = append(got, c.Name)
	}
	assert.Equal(t, wantSectionOrder, got)
}

// createPlanTreeCaller answers create_plan's write with a fixed id set and every
// subsequent READ from a parity fixture, so the tree create_plan renders back to
// its caller is a real AssembleSubtree over a real positioned edge set.
//
// The two halves are independent by construction: the write's id list is what
// PersistBatch returns, and the read fixture is seeded separately. That is what
// lets this test assert the RENDER without also re-asserting the builder, which
// projects/builders_section_test.go already pins.
type createPlanTreeCaller struct {
	f *parityGraphFixture
}

func (c *createPlanTreeCaller) Call(ctx context.Context, tool string, args json.RawMessage) (kgtools.ToolResult, error) {
	if tool == "mutate" {
		return kgtools.TextResult(`{"ids":["plan-1","sec-two","sec-zero","sec-one"]}`), nil
	}
	return (&parityCaller{f: c.f}).Call(ctx, tool, args)
}

func (c *createPlanTreeCaller) Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	if req.GetMutation() != nil {
		return &knowledgev1.ExecuteResponse{
			Ids:           []string{"plan-1", "sec-two", "sec-zero", "sec-one"},
			AffectedCount: 4,
		}, nil
	}
	return (&parityCaller{f: c.f}).Execute(ctx, req)
}

// R1-e consumer 6: create_plan's OWN returned tree, rendered by
// renderCreatePlanText through AssembleSubtree. This is the first thing a planner
// sees after writing a plan, so a plan whose sections render in arrival order
// here reads as written wrong even when it was written right.
func TestInterceptCreatePlan_ReturnedTreeOrdersSectionsByPosition(t *testing.T) {
	gc := &createPlanTreeCaller{f: sectionOrderFixture()}
	handled, res := InterceptCreatePlan(opCtx(), interceptTestDeps{gc: gc}, kgtools.CallToolParams{
		Name: "create_plan",
		Arguments: json.RawMessage(`{
			"name":"chunked","goal":"g","summary":"s","no_patterns_reason":"x",
			"sections":[
				{"name":"Section two","body":"body of Section two","summary":"s2","position":2},
				{"name":"Section zero","body":"body of Section zero","summary":"s0","position":0},
				{"name":"Section one","body":"body of Section one","summary":"s1","position":1}
			]
		}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "%s", toolResultText(res))
	body := toolResultText(res)
	assert.Equal(t, wantSectionOrder, namesInOrder(body, wantSectionOrder),
		"the tree create_plan returns is the planner's first read of what it just wrote")
}
