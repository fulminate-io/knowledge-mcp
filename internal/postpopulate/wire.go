// SPDX-License-Identifier: Apache-2.0

package postpopulate

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// nanosToTimePP converts an int64 unix-nanos value (the value-embed proto
// Edge.LastValidated representation — decision f21640fb) to a time.Time for the
// kgwire.BatchEdge.LastValidated field (still a time.Time), mapping 0 → zero time.
func nanosToTimePP(nanos int64) time.Time {
	if nanos == 0 {
		return time.Time{}
	}
	return time.Unix(0, nanos)
}

// GraphCaller is the narrow Execute-only wire seam every PostPopulate hook now
// uses to read + write the knowledge MCP wire. It mirrors linker.GraphCaller
// (linker/client.go:30) and tools.GraphCaller (deps.go:61) WITHOUT importing
// either package — keeping postpopulate cycle-free (tools imports collectors,
// collectors register postpopulate hooks, so postpopulate must not import
// tools). The production graphClientCaller satisfies this naturally.
//
// PostPopulate hooks NEVER hold an in-process store engine — the client has no
// store DB (project 1ce7d7aa, "client operates zero store engine"). Every
// graph read/write rides this seam: a query/mutate compiled via engine.Compile
// then run through Execute, exactly like the linker's read helpers
// (linker/helpers.go) and the pipeline's rpc helpers (pipeline/rpc.go).
type GraphCaller interface {
	Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error)
}

// selectorArgs returns the (graph, name-field) selector key/value pair the
// server's ResolveGraphDB (tools_graph_routing.go) expects for graphType:
// code routes by repo, cloud/cicd by account, everything else by name. This is
// the SAME translation the proven pipeline/rpc.go fetchNodes (L151-160) +
// writeBatchUpdates (L288-297) switch performs. Routing a cloud/cicd write via
// name: would land Account-less and the server rejects it ("graph=cloud
// requires account") — the FUL-288 silent-write regression these helpers exist
// to prevent. The returned map is merged into the query/mutate args.
func selectorArgs(gt kgtypes.GraphType, graphName string) map[string]any {
	args := map[string]any{"graph": string(gt)}
	switch gt {
	case kgtypes.GraphCode:
		args["repo"] = graphName
	case kgtypes.GraphCloud, kgtypes.GraphCICD:
		args["account"] = graphName
	default:
		if graphName != "" && graphName != "default" {
			args["name"] = graphName
		}
	}
	return args
}

// BrowseNodes reads nodes from a named per-account/per-repo graph via the
// Execute carrier seam: a type-browse (and/or metadata-filtered) query compiled
// to a QueryPlan, run through Execute, decoded from the nodes_json carrier.
// extra carries the query-shape filters every hook needs (type / meta /
// ids / limit) — it is merged onto the selectorArgs base so the (gt, graphName)
// routing always wins. Mirrors linker.browseNodesViaEngine but adds the gt→
// selector translation (the linker's name:-only cloud read is a latent bug).
//
// Returns wire nodes ([]*knowledgev1.Node) straight from engine.DecodeNodes —
// both the read path and the proxy-WRITE path (LinkNodesAndEdgesBatch) are now
// knowledgev1-typed. Hooks read fields via kgtypes.Value/Meta/IsTombstoned.
func BrowseNodes(ctx context.Context, gc GraphCaller, gt kgtypes.GraphType, graphName string, extra map[string]any) ([]*knowledgev1.Node, error) {
	args := selectorArgs(gt, graphName)
	args["format"] = "json"
	maps.Copy(args, extra)
	body, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("postpopulate: marshal browse args: %w", err)
	}
	req, ok := engine.Compile("query", body)
	if !ok {
		return nil, fmt.Errorf("postpopulate: browse query args not reducible to an ExecuteRequest")
	}
	resp, err := gc.Execute(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("postpopulate: browse %s/%s: %w", gt, graphName, err)
	}
	return engine.DecodeNodes(resp)
}

