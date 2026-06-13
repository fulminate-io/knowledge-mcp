// SPDX-License-Identifier: Apache-2.0

// Package tools — wire_persist.go exports the shared client-side
// persistence helpers every relocated intercept uses to talk
// to the server's mutate / query / traverse handlers. Each helper is
// single-RPC (one gc.Call) so the client-side intercept code stays a
// thin sequencer over the wire instead of re-implementing per-feature
// transaction shapes.
//
// Helpers:
//   - PersistBatch — single mutate(create_batch) with optional bundle_id.
//   - LookupNode   — single query(id) with include_edges:false.
//   - LinkOne      — single mutate(link) for one from→to edge.
//   - TraverseDescendants — single traverse(start, edge, depth) returning hydrated nodes.
//   - TraverseDescendantsWithEdges — like TraverseDescendants but also returns the subtree's structure edges (one RPC).
//   - UpdateBatchStatus   — single mutate(update_batch) carrying per-id status updates.
//
// All return the underlying gc.Call error verbatim (no double-wrap with
// "<feature>: ..." prefixes). Callers pre-add their tool-name prefix.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	"github.com/fulminate-io/knowledge-mcp/internal/projects/render"
)

// persistBatchNode mirrors server-side nodeCreateItem
// (tools_mutate_create_batch.go:52) plus the source carrier. The struct uses the
// wire tags the server expects. Source is mapped from store.Node.Source so a
// client-stamped provenance (e.g. buildFindingNode's 'llm:claude') survives onto
// the create_batch carrier and through to the engine NodeBody.source field — the
// Gap-2 fix (Source was previously lossy on the batch wire).
type persistBatchNode struct {
	Type        string            `json:"type"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Summary     string            `json:"summary"`
	Content     string            `json:"content"`
	Status      string            `json:"status"`
	Metadata    map[string]string `json:"metadata"`
	Source      string            `json:"source,omitempty"`
}

// persistBatchEdge mirrors server-side edgeCreateItem
// (tools_mutate_create_batch.go:69). The from_idx / to_idx fields are
// emitted explicitly so the server's UnmarshalJSON sentinel (-1 ==
// "use string ID") applies even when both endpoints reference an
// existing node by ID.
//
// It also carries the five edge-metadata fields
// (weight/confidence/method/evidence/last_validated) with omitempty so a
// Method/Weight/… set on a kgwire.BatchEdge survives the PersistBatch
// projection onto the engine create_batch edgeBody (the json keys match the
// engine's edgeBody decode tags so engine.Compile threads them onto the
// BatchEdgeSpec). last_validated is the RFC3339 STRING shape the edgeBody
// decodes — NOT the int64 unix-nanos proto carrier. omitempty makes an
// all-unset edge marshal byte-identically to the pre-metadata shape, so every
// existing PersistBatch caller is unaffected.
type persistBatchEdge struct {
	FromIdx       int     `json:"from_idx"`
	ToIdx         int     `json:"to_idx"`
	FromID        string  `json:"from_id,omitempty"`
	ToID          string  `json:"to_id,omitempty"`
	Type          string  `json:"type"`
	Weight        float64 `json:"weight,omitempty"`
	Confidence    float64 `json:"confidence,omitempty"`
	Method        string  `json:"method,omitempty"`
	Evidence      string  `json:"evidence,omitempty"`
	LastValidated string  `json:"last_validated,omitempty"`
}

// persistBatchArgs is the wire-shape envelope sent to mutate(create_batch).
type persistBatchArgs struct {
	Operation string             `json:"operation"`
	Nodes     []persistBatchNode `json:"nodes"`
	Edges     []persistBatchEdge `json:"edges"`
	BundleID  string             `json:"bundle_id,omitempty"`
}

// PersistBatch fires a single mutate(create_batch) RPC and returns the
// generated node IDs. bundleID is optional — empty is a no-op on the
// server (store.ContextWithBundleID short-circuits at id=""). When
// non-empty, every node + edge created by the batch shares one
// bundle_anchor in the version overlay.
//
// Single-RPC, all-or-nothing: the server runs validation across every
// item, then opens ONE store.Txn that commits exactly once. Mirrors the
// historical projects.CreatePlan + projects.CreateTicket bundle parity.
func PersistBatch(ctx context.Context, gc GraphCaller, nodes []*knowledgev1.Node, edges []kgwire.BatchEdge, bundleID string) ([]string, error) {
	if gc == nil {
		return nil, fmt.Errorf("PersistBatch: graph caller unavailable")
	}
	wireNodes := make([]persistBatchNode, len(nodes))
	for i, n := range nodes {
		wireNodes[i] = persistBatchNode{
			Type:        n.Type,
			Name:        n.SymbolName,
			Description: n.Description,
			Summary:     n.Summary,
			Content:     n.Content,
			Status:      n.Status,
			Metadata:    nodeMetadataMap(n),
			Source:      n.Source,
		}
	}
	wireEdges := make([]persistBatchEdge, len(edges))
	for i, e := range edges {
		we := persistBatchEdge{
			FromIdx:    e.FromIdx,
			ToIdx:      e.ToIdx,
			FromID:     e.FromID,
			ToID:       e.ToID,
			Type:       string(e.Type),
			Weight:     e.Weight,
			Confidence: e.Confidence,
			Method:     e.Method,
			Evidence:   e.Evidence,
		}
		// LastValidated bridges kgwire.BatchEdge's time.Time onto the edgeBody's
		// RFC3339 string shape — skip-on-zero, UTC RFC3339 — mirroring
		// postpopulate edgesToWire exactly so the two create_batch projections
		// cannot diverge. (NOT the int64 unix-nanos proto-BatchEdge carrier.)
		if !e.LastValidated.IsZero() {
			we.LastValidated = e.LastValidated.UTC().Format(time.RFC3339)
		}
		wireEdges[i] = we
	}
	args, err := json.Marshal(persistBatchArgs{
		Operation: "create_batch",
		Nodes:     wireNodes,
		Edges:     wireEdges,
		BundleID:  bundleID,
	})
	if err != nil {
		return nil, fmt.Errorf("PersistBatch: marshal: %w", err)
	}
	// Lower the create_batch JSON to a MutationPlan via the engine
	// (reuses the existing compile_mutate.go create_batch lowering) and Execute it;
	// the created node IDs ride the raw resp.GetIds() carrier (not the formatted
	// {ids:[...]} tool wire).
	resp, err := executeMutate(ctx, gc, args)
	if err != nil {
		return nil, fmt.Errorf("PersistBatch: %w", err)
	}
	return resp.GetIds(), nil
}

// executeMutate lowers a mutate JSON arg to a MutationPlan ExecuteRequest via
// engine.Compile and runs it through the Execute carrier seam. Shared by
// PersistBatch / LinkOne (the create_batch / link shapes the engine compiles).
func executeMutate(ctx context.Context, gc GraphCaller, args json.RawMessage) (*knowledgev1.ExecuteResponse, error) {
	ex, err := persistExecutor(gc)
	if err != nil {
		return nil, err
	}
	req, ok := engine.Compile("mutate", args)
	if !ok {
		return nil, fmt.Errorf("mutate args not reducible to a MutationPlan")
	}
	return ex.Execute(ctx, req)
}

// executeQuery lowers a query JSON arg to a QueryPlan ExecuteRequest via
// engine.Compile and runs it through the Execute carrier seam. Shared by the
// reducible query READS (type-browse listings, by-id, etc.) the tools intercepts
// issue; callers decode the response via engine.DecodeNodes / DecodeSearch /
// DecodeGraphNames per the read shape.
func executeQuery(ctx context.Context, gc GraphCaller, args json.RawMessage) (*knowledgev1.ExecuteResponse, error) {
	ex, err := persistExecutor(gc)
	if err != nil {
		return nil, err
	}
	req, ok := engine.Compile("query", args)
	if !ok {
		return nil, fmt.Errorf("query args not reducible to a QueryPlan")
	}
	return ex.Execute(ctx, req)
}

// persistExecutor upgrades a GraphCaller to the render.Executor carrier seam (the
// production graphClientCaller implements it). A non-Executor GraphCaller returns
// a typed error so the missing seam is loud.
func persistExecutor(gc GraphCaller) (render.Executor, error) {
	ex, ok := gc.(render.Executor)
	if !ok {
		return nil, fmt.Errorf("persist requires an Execute-capable graph client")
	}
	return ex, nil
}

// nodeMetadataMap extracts every key/value pair from n.Metadata as a
// plain string→string map. We extract all keys here so the wire payload
// carries the full backend / pattern / status metadata that
// BuildPlanGraph / BuildTicketNode / BuildProjectNode set on the node.
func nodeMetadataMap(n *knowledgev1.Node) map[string]string {
	if len(n.Metadata) == 0 {
		return nil
	}
	out := make(map[string]string, len(n.Metadata))
	maps.Copy(out, n.Metadata)
	return out
}

// LookupNode fetches a node by ID via a single query(id) RPC.
// Wraps render.FetchNode — the wire shape is identical (include_edges
// is false by default in MarshalQueryByID).
//
// Returns (nil, nil) when the node does not exist or the response
// body is empty; callers branch on node == nil for the not-found
// case. Transport errors are surfaced verbatim.
func LookupNode(ctx context.Context, gc GraphCaller, id string) (*knowledgev1.Node, error) {
	return render.FetchNode(ctx, gc, id)
}

// linkArgs is the wire-shape envelope for mutate(link).
type linkArgs struct {
	Operation    string `json:"operation"`
	From         string `json:"from"`
	To           string `json:"to"`
	Relationship string `json:"relationship"`
}

// LinkOne fires a single mutate(link) RPC creating one from→to edge of
// the given type. Returns the underlying gc.Call error verbatim; on
// IsError the response text is folded into the returned error.
func LinkOne(ctx context.Context, gc GraphCaller, fromID, toID string, edgeType kgtypes.EdgeType) error {
	if gc == nil {
		return fmt.Errorf("LinkOne: graph caller unavailable")
	}
	args, err := json.Marshal(linkArgs{
		Operation:    "link",
		From:         fromID,
		To:           toID,
		Relationship: string(edgeType),
	})
	if err != nil {
		return fmt.Errorf("LinkOne: marshal: %w", err)
	}
	if _, err := executeMutate(ctx, gc, args); err != nil {
		return fmt.Errorf("LinkOne: %w", err)
	}
	return nil
}

// linkWithMetaArgs is the wider wire-shape envelope for a metadata-carrying
// mutate(link). It is deliberately distinct from the narrow linkArgs so LinkOne's
// 4-field bare-link hot path stays untouched. The metadata json keys
// (weight/confidence/method/edge_evidence/last_validated) match mutateArgs'
// canonical tags so engine.Compile threads them onto the EdgeSpec.
type linkWithMetaArgs struct {
	Operation     string  `json:"operation"`
	From          string  `json:"from"`
	To            string  `json:"to"`
	Relationship  string  `json:"relationship"`
	Weight        float64 `json:"weight,omitempty"`
	Confidence    float64 `json:"confidence,omitempty"`
	Method        string  `json:"method,omitempty"`
	EdgeEvidence  string  `json:"edge_evidence,omitempty"`
	LastValidated string  `json:"last_validated,omitempty"`
}

// LinkOneWithMeta fires a single mutate(link) RPC creating one from→to edge of
// the given type, carrying the edge's metadata (Weight/Confidence/Method/
// Evidence/LastValidated) AS-GIVEN onto the EdgeSpec. It runs the SAME
// executeMutate seam as LinkOne; only the wider wire struct differs.
//
// LastValidated marshals with time.RFC3339Nano (NOT time.RFC3339) so sub-second
// precision survives the round-trip — the migration's "preserve verbatim"
// contract depends on it. A zero LastValidated is omitted (omitempty), decoding
// to the unset/zero time on the server.
func LinkOneWithMeta(ctx context.Context, gc GraphCaller, e *knowledgev1.Edge) error {
	if gc == nil {
		return fmt.Errorf("LinkOneWithMeta: graph caller unavailable")
	}
	wire := linkWithMetaArgs{
		Operation:    "link",
		From:         e.FromId,
		To:           e.ToId,
		Relationship: e.Type,
		Weight:       e.Weight,
		Confidence:   e.Confidence,
		Method:       e.Method,
		EdgeEvidence: e.Evidence,
	}
	if e.LastValidated != 0 {
		wire.LastValidated = time.Unix(0, e.LastValidated).Format(time.RFC3339Nano)
	}
	args, err := json.Marshal(wire)
	if err != nil {
		return fmt.Errorf("LinkOneWithMeta: marshal: %w", err)
	}
	if _, err := executeMutate(ctx, gc, args); err != nil {
		return fmt.Errorf("LinkOneWithMeta: %w", err)
	}
	return nil
}

// persistTraverseArgs is the wire-shape envelope for a single traverse
// RPC that walks edge_types from start to a bounded depth and returns
// hydrated nodes. Named distinct from cmd/knowledge/internal/tools/
// tools_logs_args.go::traverseArgs which mirrors a different wire shape.

// TraverseDescendants walks the EdgeKGContains tree (or any edge type)
// forward from rootID up to depth and returns the hydrated descendant
// nodes in BFS-discovery order. Mirrors the
// projects.descendantsForRollup contract (rootID NOT included).
//
// Single gc.Call — the server's traverse handler issues one store-side
// bounded-depth query and returns the full BFS in one response.
func TraverseDescendants(ctx context.Context, gc GraphCaller, rootID string, edgeType kgtypes.EdgeType, depth int) ([]*knowledgev1.Node, error) {
	if gc == nil {
		return nil, fmt.Errorf("TraverseDescendants: graph caller unavailable")
	}
	ex, err := persistExecutor(gc)
	if err != nil {
		return nil, fmt.Errorf("TraverseDescendants: %w", err)
	}
	// A forward (out) traversal over the edge type, returning the
	// raw traversal_results_json carrier ([]store.TraversalResult — each carries a
	// full store.Node), then the skip-root / skip-empty-ID filter client-side.
	fwd := true
	plan := &knowledgev1.QueryPlan{
		Selection:  &knowledgev1.Selection{FromId: []string{rootID}, EdgeTypes: []string{string(edgeType)}},
		Forward:    &fwd,
		ReturnMode: knowledgev1.ReturnMode_RETURN_MODE_TRAVERSAL,
	}
	if depth > 0 {
		plan.MaxHops = int32(depth)
	}
	resp, err := ex.Execute(ctx, &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Query{Query: plan},
	})
	if err != nil {
		return nil, fmt.Errorf("TraverseDescendants: %w", err)
	}
	results, derr := engine.DecodeTraversal(resp)
	if derr != nil {
		return nil, fmt.Errorf("TraverseDescendants: decode: %w", derr)
	}
	return filterTraversalDescendants(results, rootID), nil
}

// TraverseDescendantsWithEdges is the edge-aware sibling of
// TraverseDescendants: one forward traversal that returns BOTH the
// hydrated descendant nodes (root filtered, BFS-discovery order) AND
// the subtree's structure edges, in a single Execute. It carries the
// same QueryPlan shape as TraverseDescendants and adds one field:
// IncludeEdgeMetadata, which is what makes the server populate the
// traversal_edges carrier (the edges walked during the traversal).
// The whole flat (nodes, edges) result is the complete source a
// client-side tree builder needs without any per-node fetch.
//
// It deliberately does NOT set IncludeTombstones. The structure-edge
// carrier unconditionally drops any edge whose peer is tombstoned, so a
// tombstoned child never reaches the index regardless of the flag; the
// per-node walk this replaces dropped the same edges, so behavior is
// preserved by the edge being absent. Setting the flag would only add
// tombstoned nodes whose inbound edge is still dropped — orphans that
// render in neither tree.
//
// A separate function (rather than extending TraverseDescendants) keeps
// the two existing nodes-only callers of TraverseDescendants unchanged.
func TraverseDescendantsWithEdges(
	ctx context.Context,
	gc GraphCaller,
	rootID string,
	edgeType kgtypes.EdgeType,
	depth int,
) ([]*knowledgev1.Node, []*knowledgev1.Edge, error) {
	if gc == nil {
		return nil, nil, fmt.Errorf("TraverseDescendantsWithEdges: graph caller unavailable")
	}
	ex, err := persistExecutor(gc)
	if err != nil {
		return nil, nil, fmt.Errorf("TraverseDescendantsWithEdges: %w", err)
	}
	fwd := true
	plan := &knowledgev1.QueryPlan{
		Selection:           &knowledgev1.Selection{FromId: []string{rootID}, EdgeTypes: []string{string(edgeType)}},
		Forward:             &fwd,
		ReturnMode:          knowledgev1.ReturnMode_RETURN_MODE_TRAVERSAL,
		IncludeEdgeMetadata: true,
	}
	if depth > 0 {
		plan.MaxHops = int32(depth)
	}
	resp, err := ex.Execute(ctx, &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Query{Query: plan},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("TraverseDescendantsWithEdges: %w", err)
	}
	results, derr := engine.DecodeTraversal(resp)
	if derr != nil {
		return nil, nil, fmt.Errorf("TraverseDescendantsWithEdges: decode: %w", derr)
	}
	nodes := filterTraversalDescendants(results, rootID)
	// engine.EdgesFromProto returns a []knowledgev1.Edge value slice;
	// take addresses into a pointer slice so the index builder consumes
	// the same *knowledgev1.Edge shape the wire carriers use elsewhere.
	edgeVals := engine.EdgesFromProto(resp.GetTraversalEdges())
	edges := make([]*knowledgev1.Edge, len(edgeVals))
	for i := range edgeVals {
		edges[i] = &edgeVals[i]
	}
	return nodes, edges, nil
}

// filterTraversalDescendants applies the skip-root / skip-empty-ID filter over a
// decoded []engine.TraversalResult and returns the surviving nodes in order. The
// carrier already carries full typed nodes, so there is no wire→store conversion
// (this is the filter half of the removed collectTraverseNodes).
func filterTraversalDescendants(results []engine.TraversalResult, rootID string) []*knowledgev1.Node {
	out := make([]*knowledgev1.Node, 0, len(results))
	for _, r := range results {
		if r.Node.Id == "" || r.Node.Id == rootID {
			continue
		}
		out = append(out, r.Node)
	}
	return out
}

// UpdateBatchStatus issues a single mutate(update_batch) RPC applying
// status to every id in ids. bundleID is optional — empty is a no-op
// on the server. Two RPCs total when combined with TraverseDescendants:
// one traverse + one update_batch regardless of N descendants.
//
// Returns the underlying gc.Call error verbatim. Empty ids is a no-op.
func UpdateBatchStatus(ctx context.Context, gc GraphCaller, ids []string, status, bundleID string) error {
	if gc == nil {
		return fmt.Errorf("UpdateBatchStatus: graph caller unavailable")
	}
	if len(ids) == 0 {
		return nil
	}
	_ = bundleID // version-overlay bundle anchor is not carried by the engine
	// MutationPlan write path (parity with the create_batch carrier path, which
	// also drops it); the rollup writes still apply the uniform status to every id.
	ex, err := persistExecutor(gc)
	if err != nil {
		return fmt.Errorf("UpdateBatchStatus: %w", err)
	}
	// Every id gets the SAME status — a uniform UPDATE over Selection.Ids with
	// set_fields{status} (the homogeneous shape the engine UPDATE arm expresses),
	// NOT the heterogeneous update_batch the engine default-denies.
	plan := &knowledgev1.MutationPlan{
		Kind:      knowledgev1.MutationPlan_MUTATION_KIND_UPDATE,
		Selection: &knowledgev1.Selection{Ids: ids},
		SetFields: map[string]string{"status": status},
	}
	if _, err := ex.Execute(ctx, &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Mutation{Mutation: plan},
	}); err != nil {
		return fmt.Errorf("UpdateBatchStatus: %w", err)
	}
	return nil
}

// _ binds the kgtools import — used indirectly via gc.Call signatures.
var _ kgtools.ToolResult
