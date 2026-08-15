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
	"github.com/fulminate-io/knowledge-mcp/internal/paging"
)

// wire.go holds the shared wire read-helpers the topology analyzer families
// reuse: every read goes through the same engine.Compile → caller.Execute →
// engine.Decode path, so no family package re-implements the node-browse /
// bulk-edge / by-id / list-graphs / find-knowledge-findings reads. The bulk-edge
// helper (FetchEdges) is the N+1 guard — a bulk paged read over the whole
// node-ID set rather than a per-node fan-out.
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
// name) graph, drained in bounded id-keyset pages ({graph, name/account, type:
// nodeType, limit: BrowsePageSize, after_id, skip_total}) whose typed Nodes
// carrier (engine.DecodeNodes) carries the full node payloads. The legacy
// scoped.IterateAll(nodeType) reads (cloud / content analyzers) become this call.
//
// The SINGULAR type key is load-bearing, not a spelling preference. It takes the
// compiler's Selection.NodeType arm, which pushes the type filter into the INDEX
// SELECTION so the filtering happens before any cap by construction; the plural
// types key is a post-filter applied AFTER the cap, which silently returned zero
// nodes on any graph holding more than a page of other types that sort first. It
// is also the only compile arm that threads after_id, so the drain needs it.
//
// Results arrive in id-ASCENDING order (after_id presence pins the ordering on
// both backends), NOT in the backend's default order as before — consumers must
// not depend on the previous ordering.
func FetchNodesByType(ctx context.Context, caller GraphCaller, graphType kgtypes.GraphType, name string, nodeType kgtypes.NodeType) ([]*knowledgev1.Node, error) {
	return paging.DrainKeysetPages(func(afterID string) ([]*knowledgev1.Node, error) {
		payload := scopePayload(graphType, name)
		payload["type"] = string(nodeType)
		payload["limit"] = paging.BrowsePageSize
		// The key is present on EVERY page including the first, where the value is
		// the empty string: presence, not emptiness, selects the keyset browse. An
		// omitted key leaves after_id unset and the backend pages in its own default
		// order, so the cursor taken from page 1 skips every lower id.
		payload["after_id"] = afterID
		payload["skip_total"] = true // the drain discards Total
		resp, err := executeQuery(ctx, caller, payload)
		if err != nil {
			return nil, err
		}
		nodes, derr := engine.DecodeNodes(resp)
		if derr != nil {
			return nil, fmt.Errorf("topology/wire: decode nodes-by-type: %w", derr)
		}
		return nodes, nil
	}, paging.BrowsePageSize)
}