// ListGraphNames enumerates the indexed graph names of graphType via the
// Execute carrier seam: a query(mode:modules) compiled to RETURN_MODE_GRAPH_
// NAMES whose carrier decodes to []*knowledgev1.GraphInfo; we project
// GraphInfo.Name → []string. Mirrors linker.fetchGraphNames. The hook
// orchestrator uses this to enumerate peer-account graphs (cross-account trust,
// cross-VPC, cloud-LB index) so an edge can be written into each peer's named
// graph.
func ListGraphNames(ctx context.Context, gc GraphCaller, gt kgtypes.GraphType) ([]string, error) {
	body, err := json.Marshal(map[string]any{
		"graph": string(gt),
		"mode":  "modules",
	})
	if err != nil {
		return nil, fmt.Errorf("postpopulate: marshal list-graphs args: %w", err)
	}
	req, ok := engine.Compile("query", body)
	if !ok {
		return nil, fmt.Errorf("postpopulate: list-graphs query args not reducible to an ExecuteRequest")
	}
	resp, err := gc.Execute(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("postpopulate: list graphs (%s): %w", gt, err)
	}
	infos, err := engine.DecodeGraphNames(resp)
	if err != nil {
		return nil, fmt.Errorf("postpopulate: list graphs decode (%s): %w", gt, err)
	}
	names := make([]string, 0, len(infos))
	for _, gi := range infos {
		if gi.Name == "" {
			continue
		}
		names = append(names, gi.Name)
	}
	return names, nil
}

// LinkEdgesBatch writes a batch of edges into a named per-account/per-repo
// graph via EXACTLY ONE mutate(create_batch) Execute call — never a per-edge
// loop. Empty edges is a no-op (no RPC fired). Edge endpoints are referenced by
// string ID (from_id/to_id); the nodes they connect already exist in the graph
// (the collector wrote them at upload time). The (gt, graphName) selector routes
// the write to the right backing DB so per-account topology analyzers see the
// edges intra-graph — NOT the linkage graph.
func LinkEdgesBatch(ctx context.Context, gc GraphCaller, gt kgtypes.GraphType, graphName string, edges []knowledgev1.Edge) error {
	if len(edges) == 0 {
		return nil
	}
	return execCreateBatch(ctx, gc, gt, graphName, nil, edges)
}

// LinkNodesAndEdgesBatch writes both new nodes AND edges into a named graph via
// EXACTLY ONE mutate(create_batch) Execute call. Used where a resolver must
// materialize a node that does not yet exist before linking it (k8s cross-graph
// proxy nodes, codesync package/hierarchy nodes, AWS CIDR sentinel nodes). Edges
// reference batch-local nodes by FromIdx/ToIdx (index into nodes) or pre-existing
// nodes by FromID/ToID — the store's createBatchEdges resolves both. Empty
// nodes AND edges is a no-op.
func LinkNodesAndEdgesBatch(ctx context.Context, gc GraphCaller, gt kgtypes.GraphType, graphName string, nodes []*knowledgev1.Node, edges []knowledgev1.Edge) error {
	if len(nodes) == 0 && len(edges) == 0 {
		return nil
	}
	batchEdges := make([]kgwire.BatchEdge, len(edges))
	for i := range edges {
		e := &edges[i]
		batchEdges[i] = kgwire.BatchEdge{
			FromIdx:       -1,
			ToIdx:         -1,
			FromID:        e.FromId,
			ToID:          e.ToId,
			Type:          kgtypes.EdgeType(e.Type),
			Weight:        e.Weight,
			Confidence:    e.Confidence,
			Method:        e.Method,
			Evidence:      e.Evidence,
			LastValidated: nanosToTimePP(e.LastValidated),
		}
	}
	return execCreateBatchNodes(ctx, gc, gt, graphName, nodes, batchEdges)
}

