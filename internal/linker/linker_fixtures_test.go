// SPDX-License-Identifier: Apache-2.0

package linker

// linker_fixtures_test.go holds the package's shared test doubles.
//
// THEY LIVE IN A FILE OF THEIR OWN because they used to live in the image
// pass's test file, and the image pass was deleted: a fixture six test files
// share is not the property of whichever pass happened to introduce it, and
// filing it under one made deleting that pass take the whole package's test
// scaffolding with it. This is the test-side twin of the same move the
// production helpers made in helpers.go.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// recordedCall captures one gc.Call / gc.Execute invocation for assertion.
type recordedCall struct {
	Tool string
	Args map[string]any
}

// fakeGraphCaller is a scripted GraphCaller + linkerExecutor for sub-linker
// unit tests. The `respond` function returns the mock response per call (keyed
// on the OLD tool+args envelope shape); `calls` records every call for
// assertion. The reads ride the Execute carrier seam: Execute
// reconstructs the (tool, args) shape from the compiled ExecuteRequest, invokes
// `respond`, and re-shapes the returned `{graphs}` / `{nodes}` envelope into the
// carrier fields (graph_names_json / nodes_json) the engine decode reads. The
// mutate(link) emit still rides Call (emitLink stays raw — link_graph proxy).
type fakeGraphCaller struct {
	respond func(tool string, args map[string]any) (kgtools.ToolResult, error)
	calls   []recordedCall

	// nodesByGraph seeds by-id resolution for crossgraph.ResolveAndLink's endpoint
	// proxy materialization: graph INSTANCE key → nodeID → node. A by-id
	// FetchNodeIn against (graph, id) returns the seeded node so the linkage proxy
	// builds. Empty → the id resolves nowhere → best-effort raw id (linkage path).
	//
	// THE KEY IS THE INSTANCE, NOT THE FAMILY, and the difference is load-bearing
	// for any fixture holding MORE THAN ONE graph of a family. The endpoint locator
	// probes each enumerated graph by id and takes the FIRST that resolves, so a
	// family-keyed map answers YES for every graph in the family and the proxy's
	// graph-name component becomes whichever name the enumeration happened to yield
	// first — map order, in a test that reads as deterministic. seedCodeNode keeps
	// the family-wide spelling under the WILDCARD instance for the single-graph
	// fixtures; seedCodeNodeIn declares residency in one named graph.
	nodesByGraph map[string]map[string]*knowledgev1.Node

	// capturedLinks records every MUTATION_KIND_LINK ExecuteRequest the crossgraph
	// composer issues (emitLink now composes the linkage edge client-side over the
	// Execute seam rather than a raw mutate(link) Call). Each entry is the
	// (target-graph, from, to, relationship, edge-metadata) of the composed edge.
	capturedLinks []capturedLink

	// edgesByType is the linkage graph's edge VOCABULARY, read through the
	// StatsFn seam before an edge type is declared. An empty map is the
	// BOOTSTRAP case — a linkage graph holding no edges yet — which is what
	// every linker test here drives: the pass declares the graph's first
	// BUILDS/DEPLOYS, and a write with no matching stored family admits the
	// caller's spelling.
	edgesByType map[string]int64

	// statsCalls counts vocabulary reads across one linker pass. It is not
	// decoration: it is the ONLY observable the per-pass cost gate has. A cache
	// read once per PASS and the per-edge N+1 it replaces emit byte-identical
	// edges, so nothing in the captured links can tell them apart.
	statsCalls int
}

// capturedLink is one composed LINK ExecuteRequest's salient fields.
type capturedLink struct {
	TargetGraph   string
	FromID        string
	ToID          string
	Relationship  string
	Method        string
	Confidence    float64
	LastValidated int64
}

func (f *fakeGraphCaller) Call(_ context.Context, tool string, rawArgs json.RawMessage) (kgtools.ToolResult, error) {
	var args map[string]any
	_ = json.Unmarshal(rawArgs, &args)
	f.calls = append(f.calls, recordedCall{Tool: tool, Args: args})
	if f.respond == nil {
		return kgtools.ToolResult{Content: []kgtools.ContentBlock{{Type: "text", Text: `{}`}}}, nil
	}
	return f.respond(tool, args)
}

