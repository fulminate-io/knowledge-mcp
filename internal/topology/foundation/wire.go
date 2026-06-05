// SPDX-License-Identifier: Apache-2.0

package foundation

import (
	"context"
	"encoding/json"
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/graphsel"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// wire.go holds the shared wire read-helpers the topology analyzer families
// reuse: every read is a single engine.Compile → caller.Execute → engine.Decode
// round-trip, so no family package re-implements the node-browse / bulk-edge /
// by-id / list-graphs / find-knowledge-findings reads. The bulk-edge helper
// (FetchEdges) is the N+1 guard — one Execute over the whole node-ID set rather
// than a per-node fan-out.
//
// All six helpers are authored here up-front (before the parallel analyzer
// waves) so the foundation package stays frozen once the waves consume it.

// scopePayload seeds a Compile query payload with the graph selector keys,
// routing the single instance name into the field the server resolver keys off
// per graph type (tools_graph_routing.go ResolveGraphDB): code graphs scope via
// repo, cloud/cicd via account, every other graph via name. An empty name
// leaves only the graph key (knowledge / linkage need no instance). This is the
// payload-key twin of graphTarget below; both must agree so the Compile-based
// and raw-plan helpers scope identically.
func scopePayload(graphType kgtypes.GraphType, name string) map[string]any {
	if name == "" {
		return map[string]any{"graph": string(graphType)}
	}
	return graphsel.ScopePayload(graphType, name, false)
}

// graphTarget builds the envelope GraphSelector for the raw-QueryPlan helpers
// (FetchAllNodes, FetchEdges) that bypass engine.Compile — those build a
// Match-all / RETURN_MODE_EDGES plan the LLM-facing Compile surface does not
// reduce, so they set the Target directly (mirroring dispatch_graphwide.go's
// raw nodes/edges plans). The instance name is routed into the same field the
// server resolver keys off per graph type as scopePayload above. Returns nil
// when there is nothing to scope (knowledge / linkage with no instance), which
// the server treats as the knowledge graph.
func graphTarget(graphType kgtypes.GraphType, name string) *knowledgev1.GraphSelector {
	if graphType == "" && name == "" {
		return nil
	}
	return graphsel.GraphSelectorFor(graphType, name, false)
}

// executeQuery compiles a query payload to an ExecuteRequest and runs it over
// caller. Returns a typed error when the args are not reducible (should not
// happen for the fixed internal shapes the helpers build) or the Execute fails.
func executeQuery(ctx context.Context, caller GraphCaller, payload map[string]any) (*knowledgev1.ExecuteResponse, error) {
	if caller == nil {
		return nil, fmt.Errorf("topology/wire: graph caller unavailable")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("topology/wire: marshal query payload: %w", err)
	}
	req, ok := engine.Compile("query", body)
	if !ok {
		return nil, fmt.Errorf("topology/wire: query not reducible to an ExecuteRequest")
	}
	resp, err := caller.Execute(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("topology/wire: execute query: %w", err)
	}
	return resp, nil
}

// FetchNodesByType returns every node of nodeType in the scoped (graphType,
// name) graph in ONE Execute: a plural-types browse ({graph, name/account,
// types:[nodeType], limit:0}) whose typed Nodes carrier (engine.DecodeNodes)
// carries the full node payloads. The legacy scoped.IterateAll(nodeType) reads
// (cloud / content analyzers) become this call.
func FetchNodesByType(ctx context.Context, caller GraphCaller, graphType kgtypes.GraphType, name string, nodeType kgtypes.NodeType) ([]*knowledgev1.Node, error) {
	payload := scopePayload(graphType, name)
	payload["types"] = []string{string(nodeType)}
	payload["limit"] = 0 // no cap — we want every node of the type
	resp, err := executeQuery(ctx, caller, payload)
	if err != nil {
		return nil, err
	}
	nodes, err := engine.DecodeNodes(resp)
	if err != nil {
		return nil, fmt.Errorf("topology/wire: decode nodes-by-type: %w", err)
	}
	return nodes, nil
}

// FetchAllNodes returns every node in the scoped (graphType, name) graph in ONE
// Execute. It builds a Match-all RETURN_MODE_NODES plan (empty Selection →
// Match(""), Limit 0 → no cap) directly with the envelope Target, rather than
// through engine.Compile: the LLM-facing Compile surface deliberately has no
// all-nodes (no id / type / meta) arm, so the all-nodes browse is expressed as
// a raw plan exactly as dispatch_graphwide.go's node-enumeration pass does. The
// gonum builder's node-materialize pass and the content family's full node walk
// both use this.
func FetchAllNodes(ctx context.Context, caller GraphCaller, graphType kgtypes.GraphType, name string) ([]*knowledgev1.Node, error) {
	if caller == nil {
		return nil, fmt.Errorf("topology/wire: graph caller unavailable")
	}
	plan := &knowledgev1.QueryPlan{Selection: &knowledgev1.Selection{}}
	resp, err := caller.Execute(ctx, &knowledgev1.ExecuteRequest{
		Plan:   &knowledgev1.ExecuteRequest_Query{Query: plan},
		Target: graphTarget(graphType, name),
	})
	if err != nil {
		return nil, fmt.Errorf("topology/wire: execute all-nodes: %w", err)
	}
	nodes, err := engine.DecodeNodes(resp)
	if err != nil {
		return nil, fmt.Errorf("topology/wire: decode all-nodes: %w", err)
	}
	return nodes, nil
}

// FetchNodeByID returns the single node with the given id in the scoped
// (graphType, name) graph and whether it was found, in ONE Execute: a by-id
// RETURN_MODE_NODES plan ({ById} + envelope Target) whose typed Nodes carrier
// carries the node. ok=false when the node is absent (or any decode hiccup
// leaves the carrier empty). The legacy scoped.Query(ByID) / ResolveNodeName
// reads (god_object, betweenness) become this call.
//
// It builds the raw plan directly rather than through engine.Compile because
// the LLM-facing Compile surface treats a code-graph by-id read as the
// SPECIALIZED analyze-node intercept (non-reducible); god_object reads
// code-graph nodes by id, so the by-id read is expressed as a raw plan that the
// engine executes against whatever graph the Target resolves.
func FetchNodeByID(ctx context.Context, caller GraphCaller, graphType kgtypes.GraphType, name, id string) (*knowledgev1.Node, bool, error) {
	if id == "" {
		return nil, false, nil
	}
	if caller == nil {
		return nil, false, fmt.Errorf("topology/wire: graph caller unavailable")
	}
	resp, err := caller.Execute(ctx, &knowledgev1.ExecuteRequest{
		Plan:   &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{ById: id}},
		Target: graphTarget(graphType, name),
	})
	if err != nil {
		return nil, false, fmt.Errorf("topology/wire: execute node-by-id: %w", err)
	}
	nodes, err := engine.DecodeNodes(resp)
	if err != nil {
		return nil, false, fmt.Errorf("topology/wire: decode node-by-id: %w", err)
	}
	for _, n := range nodes {
		if n != nil && n.Id == id {
			return n, true, nil
		}
	}
	return nil, false, nil
}

