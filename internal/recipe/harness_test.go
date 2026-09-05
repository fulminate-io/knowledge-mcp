// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// harness_test.go holds the shared RunRecipe test double and the recipe bodies
// the package's tests drive through it.
//
// IT WAS SPLIT OUT OF run_recipe_integration_test.go when the SAVED-recipe path
// was retired. That file's own subject — loading a recipe node out of the
// transformers graph — no longer exists, but the harness it carried is what
// every surviving test still runs on, so the harness moved here rather than
// going down with the subject.
//
// NOTHING HERE SEEDS A RECIPE BUCKET. Every caller below serves the SOURCE graph
// alone and every run supplies its body inline, which is the only shape RunRecipe
// accepts now.

// routingCaller is a foundation.GraphCaller that routes each Execute to a
// per-graph-type node/edge set, so a full RunRecipe (source graph read, and any
// further read an interpretation issues) can be driven against fakes only —
// NEVER a store. It records mutation plans so a test can assert that a refused
// run issued none.
//
// THE PER-GRAPH-TYPE ROUTING IS ITSELF UNDER TEST, not incidental scaffolding: a
// read aimed at the wrong graph would still find rows and still look like it
// worked, so collapsing this dispatch to a single set has to be observable.
//
// The routing key is graph TYPE alone, not the instance name, which suffices
// while every test here reads a web or pdf source. A test needing two graphs of
// the SAME type must widen the key first.
type routingCaller struct {
	nodesByGraph map[string][]*knowledgev1.Node
	edgesByGraph map[string][]*knowledgev1.Edge

	mutations []*knowledgev1.MutationPlan

	// calls counts every Execute, so a test can assert a read's cost is
	// page-bounded rather than one round trip per row.
	calls int
	// truncate stamps the response's Truncated flag the way a bounded server
	// page does, so a truncation path is reachable in test.
	truncate bool
}

func (c *routingCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	c.calls++
	if m := req.GetMutation(); m != nil {
		c.mutations = append(c.mutations, m)
		return &knowledgev1.ExecuteResponse{}, nil
	}
	g := req.GetTarget().GetGraph()
	q := req.GetQuery()
	resp := &knowledgev1.ExecuteResponse{Truncated: c.truncate}
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		resp.Edges = c.edgesByGraph[g]
		return resp, nil
	}
	// THE TOMBSTONE GATE, modeling the server's by-ids read: a tombstoned row is
	// served ONLY when the plan asks for tombstones. Without it a fake handing
	// back its seeded rows unconditionally could not tell a reader that asks for
	// tombstones from one that does not.
	for _, n := range c.nodesByGraph[g] {
		if n.GetTombstonedAt() != 0 && !q.GetIncludeTombstones() {
			continue
		}
		resp.Nodes = append(resp.Nodes, n)
	}
	return resp, nil
}

const fullRecipeBody = `select section
emit pattern {
    type := "pattern"
    name := section.symbol_name
} as $p
traverse relates-to out as $t
emit pattern {
    type := "pattern"
    name := $t.symbol_name
} as $rp
link $p --[relates-to]--> $rp`

const simpleEmitRecipeBody = `select section
emit pattern {
    type := "pattern"
    name := section.symbol_name
}`

// fullRecipeCaller serves the SOURCE graph only. The recipe body rides the run
// options, so there is no recipe node to seed.
func fullRecipeCaller() *routingCaller {
	return &routingCaller{
		nodesByGraph: map[string][]*knowledgev1.Node{
			string(kgtypes.GraphWebRaw): {
				{Id: "s1", Type: "section", SymbolName: "Message Router"},
				{Id: "s2", Type: "section", SymbolName: "Message Channel"},
			},
		},
		edgesByGraph: map[string][]*knowledgev1.Edge{
			string(kgtypes.GraphWebRaw): {{FromId: "s1", ToId: "s2", Type: "relates-to"}},
		},
	}
}

// section builds a minimal source row.
func section(id, name string) *knowledgev1.Node {
	return &knowledgev1.Node{Id: id, Type: "section", SymbolName: name}
}

// sourceOnlyCaller serves the supplied rows as the web source graph and nothing
// else — the shape every inline run needs.
func sourceOnlyCaller(rows ...*knowledgev1.Node) *routingCaller {
	return &routingCaller{
		nodesByGraph: map[string][]*knowledgev1.Node{string(kgtypes.GraphWebRaw): rows},
		edgesByGraph: map[string][]*knowledgev1.Edge{},
	}
}

// inlineOpts builds the run options for an inline extract over the eip fixture
// slug. Extract is always true: it is the only mode an admitted run has.
func inlineOpts(body string) Options {
	return Options{
		SourceManifest: FormatSourceManifest("hohpe-eip", "inline"),
		Body:           body,
		Extract:        true,
	}
}

// runInline drives RunRecipe over a source-only caller with an inline body.
//
// The nil sink is not a shortcut: an extract never writes, so the parameter is
// unreachable from here. It goes away entirely when the write path does.
func runInline(t *testing.T, caller *routingCaller, body string) (*Result, error) {
	t.Helper()
	return RunRecipe(context.Background(), caller, "src-graph", kgtypes.GraphWebRaw, inlineOpts(body))
}
