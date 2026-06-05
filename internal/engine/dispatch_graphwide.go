// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"context"
	"encoding/json"
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// dispatch_graphwide.go holds the client-side composition of the
// graph-wide-edges traverse (a start-less traverse). A single compile→one-Execute
// cannot express the two-step "enumerate every node, then union their edges", so
// — like dispatchQueryByID composes include_edges — this routes through a
// pre-Compile composer in the Dispatch seam: a Match-all node enumeration + the
// existing RETURN_MODE_EDGES ids[]→union carrier (NO new server arm). Split into
// a sibling file so dispatch.go stays under the 500-line cap.

// dispatchGraphWideEdges intercepts a start-less traverse (traverse with no
// Start) — the graph-wide-edges fast path the legacy handleTraverseGraphWideEdges
// served. It composes the result client-side and returns handled=true. For a
// from_id walk (Start set) or a logs traverse (rendered by the client intercept),
// it returns handled=false so Dispatch proceeds to the generic Compile/exec flow.
//
// BOUNDEDNESS: a CONSTANT number of Execute calls regardless of node/edge
// cardinality — (1) one Match-all RETURN_MODE_NODES enumeration, (2) one
// RETURN_MODE_EDGES ids[]→union read (the proto field-7 carrier: "A node-SET
// (ids[]) returns the union of every node's edges in ONE call"). Two Executes.
func dispatchGraphWideEdges(ctx context.Context, exec ExecuteFn, args json.RawMessage) (kgtools.ToolResult, bool) {
	var a traverseArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return kgtools.ToolResult{}, false // malformed → let the generic flow surface it.
	}
	if a.Start != "" || a.Graph == "logs" {
		return kgtools.ToolResult{}, false // from_id walk / logs intercept → not graph-wide.
	}
	target := buildTarget(a.Graph, a.Repo, a.Account, a.Name, a.Language, a.Branch)

	// (1) Match-all RETURN_MODE_NODES enumeration — every node in the resolved
	// graph (Selection empty → Match(""), Limit 0 → no cap). Mirrors the server's
	// Match("").All().Limit(0) (tools_traverse_graphwide.go:52).
	nodesPlan := &knowledgev1.QueryPlan{Selection: &knowledgev1.Selection{}}
	applyTombstones(nodesPlan, a.IncludeTombstones)
	nodesResp, err := exec(ctx, &knowledgev1.ExecuteRequest{
		Plan:   &knowledgev1.ExecuteRequest_Query{Query: nodesPlan},
		Target: target,
	})
	if err != nil {
		return renderEngineError(err), true
	}
	nodes, derr := DecodeNodes(nodesResp)
	if derr != nil {
		return errorResult("graph-wide node enumeration decode: " + derr.Error()), true
	}

	// (2) RETURN_MODE_EDGES ids[]→union read over every node id. Forward=true
	// (OutgoingEdges) so each edge is yielded once from its source — matching the
	// server collectGraphWideEdges dedup (tools_traverse_graphwide.go:69-85).
	// edge_types ride AS-GIVEN, canonicalized client-side.
	ids := make([]string, len(nodes))
	for i, n := range nodes {
		ids[i] = n.Id
	}
	edges, err := graphWideEdgeUnion(ctx, exec, ids, a.EdgeTypes, a.Graph, target)
	if err != nil {
		return renderEngineError(err), true
	}

	return renderGraphWideEdges(a, nodes, edges), true
}

// graphWideEdgeUnion issues ONE RETURN_MODE_EDGES read over the node-id set and
// returns the union of every node's outgoing edges. An empty id set is a no-op
// (no Execute, no edges). edge_types are canonicalized per-graph client-side.
func graphWideEdgeUnion(ctx context.Context, exec ExecuteFn, ids, edgeTypes []string, graph string, target *knowledgev1.GraphSelector) ([]knowledgev1.Edge, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	fwd := true
	sel := &knowledgev1.Selection{}
	if len(edgeTypes) > 0 {
		sel.EdgeTypes = canonicalEdgeCasings(graph, edgeTypes)
	}
	edgesPlan := &knowledgev1.QueryPlan{
		Ids:        ids,
		Selection:  sel,
		Forward:    &fwd,
		ReturnMode: knowledgev1.ReturnMode_RETURN_MODE_EDGES,
	}
	resp, err := exec(ctx, &knowledgev1.ExecuteRequest{
		Plan:   &knowledgev1.ExecuteRequest_Query{Query: edgesPlan},
		Target: target,
	})
	if err != nil {
		return nil, err
	}
	return DecodeEdges(resp)
}