// FetchAllNodes returns every node in the scoped (graphType, name) graph, read
// in BOUNDED keyset pages. It builds a Match-all RETURN_MODE_NODES plan (empty
// Selection → Match("")) directly with the envelope Target, rather than through
// engine.Compile: the LLM-facing Compile surface deliberately has no all-nodes
// (no id / type / meta) arm, so the all-nodes browse is expressed as a raw plan
// exactly as dispatch_graphwide.go's node-enumeration pass does. The gonum
// builder's node-materialize pass and the content family's full node walk both
// use this.
//
// It used to be ONE Execute carrying Limit 0, which the server read as "no cap".
// That is retired: an uncapped browse costs the whole node table on demand, and
// no user-reachable read may be unbounded. Each page now carries an explicit
// positive Limit and the drain advances a keyset cursor.
//
// Results arrive in id-ASCENDING order (after_id presence pins the ordering on
// both backends), NOT in the backend's default order as before — consumers must
// not depend on the previous ordering.
func FetchAllNodes(ctx context.Context, caller GraphCaller, graphType kgtypes.GraphType, name string) ([]*knowledgev1.Node, error) {
	if caller == nil {
		return nil, fmt.Errorf("topology/wire: graph caller unavailable")
	}
	return paging.DrainKeysetPages(func(afterID string) ([]*knowledgev1.Node, error) {
		cursor := afterID
		plan := &knowledgev1.QueryPlan{
			Selection: &knowledgev1.Selection{},
			Limit:     int32(paging.BrowsePageSize),
			// SET on EVERY page including the first, where the value is empty:
			// presence, not emptiness, selects the keyset browse. An omitted field
			// leaves the backend paging in its own default order, so the cursor
			// taken from page 1 would skip every lower id.
			AfterId:   &cursor,
			SkipTotal: true, // the drain discards Total
		}
		resp, err := caller.Execute(ctx, &knowledgev1.ExecuteRequest{
			Plan:   &knowledgev1.ExecuteRequest_Query{Query: plan},
			Target: graphTarget(graphType, name),
		})
		if err != nil {
			return nil, fmt.Errorf("topology/wire: execute all-nodes: %w", err)
		}
		nodes, derr := engine.DecodeNodes(resp)
		if derr != nil {
			return nil, fmt.Errorf("topology/wire: decode all-nodes: %w", derr)
		}
		return nodes, nil
	}, paging.BrowsePageSize)
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
// scoped (graphType, name) graph: a node-SET RETURN_MODE_EDGES query (ids[] +
// both-direction union per the engine node-SET carrier), optionally filtered to
// edgeTypes, carrying the envelope Target so the read scopes to the analyzer's
// graph (not the default knowledge graph). This is the N+1 guard — a bulk edges
// read over the whole node set rather than N per-node traverses. Empty ids → no
// call, nil edges. The legacy per-node scoped.IterEdges reads (gonum
// edge-materialize, sampleNeighbors, CBO/RFC/WMC/FanIn) become per-direction
// filters over this read.
//
// The id set is chunked into bounded pivot pages and deduped into one union by
// paging.DrainPivotEdges, so a set larger than one page costs
// ceil(len(ids)/paging.EdgePivotPageSize) sequential round trips rather than
// one. A SATURATED SINGLE PIVOT ABORTS: if one node alone fills a ceiling page
// the drain cannot split further and errors naming it, because an analyzer
// consuming a silently short edge set emits confidently WRONG rankings rather
// than degrading visibly.
func FetchEdges(ctx context.Context, caller GraphCaller, graphType kgtypes.GraphType, name string, ids []string, edgeTypes []kgtypes.EdgeType) ([]knowledgev1.Edge, error) {
	if caller == nil || len(ids) == 0 {
		return nil, nil
	}
	// The plan Limit and the drain's edgeCap are the same number twice on
	// purpose: the Limit is what the server enforces, the cap is what the drain
	// uses to notice it was enforced. One without the other yields a drain that
	// never detects truncation, or one that splits on a threshold nobody applies.
	edges, err := paging.DrainPivotEdges(ids, paging.EdgePivotPageSize, engine.CorrelationsEdgeScanCap,
		func(idPage []string) ([]knowledgev1.Edge, error) {
			plan := &knowledgev1.QueryPlan{
				Ids:               idPage,
				ReturnMode:        knowledgev1.ReturnMode_RETURN_MODE_EDGES,
				IncludeTombstones: true,
				Limit:             int32(engine.CorrelationsEdgeScanCap),
			}
			if len(edgeTypes) > 0 {
				ets := make([]string, len(edgeTypes))
				for i := range edgeTypes {
					ets[i] = string(edgeTypes[i])
				}
				plan.Selection = &knowledgev1.Selection{EdgeTypes: ets}
			}
			resp, rerr := caller.Execute(ctx, &knowledgev1.ExecuteRequest{
				Plan:   &knowledgev1.ExecuteRequest_Query{Query: plan},
				Target: graphTarget(graphType, name),
			})
			if rerr != nil {
				return nil, fmt.Errorf("topology/wire: execute bulk edges: %w", rerr)
			}
			page, derr := engine.DecodeEdges(resp)
			if derr != nil {
				return nil, fmt.Errorf("topology/wire: decode bulk edges: %w", derr)
			}
			return page, nil
		})
	if err != nil {
		return nil, err
	}
	return edges, nil
}

// FetchAllEdges returns every edge incident to the ids set in the scoped
// (graphType, name) graph, optionally filtered to edgeTypes. It is the
// WHOLE-GRAPH shape of FetchEdges, for callers whose node set is the entire
// graph, and it reads that set in BOUNDED PIVOT PAGES: ids are chunked into
// pages of paging.EdgePivotPageSize, each page carries an explicit positive
// Limit, and the pages are deduped into one union by paging.DrainPivotEdges.
//
// The read used to be a single match-all request carrying NO pivot — faster
// (measured ~23ms against ~1.29s for the equivalent pivot read at ~157k edges)
// and unbounded by construction, which is exactly why it is retired: a request
// whose cost scales with the whole edge table is a denial-of-service surface,
// and no user-supplied read may be uncapped. The latency is traded deliberately.
//
// TWO CONSEQUENCES CALLERS MUST KNOW.
//
// The RPC count is now ceil(len(ids)/paging.EdgePivotPageSize) sequential round
// trips rather than one — on a ~100k-node graph that is ~200 per whole-graph
// edge load, paid by every topology analyzer that materializes a graph. Do NOT
// parallelize the pages: they share the drain's dedup map, and the server is the
// shared resource this bound exists to protect.
//
// A FULLY-DANGLING edge — one whose BOTH endpoints have been hard-deleted — is
// no longer returned, because a vanished endpoint can never appear in the pivot
// set. The match-all read surfaced those; nothing that consumes this function
// can map them to nodes anyway.
//
// A SATURATED SINGLE PIVOT ABORTS, and callers must propagate the error rather
// than degrade. If one node alone returns a full ceiling page, the drain cannot
// split further and returns an error naming it. This function feeds whole-graph
// analyzers — centrality, community detection, degree histograms, structural
// motifs — which consume an edge SET and emit rankings; a silently short edge
// set does not degrade those gracefully, it produces confidently WRONG rankings
// indistinguishable from right ones. Degrading with a notice was rejected
// because the intermediate helpers carry no channel for one, and because the
// findings those analyzers emit describe the GRAPH rather than the read that
// produced it, so a notice would attach to the wrong subject.
//
// Use FetchEdges, not this, whenever the node set is a genuine SUBSET: it pages
// the same way but over the caller's set rather than the whole graph, so a set
// small enough to send at once still costs a single round trip.
func FetchAllEdges(ctx context.Context, caller GraphCaller, graphType kgtypes.GraphType, name string, ids []string, edgeTypes []kgtypes.EdgeType) ([]knowledgev1.Edge, error) {
	if caller == nil {
		return nil, nil
	}
	// The plan Limit and the drain's edgeCap are the same number twice on
	// purpose: the Limit is what the server enforces, the cap is what the drain
	// uses to notice it was enforced. One without the other yields a drain that
	// never detects truncation, or one that splits on a threshold nobody applies.
	edges, err := paging.DrainPivotEdges(ids, paging.EdgePivotPageSize, engine.CorrelationsEdgeScanCap,
		func(idPage []string) ([]knowledgev1.Edge, error) {
			plan := &knowledgev1.QueryPlan{
				Ids:               idPage,
				ReturnMode:        knowledgev1.ReturnMode_RETURN_MODE_EDGES,
				IncludeTombstones: true,
				Limit:             int32(engine.CorrelationsEdgeScanCap),
			}
			if len(edgeTypes) > 0 {
				ets := make([]string, len(edgeTypes))
				for i := range edgeTypes {
					ets[i] = string(edgeTypes[i])
				}
				plan.Selection = &knowledgev1.Selection{EdgeTypes: ets}
			}
			resp, rerr := caller.Execute(ctx, &knowledgev1.ExecuteRequest{
				Plan:   &knowledgev1.ExecuteRequest_Query{Query: plan},
				Target: graphTarget(graphType, name),
			})
			if rerr != nil {
				return nil, fmt.Errorf("topology/wire: execute all edges: %w", rerr)
			}
			page, derr := engine.DecodeEdges(resp)
			if derr != nil {
				return nil, fmt.Errorf("topology/wire: decode all edges: %w", derr)
			}
			return page, nil
		})
	if err != nil {
		return nil, fmt.Errorf("topology/wire: all edges %s/%s: %w", graphType, name, err)
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
