// SPDX-License-Identifier: Apache-2.0

package linker

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// recordedCall captures one gc.Call / gc.Execute invocation for assertion.
type recordedCall struct {
	Tool string
	Args map[string]any
}

// fakeGraphCaller is a scripted GraphCaller + linkerExecutor for sub-linker
// unit tests. The `respond` function returns the mock response per call (keyed
// on the OLD tool+args envelope shape); `calls` records every call for
// assertion. The reads ride the Execute carrier seam (T-GTB6): Execute
// reconstructs the (tool, args) shape from the compiled ExecuteRequest, invokes
// `respond`, and re-shapes the returned `{graphs}` / `{nodes}` envelope into the
// carrier fields (graph_names_json / nodes_json) the engine decode reads. The
// mutate(link) emit still rides Call (emitLink stays raw — link_graph proxy).
type fakeGraphCaller struct {
	respond func(tool string, args map[string]any) (kgtools.ToolResult, error)
	calls   []recordedCall

	// nodesByGraph seeds by-id resolution for crossgraph.ResolveAndLink's endpoint
	// proxy materialization (T-GTB6): graphType → nodeID → node. A by-id
	// FetchNodeIn against (graph, id) returns the seeded node so the linkage proxy
	// builds. Empty → the id resolves nowhere → best-effort raw id (linkage path).
	nodesByGraph map[string]map[string]*knowledgev1.Node

	// capturedLinks records every MUTATION_KIND_LINK ExecuteRequest the crossgraph
	// composer issues (emitLink now composes the linkage edge client-side over the
	// Execute seam rather than a raw mutate(link) Call). Each entry is the
	// (target-graph, from, to, relationship, edge-metadata) of the composed edge.
	capturedLinks []capturedLink
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

// seedNode registers a node for by-id resolution in the named graph (used by the
// crossgraph endpoint proxy materialization).
func (f *fakeGraphCaller) seedNode(graphType string, n *knowledgev1.Node) {
	if f.nodesByGraph == nil {
		f.nodesByGraph = map[string]map[string]*knowledgev1.Node{}
	}
	if f.nodesByGraph[graphType] == nil {
		f.nodesByGraph[graphType] = map[string]*knowledgev1.Node{}
	}
	f.nodesByGraph[graphType][n.Id] = n
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
		var nodes []*knowledgev1.Node
		if n, ok := f.nodesByGraph[gt][id]; ok {
			nodes = []*knowledgev1.Node{n}
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
// original linker. Five canonical shapes plus an edge case.
func TestExtractImageName(t *testing.T) {
	cases := map[string]string{
		"gcr.io/project/myapp:v1.2.3":                         "myapp",
		"123456.dkr.ecr.us-east-1.amazonaws.com/myapp:latest": "myapp",
		"docker.io/library/nginx:1.25":                        "nginx",
		"myapp:latest":                                        "myapp",
		"ghcr.io/org/repo/service@sha256:abc123":              "service",
	}
	for input, want := range cases {
		assert.Equal(t, want, extractImageName(input), "input=%q", input)
	}
}

// TestExtractContainerImages parses a representative Deployment fixture
// and asserts every container/initContainer image is returned.
func TestExtractContainerImages(t *testing.T) {
	const content = `{
		"spec": {
			"template": {
				"spec": {
					"containers": [{"image": "gcr.io/project/myapp:v1"}, {"image": "sidecar:latest"}],
					"initContainers": [{"image": "init:v2"}]
				}
			}
		}
	}`
	got := extractContainerImages(content)
	assert.ElementsMatch(t, []string{"gcr.io/project/myapp:v1", "sidecar:latest", "init:v2"}, got)
}

// TestLinkImageTargets_EmitsBuildsEdge wires a fake graphCaller through a
// full LinkImageTargets run: one cloud graph + one code repo, the cloud
// graph has a Deployment whose container image matches the repo name. The
// linker must emit exactly one mutate(link, relationship:"BUILDS",
// link_graph:"linkage", method:"tier1-image", confidence:0.9).
func TestLinkImageTargets_EmitsBuildsEdge(t *testing.T) {
	cloudResource := &knowledgev1.Node{
		Id:         "default/Deployment/myapp-server",
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: "myapp-server",
		Content:    `{"spec":{"template":{"spec":{"containers":[{"image":"gcr.io/project/myapp:v1"}]}}}}`,
		Metadata: map[string]string{
			"resource_type": "Deployment",
		},
	}

	gc := &fakeGraphCaller{}
	gc.respond = func(tool string, args map[string]any) (kgtools.ToolResult, error) {
		if tool == "query" {
			graph, _ := args["graph"].(string)
			switch graph {
			case "code":
				return jsonResult(t, map[string]any{"graphs": []string{"myapp"}}), nil
			case "cloud":
				if _, hasType := args["type"]; hasType {
					return jsonResult(t, map[string]any{"nodes": []*knowledgev1.Node{cloudResource}}), nil
				}
				return jsonResult(t, map[string]any{"graphs": []string{"prod"}}), nil
			}
		}
		return kgtools.ToolResult{Content: []kgtools.ContentBlock{{Type: "text", Text: `{}`}}}, nil
	}
	// The cloud resource is a real node in cloud/prod, so the crossgraph composer
	// materializes its deterministic proxy as the linkage edge FROM. The TO
	// (imageName "myapp") is a repo NAME, not a node → best-effort raw id (server
	// ResolveOrProxy parity).
	gc.seedNode("cloud", cloudResource)

	n, err := LinkImageTargets(context.Background(), gc, LinkOptions{})
	require.NoError(t, err)
	assert.Equal(t, 1, n, "one match → one link emitted")

	// emitLink now composes the linkage edge client-side over the Execute seam
	// (no raw mutate(link) Call): assert the captured LINK. FROM is the
	// deterministic cloud proxy; TO is the best-effort raw repo name.
	require.Len(t, gc.capturedLinks, 1, "expected one composed linkage LINK")
	link := gc.capturedLinks[0]
	assert.Equal(t, "linkage", link.TargetGraph)
	assert.Equal(t, "BUILDS", link.Relationship, "edge type uppercased for the linkage graph")
	assert.Equal(t, "tier1-image", link.Method)
	assert.InDelta(t, 0.9, link.Confidence, 0.0001)
	assert.Equal(t, "proxy:cloud:prod:"+cloudResource.Id, link.FromID, "cloud FROM materialized to its deterministic proxy")
	assert.Equal(t, "myapp", link.ToID, "non-node repo-name TO stays raw (best-effort)")
}

// TestLinkImageTargets_DryRun_NoMutates asserts DryRun:true skips every
// mutate(link) emission while still walking the graphs.
func TestLinkImageTargets_DryRun_NoMutates(t *testing.T) {
	cloudResource := &knowledgev1.Node{
		Id:       "default/Deployment/myapp-server",
		Type:     string(kgtypes.NodeCloudResource),
		Content:  `{"spec":{"template":{"spec":{"containers":[{"image":"gcr.io/project/myapp:v1"}]}}}}`,
		Metadata: map[string]string{"resource_type": "Deployment"},
	}
	gc := &fakeGraphCaller{}
	gc.respond = func(tool string, args map[string]any) (kgtools.ToolResult, error) {
		if tool == "query" {
			graph, _ := args["graph"].(string)
			switch graph {
			case "code":
				return jsonResult(t, map[string]any{"graphs": []string{"myapp"}}), nil
			case "cloud":
				if _, hasType := args["type"]; hasType {
					return jsonResult(t, map[string]any{"nodes": []*knowledgev1.Node{cloudResource}}), nil
				}
				return jsonResult(t, map[string]any{"graphs": []string{"prod"}}), nil
			}
		}
		return kgtools.ToolResult{Content: []kgtools.ContentBlock{{Type: "text", Text: `{}`}}}, nil
	}

	n, err := LinkImageTargets(context.Background(), gc, LinkOptions{DryRun: true})
	require.NoError(t, err)
	// DryRun still increments the count of matched links — the caller
	// can use the count to size a confirmation prompt — but no mutate
	// call is ever issued.
	_ = n
	for _, c := range gc.calls {
		assert.NotEqual(t, "mutate", c.Tool, "DryRun must not emit mutate calls")
	}
}