// Stats serves the linkage graph's edge vocabulary through the StatsFn seam the
// per-pass vocabulary cache consumes, counting every read.
//
// The COUNT is the point. A nil edgesByType answers an empty vocabulary, which
// is the bootstrap case a write admits — so the emitted edges are the same
// whether the vocabulary is read once or once per edge, and only statsCalls
// separates the two.
func (f *fakeGraphCaller) Stats(_ context.Context, _ *knowledgev1.StatsRequest) (*knowledgev1.StatsResponse, error) {
	f.statsCalls++
	return &knowledgev1.StatsResponse{
		GraphStats: &knowledgev1.GraphStats{EdgesByType: f.edgesByType},
	}, nil
}

// codeInstanceKeyAny is the WILDCARD instance key: a node seeded under it
// resolves in EVERY code graph the fixture enumerates. It is what seedCodeNode
// uses, and it is correct only for a fixture holding one code graph.
const codeInstanceKeyAny = "code/*"

// codeInstanceKey is the per-graph residency key a by-id probe is answered from.
func codeInstanceKey(repo string) string { return "code/" + repo }

// seedCodeNode registers a node for by-id resolution in EVERY code graph (used by
// the crossgraph endpoint proxy materialization).
//
// THE GRAPH TYPE IS FIXED RATHER THAN A PARAMETER because the code graph is the
// only one the surviving linker fixtures seed. It took a graphType argument while
// the account-keyed and log endpoints existed; with those gone every caller passed
// "code", and a parameter with one possible value states a generality the fixture
// does not have. The map stays keyed by instance so a second family costs a
// parameter again rather than a rewrite.
//
// USE seedCodeNodeIn WHEN THE FIXTURE HOLDS MORE THAN ONE CODE GRAPH. This
// wildcard makes the node resolve in all of them, which is what turns the
// endpoint locator's first-hit into map order.
func (f *fakeGraphCaller) seedCodeNode(n *knowledgev1.Node) {
	f.seedNodeUnder(codeInstanceKeyAny, n)
}

// seedCodeNodeIn registers a node as resident in ONE named code graph, so a by-id
// probe against any other graph misses exactly as it would in production.
func (f *fakeGraphCaller) seedCodeNodeIn(repo string, n *knowledgev1.Node) {
	f.seedNodeUnder(codeInstanceKey(repo), n)
}

func (f *fakeGraphCaller) seedNodeUnder(key string, n *knowledgev1.Node) {
	if f.nodesByGraph == nil {
		f.nodesByGraph = map[string]map[string]*knowledgev1.Node{}
	}
	if f.nodesByGraph[key] == nil {
		f.nodesByGraph[key] = map[string]*knowledgev1.Node{}
	}
	f.nodesByGraph[key][n.Id] = n
}

