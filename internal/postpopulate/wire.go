// SPDX-License-Identifier: Apache-2.0

package postpopulate

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/graphsel"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	"github.com/fulminate-io/knowledge-mcp/internal/paging"
)

// nanosToTimePP converts an int64 unix-nanos value (the value-embed proto
// Edge.LastValidated representation) to a time.Time for the
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
// store DB ("client operates zero store engine"). Every
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
// requires account") — the silent-write regression these helpers exist
// to prevent. The returned map is merged into the query/mutate args.
func selectorArgs(gt kgtypes.GraphType, graphName string) map[string]any {
	return graphsel.ScopePayload(gt, graphName, true)
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
//
// THIS SEAM SERVES ONE BOUNDED PAGE and refuses, before any RPC, a browse
// payload that carries no positive limit — see requirePositiveBrowseLimit. A
// caller that wants the whole matching set calls BrowseAllNodes.
func BrowseNodes(ctx context.Context, gc GraphCaller, gt kgtypes.GraphType, graphName string, extra map[string]any) ([]*knowledgev1.Node, error) {
	if err := requirePositiveBrowseLimit(extra); err != nil {
		return nil, err
	}
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

// UnboundedBrowseError reports a browse payload BrowseNodes refuses because it
// carries no positive limit. A non-positive or absent limit is not a request for
// everything at this seam: the client compiler rewrites it to the browse default
// (compile_query.go, applyBrowseLimitOffset), so serving it would hand the caller
// a silent default page in place of the set it asked for.
type UnboundedBrowseError struct{ Found any }

func (e *UnboundedBrowseError) Error() string {
	return fmt.Sprintf(
		"postpopulate: browse payload carries no positive limit (%v): this seam serves ONE bounded page — "+
			"use BrowseAllNodes to read the whole matching set", e.Found)
}

// positiveBrowseLimit reads a payload's limit across the numeric shapes such a
// payload can carry: a map built in Go holds an int, one that has been through a
// JSON round trip holds a float64. It reports the value and whether it is a
// usable positive bound.
func positiveBrowseLimit(v any) (int, bool) {
	var n int
	switch t := v.(type) {
	case int:
		n = t
	case int32:
		n = int(t)
	case int64:
		n = int(t)
	case float64:
		n = int(t)
	default:
		return 0, false
	}
	return n, n > 0
}

// requirePositiveBrowseLimit refuses, before any RPC, a browse payload whose
// limit is absent, unparseable or non-positive — the exact shape the compiler
// silently rewrites to the browse default. A by-id payload is EXEMPT: "ids" and
// "id" take the bulk-ids and by-id compile arms, which never reach
// applyBrowseLimitOffset, so no browse default applies to them.
//
// A refusal rather than a silent upgrade to a drain: this seam is the
// single-page primitive BrowseAllNodes calls, and making it page on its own
// would restore "limit 0 means everything" at a client seam. A loud refusal
// converts a silent partial read into an immediate, attributable failure.
func requirePositiveBrowseLimit(extra map[string]any) error {
	if _, ok := extra["ids"]; ok {
		return nil
	}
	if _, ok := extra["id"]; ok {
		return nil
	}
	if _, ok := positiveBrowseLimit(extra["limit"]); ok {
		return nil
	}
	return &UnboundedBrowseError{Found: extra["limit"]}
}

// UnpageablePayloadError reports a browse payload BrowseAllNodes refuses to
// drain because it does not reach the singular type-browse arm — the only arm
// the id-keyset cursor rides. Key names the key responsible.
type UnpageablePayloadError struct {
	Key    string
	Reason string
}

func (e *UnpageablePayloadError) Error() string {
	return fmt.Sprintf("postpopulate: browse payload key %q cannot be drained in keyset pages: %s", e.Key, e.Reason)
}

// precedenceKeysAboveType are the query keys the compiler dispatches on BEFORE
// the singular type browse, in its own precedence order.
var precedenceKeysAboveType = []string{"ids", "id", "text", "types"}

// requireSingularTypeBrowse refuses, before any RPC, a payload the keyset drain
// cannot page. It checks FOUR keys rather than one because the compiler
// dispatches a mode-less query in strict precedence order — ids, id, text,
// types, type, meta — and only the singular type-browse arm threads after_id. A
// payload carrying "type" PLUS any higher-precedence key takes the
// higher-precedence arm, which ignores the cursor: every page would return the
// same first page, the drain would never see a short page, and the loop would
// not terminate. Refusing loudly beats hanging.
func requireSingularTypeBrowse(extra map[string]any) error {
	for _, k := range precedenceKeysAboveType {
		if _, ok := extra[k]; ok {
			return &UnpageablePayloadError{
				Key:    k,
				Reason: "it outranks the singular type browse, and its arm threads no keyset cursor",
			}
		}
	}
	t, ok := extra["type"].(string)
	if !ok || strings.TrimSpace(t) == "" {
		return &UnpageablePayloadError{
			Key:    "type",
			Reason: "a non-blank singular type is what selects the arm the keyset cursor rides",
		}
	}
	return nil
}

// BrowseAllNodes returns EVERY node matching a browse payload, read as bounded
// id-keyset pages, where BrowseNodes performs a single bounded read. It is the
// helper for a caller that intends "all".
//
// A limit of 0 is a bounded DEFAULT at this seam, never a request for
// everything: the client compiler rewrites a non-positive limit to the browse
// default, and the server clamps any limit to its own row ceiling. Both bounds
// are deliberate and unchanged — a shared system never serves an unbounded
// query, it pages — so "all" is spelled as a drain rather than as a large limit.
//
// Cross-page ordering is id-ASCENDING and stable on both backends, which is what
// makes the cursor total: each page asks for the ids strictly greater than the
// last id of the page before it, and page one passes a SET BUT EMPTY cursor
// (presence is what selects the keyset browse; an omitted cursor would page in
// the backend's own default order and skip every lower id).
//
// The guard checks four keys rather than one, for the precedence reason
// requireSingularTypeBrowse documents.
//
// CONCURRENT WRITES are accepted rather than mechanized. A keyset cursor is
// anchored to a row id, not to a position: a row inserted mid-drain above the
// cursor is included, one inserted below it is missed, a deleted one is simply
// absent, and duplicates are impossible. Each drain is scoped to ONE graph and
// runs in the post-populate phase after that graph's own upload has completed,
// where the collector is the only writer of those nodes; a skew self-heals on
// the next collect, which rebuilds from scratch.
func BrowseAllNodes(ctx context.Context, gc GraphCaller, gt kgtypes.GraphType, graphName string, extra map[string]any) ([]*knowledgev1.Node, error) {
	if err := requireSingularTypeBrowse(extra); err != nil {
		return nil, err
	}
	return paging.DrainKeysetPages(func(afterID string) ([]*knowledgev1.Node, error) {
		page := make(map[string]any, len(extra)+3)
		maps.Copy(page, extra)
		// Set AFTER the copy: BrowseNodes merges extra over its own base map, so
		// caller keys win there — writing the page keys last is what stops a
		// caller's stale limit from defeating the drain.
		page["limit"] = paging.BrowsePageSize
		page["after_id"] = afterID
		page["skip_total"] = true
		return BrowseNodes(ctx, gc, gt, graphName, page)
	}, paging.BrowsePageSize)
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
	// Mark this as a TRUSTED collector CREATE so the server skips the user-facing
	// system-managed-type guard (decodeCreate → validateCreateNodeBody rejects
	// type=package|file|branch as "created by the code indexer, not by hand"). The
	// postpopulate (BuildHierarchy) IS the code indexer, so its package/branch/file
	// creates are legitimate. We set the flag PROGRAMMATICALLY on the compiled proto
	// (not via a create_batch arg key) so it is UNFORGEABLE through the user mutate
	// tool — the LLM supplies args, never proto fields, and no arg maps to it.
	if mp := req.GetMutation(); mp != nil {
		mp.SystemManagedCreate = true
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