// execCreateBatch compiles + runs a single mutate(create_batch) carrying
// edges-only (edges referencing pre-existing nodes by string ID). nodes is the
// optional new-node payload (nil for the edges-only LinkEdgesBatch path).
func execCreateBatch(ctx context.Context, gc GraphCaller, gt kgtypes.GraphType, graphName string, nodes []*knowledgev1.Node, edges []knowledgev1.Edge) error {
	batchEdges := make([]kgwire.BatchEdge, len(edges))
	for i := range edges {
		e := &edges[i]
		batchEdges[i] = kgwire.BatchEdge{
			FromIdx:       -1,
			ToIdx:         -1,
			FromID:        e.FromId,
			ToID:          e.ToId,
			Type:          kgtypes.EdgeType(e.Type),
			Weight:        e.Weight,
			Confidence:    e.Confidence,
			Method:        e.Method,
			Evidence:      e.Evidence,
			LastValidated: nanosToTimePP(e.LastValidated),
		}
	}
	return execCreateBatchNodes(ctx, gc, gt, graphName, nodes, batchEdges)
}

// execCreateBatchNodes is the shared one-RPC create_batch writer: it builds the
// mutate(create_batch) args (selectorArgs base + nodes[] + edges[]), compiles to
// a CREATE MutationPlan, and runs ONE Execute. The wire JSON shape mirrors the
// engine's mutateArgs nodes[]/edges[] sub-shapes (compile_mutate.go nodeBody/
// edgeBody) so engine.Compile reduces it without legacy fallback.
func execCreateBatchNodes(ctx context.Context, gc GraphCaller, gt kgtypes.GraphType, graphName string, nodes []*knowledgev1.Node, edges []kgwire.BatchEdge) error {
	if len(nodes) == 0 && len(edges) == 0 {
		return nil
	}
	args := selectorArgs(gt, graphName)
	args["operation"] = "create_batch"
	if len(nodes) > 0 {
		args["nodes"] = nodesToWire(nodes)
	}
	if len(edges) > 0 {
		args["edges"] = edgesToWire(edges)
	}
	body, err := json.Marshal(args)
	if err != nil {
		return fmt.Errorf("postpopulate: marshal create_batch args: %w", err)
	}
	req, ok := engine.Compile("mutate", body)
	if !ok {
		return fmt.Errorf("postpopulate: create_batch args not reducible to a MutationPlan")
	}
	if _, err := gc.Execute(ctx, req); err != nil {
		return fmt.Errorf("postpopulate: create_batch %s/%s (%d nodes, %d edges): %w", gt, graphName, len(nodes), len(edges), err)
	}
	return nil
}

// nodesToWire maps knowledgev1.Node values onto the create_batch nodes[] wire
// shape (the engine's nodeBody subset: type/name/summary/content/status/metadata/
// id). SymbolName rides as the node "name" (cloud/cicd/code nodes carry their
// identity in SymbolName). Reads the proto fields directly (promoted onto the
// wire node) — providers pass the &proxy.Node extracted from BuildCrossGraphProxy
// or directly-built issuer nodes, both *knowledgev1.Node.
func nodesToWire(nodes []*knowledgev1.Node) []map[string]any {
	out := make([]map[string]any, len(nodes))
	for i, n := range nodes {
		m := map[string]any{
			"id":   n.Id,
			"type": n.Type,
			"name": n.SymbolName,
		}
		if n.Summary != "" {
			m["summary"] = n.Summary
		}
		if n.Content != "" {
			m["content"] = n.Content
		}
		if n.Status != "" {
			m["status"] = n.Status
		}
		if n.Source != "" {
			m["source"] = n.Source
		}
		if len(n.Metadata) > 0 {
			m["metadata"] = n.Metadata
		}
		out[i] = m
	}
	return out
}

// edgesToWire maps kgwire.BatchEdge values onto the create_batch edges[] wire
// shape (the engine's edgeBody: from_idx/to_idx/from_id/to_id/type + metadata).
func edgesToWire(edges []kgwire.BatchEdge) []map[string]any {
	out := make([]map[string]any, len(edges))
	for i, e := range edges {
		m := map[string]any{
			"from_idx": e.FromIdx,
			"to_idx":   e.ToIdx,
			"from_id":  e.FromID,
			"to_id":    e.ToID,
			"type":     string(e.Type),
		}
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
		if !e.LastValidated.IsZero() {
			m["last_validated"] = e.LastValidated.UTC().Format("2006-01-02T15:04:05Z07:00")
		}
		out[i] = m
	}
	return out
}