// FetchEdges returns every edge incident to ANY node in the ids set in the
// scoped (graphType, name) graph in ONE Execute: a node-SET RETURN_MODE_EDGES
// query (ids[] + both-direction union per the engine node-SET carrier),
// optionally filtered to edgeTypes, carrying the envelope Target so the read
// scopes to the analyzer's graph (not the default knowledge graph). This is the
// N+1 guard — one bulk edges read over the whole node set rather than N
// per-node traverses. Empty ids → no call, nil edges. The legacy per-node
// scoped.IterEdges reads (gonum edge-materialize, sampleNeighbors, CBO/RFC/WMC/
// FanIn) become per-direction filters over this single read.
func FetchEdges(ctx context.Context, caller GraphCaller, graphType kgtypes.GraphType, name string, ids []string, edgeTypes []kgtypes.EdgeType) ([]knowledgev1.Edge, error) {
	if caller == nil || len(ids) == 0 {
		return nil, nil
	}
	plan := &knowledgev1.QueryPlan{
		Ids:               ids,
		ReturnMode:        knowledgev1.ReturnMode_RETURN_MODE_EDGES,
		IncludeTombstones: true,
	}
	if len(edgeTypes) > 0 {
		ets := make([]string, len(edgeTypes))
		for i := range edgeTypes {
			ets[i] = string(edgeTypes[i])
		}
		plan.Selection = &knowledgev1.Selection{EdgeTypes: ets}
	}
	req := &knowledgev1.ExecuteRequest{
		Plan:   &knowledgev1.ExecuteRequest_Query{Query: plan},
		Target: graphTarget(graphType, name),
	}
	resp, err := caller.Execute(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("topology/wire: execute bulk edges: %w", err)
	}
	edges, err := engine.DecodeEdges(resp)
	if err != nil {
		return nil, fmt.Errorf("topology/wire: decode bulk edges: %w", err)
	}
	return edges, nil
}

// FetchGraphNames enumerates every loaded graph of graphType in ONE Execute: a
// query(mode:modules) compiled to RETURN_MODE_GRAPH_NAMES whose graph_names
// carrier decodes to []*knowledgev1.GraphInfo. The exposure family's
// ListGraphsLite(GraphCloud) read becomes this call. Returns the full
// []*GraphInfo (callers project Name themselves). It returns pointers (not a
// value slice) because GraphInfo embeds a proto message-state mutex — a value
// slice would trip copylocks.
func FetchGraphNames(ctx context.Context, caller GraphCaller, graphType kgtypes.GraphType) ([]*knowledgev1.GraphInfo, error) {
	resp, err := executeQuery(ctx, caller, map[string]any{
		"graph": string(graphType),
		"mode":  "modules",
	})
	if err != nil {
		return nil, err
	}
	infos, err := engine.DecodeGraphNames(resp)
	if err != nil {
		return nil, fmt.Errorf("topology/wire: decode graph-names: %w", err)
	}
	return infos, nil
}

// FetchKnowledgeFindings returns the topology findings in the knowledge graph
// whose algorithm + primary_evidence metadata match, in ONE Execute: a
// meta-filtered type=finding knowledge query ({graph:"knowledge",
// type:"finding", meta:{algorithm, primary_evidence}}) whose typed Nodes
// carrier carries the matching findings. The exposure family's
// iam_escalation-finding lookup (rootDB.Knowledge().Query(Match(NodeFinding).
// Meta("algorithm",…).Meta("primary_evidence",…))) becomes this call.
func FetchKnowledgeFindings(ctx context.Context, caller GraphCaller, algorithm, primaryEvidence string) ([]*knowledgev1.Node, error) {
	resp, err := executeQuery(ctx, caller, map[string]any{
		"graph": string(kgtypes.GraphKnowledge),
		"type":  string(kgtypes.NodeFinding),
		"meta": map[string]string{
			"algorithm":        algorithm,
			"primary_evidence": primaryEvidence,
		},
	})
	if err != nil {
		return nil, err
	}
	nodes, err := engine.DecodeNodes(resp)
	if err != nil {
		return nil, fmt.Errorf("topology/wire: decode knowledge-findings: %w", err)
	}
	return nodes, nil
}