// Execute bridges the Execute carrier seam to the test's `respond` callback.
// It reconstructs the (tool="query", args) shape the responders expect from the
// compiled QueryPlan + GraphSelector, then translates the returned envelope into
// the engine carrier fields the linker decode helpers read.
func (f *fakeGraphCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	if m := req.GetMutation(); m != nil {
		return f.execMutation(m, req.GetTarget())
	}
	q := req.GetQuery()
	if q == nil {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	// By-id resolution (render.FetchNodeIn): the crossgraph composer probes each
	// graph for a node by id to decide knowledge-vs-foreign-vs-raw. Serve it from
	// the seeded nodesByGraph; an unseeded (graph,id) returns an empty node set.
	if id := q.GetById(); id != "" {
		gt := req.GetTarget().GetGraph()
		if gt == "" {
			gt = "knowledge"
		}
		// THE NAMED INSTANCE IS TRIED FIRST AND THE WILDCARD SECOND. A fixture
		// declaring residency in one graph must MISS in the others — that miss is
		// what makes the endpoint locator's first-hit deterministic rather than a
		// function of map order — while a single-graph fixture that seeded no name
		// still resolves through the wildcard.
		var nodes []*knowledgev1.Node
		for _, key := range []string{gt + "/" + req.GetTarget().GetRepo(), gt + "/*"} {
			if n, ok := f.nodesByGraph[key][id]; ok {
				nodes = []*knowledgev1.Node{n}
				break
			}
		}
		return enginetest.ResponseWithNodes(nodes...), nil
	}
	args := map[string]any{}
	if g := req.GetTarget().GetGraph(); g != "" {
		args["graph"] = g
	}
	if r := req.GetTarget().GetRepo(); r != "" {
		args["repo"] = r
	}
	if n := req.GetTarget().GetName(); n != "" {
		args["name"] = n
	}
	if nt := q.GetSelection().GetNodeType(); nt != "" {
		args["type"] = nt
	}
	isModules := q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES
	if isModules {
		args["mode"] = "modules"
	}
	f.calls = append(f.calls, recordedCall{Tool: "query", Args: args})
	if f.respond == nil {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	res, err := f.respond("query", args)
	if err != nil {
		return nil, err
	}
	body := resultText(res)
	if isModules {
		return graphNamesResponse(body)
	}
	return nodesResponse(body)
}

// execMutation handles the crossgraph composer's MUTATION_KIND_UPSERT (proxy
// materialization — no-op, returns the upserted id) + MUTATION_KIND_LINK (the
// composed linkage edge — captured for assertion).
func (f *fakeGraphCaller) execMutation(m *knowledgev1.MutationPlan, target *knowledgev1.GraphSelector) (*knowledgev1.ExecuteResponse, error) {
	switch m.GetKind() {
	case knowledgev1.MutationPlan_MUTATION_KIND_UPSERT:
		ids := make([]string, 0, len(m.GetNodeBodies()))
		for _, b := range m.GetNodeBodies() {
			ids = append(ids, b.GetId())
		}
		return &knowledgev1.ExecuteResponse{Ids: ids}, nil
	case knowledgev1.MutationPlan_MUTATION_KIND_LINK:
		spec := m.GetEdgeSpec()
		var from string
		if ids := m.GetSelection().GetIds(); len(ids) > 0 {
			from = ids[0]
		}
		f.capturedLinks = append(f.capturedLinks, capturedLink{
			TargetGraph:   target.GetGraph(),
			FromID:        from,
			ToID:          spec.GetToId(),
			Relationship:  spec.GetRelationship(),
			Method:        spec.GetMethod(),
			Confidence:    spec.GetConfidence(),
			LastValidated: spec.GetLastValidated(),
		})
		return &knowledgev1.ExecuteResponse{AffectedCount: 1}, nil
	default:
		return &knowledgev1.ExecuteResponse{}, nil
	}
}

// graphNamesResponse re-shapes the test responder's {graphs:[...]} envelope
// into the typed GraphNames carrier ([]*knowledgev1.GraphInfo) the engine decode reads.
func graphNamesResponse(body string) (*knowledgev1.ExecuteResponse, error) {
	var env struct {
		Graphs []string `json:"graphs"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		return nil, err
	}
	return &knowledgev1.ExecuteResponse{GraphNames: graphNamesToProto(env.Graphs)}, nil
}

// nodesResponse re-shapes the test responder's {nodes:[...]} envelope into the
// typed Nodes carrier ([]*knowledgev1.Node) the engine decode reads.
func nodesResponse(body string) (*knowledgev1.ExecuteResponse, error) {
	var env struct {
		Nodes []*knowledgev1.Node `json:"nodes"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		return nil, err
	}
	return enginetest.ResponseWithNodes(env.Nodes...), nil
}

// jsonResult builds a kgtools.ToolResult whose textual body is the given
// JSON-marshallable value. Used by mock GraphCaller responders.
func jsonResult(t *testing.T, v any) kgtools.ToolResult {
	t.Helper()
	body, err := json.Marshal(v)
	require.NoError(t, err)
	return kgtools.ToolResult{Content: []kgtools.ContentBlock{{Type: "text", Text: string(body)}}}
}

// TestExtractImageName covers the regex-light parser ported from the