// renderGraphWideEdges reproduces the server renderGraphWideJSON/Text
// (tools_traverse_graphwide.go:91-135) client-side — like render_misc.go
// reproduces the server browse renderers. The engine returns the DATA (nodes +
// edges); this file renders the {start, graph, direction, nodes, edges} envelope.
func renderGraphWideEdges(a traverseArgs, nodes []*knowledgev1.Node, edges []knowledgev1.Edge) kgtools.ToolResult {
	if a.Format == "json" {
		return renderGraphWideJSON(a, nodes, edges)
	}
	return renderGraphWideText(a, nodes, edges)
}

// renderGraphWideJSON emits the structured {start,graph,direction,nodes,edges}
// payload (mirrors the server renderGraphWideJSON). Every node carries
// id/name/type/status/description; every edge carries source/target/relationship
// and, when include_edge_metadata is set, an edge_metadata sub-object.
func renderGraphWideJSON(a traverseArgs, nodes []*knowledgev1.Node, edges []knowledgev1.Edge) kgtools.ToolResult {
	nodeRows := make([]graphWideNode, 0, len(nodes))
	for _, n := range nodes {
		nodeRows = append(nodeRows, graphWideNode{
			ID: n.Id, Name: n.SymbolName,
			Type: n.Type, Status: n.Status,
			Description: n.Description,
		})
	}
	edgeRows := make([]map[string]any, 0, len(edges))
	for i := range edges {
		e := &edges[i]
		row := map[string]any{
			"source":       e.FromId,
			"target":       e.ToId,
			"relationship": e.Type,
		}
		if a.IncludeEdgeMetadata {
			if meta := graphWideEdgeMetadata(e); len(meta) > 0 {
				row["edge_metadata"] = meta
			}
		}
		edgeRows = append(edgeRows, row)
	}
	return jsonResult(map[string]any{
		"start":     "",
		"graph":     graphWideLabel(a.Graph),
		"direction": a.Direction,
		"nodes":     nodeRows,
		"edges":     edgeRows,
	})
}

// renderGraphWideText is the human-readable fallback (mirrors the server
// renderGraphWideText): a tiny node/edge count summary.
func renderGraphWideText(a traverseArgs, nodes []*knowledgev1.Node, edges []knowledgev1.Edge) kgtools.ToolResult {
	body := fmt.Sprintf("## Graph-wide traversal (graph=%s, name=%s)\n\n", graphWideLabel(a.Graph), a.Name)
	body += fmt.Sprintf("- nodes: %d\n- edges: %d\n", len(nodes), len(edges))
	if len(a.EdgeTypes) > 0 {
		body += fmt.Sprintf("- edge_types: %v\n", a.EdgeTypes)
	}
	return kgtools.TextResult(body)
}

// graphWideNode is the JSON node row (mirrors the server traverseNode,
// tools_traverse_generic.go:28).
type graphWideNode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Status      string `json:"status,omitempty"`
	Description string `json:"description,omitempty"`
}

// graphWideLabel is the render-header graph label (mirrors the server graphLabel,
// tools_traverse_generic.go:250): empty graph → "knowledge".
func graphWideLabel(graph string) string {
	if graph == "" {
		return "knowledge"
	}
	return graph
}

// graphWideEdgeMetadata builds the {weight,confidence,method,evidence,
// last_validated} map for the JSON formatter (mirrors the server edgeMetadataMap,
// tools_traverse_edge_metadata.go:202). Zero/empty fields are elided.
func graphWideEdgeMetadata(e *knowledgev1.Edge) map[string]any {
	m := map[string]any{}
	if e.Weight != 0 {
		m["weight"] = e.Weight
	}
	if e.Confidence != 0 {
		m["confidence"] = e.Confidence
	}
	if e.Method != "" {
		m["method"] = e.Method
	}
	if e.Evidence != "" {
		m["evidence"] = e.Evidence
	}
	if !nanosToTime(e.LastValidated).IsZero() {
		m["last_validated"] = nanosToTime(e.LastValidated).UTC().Format("2006-01-02T15:04:05Z")
	}
	return m
}
