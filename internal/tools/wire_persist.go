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
//   - UpdateBatchStatus   — single mutate(update_batch) carrying per-id status updates.
//
// The single-RPC subtree traversal helpers (TraverseDescendants,
// TraverseDescendantsWithEdges) live in
// cmd/knowledge/internal/projects/render/wire_traverse.go, beside the rest of
// the batched-render prologue they feed; callers here reach them through the
// render. qualifier.
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

// The create_batch wire-shape structs live in wire_persist_types.go.

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
			ID:          n.Id,
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
	resp, err := ex.Execute(ctx, &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Mutation{Mutation: plan},
	})
	if err != nil {
		return fmt.Errorf("UpdateBatchStatus: %w", err)
	}
	// THE COUNT IS READ, NOT DISCARDED, because this function's callers ENUMERATE
	// the ids they asked for in a success message. A batch that wrote fewer nodes
	// than it names would make that message assert a write it never confirmed —
	// the same silent-partial shape the cascade exists to remove, one layer down.
	//
	// EQUALITY IS THE SERVER'S CURRENT GUARANTEE ON THIS PATH, not an aspiration:
	// a named-id selection is resolved intolerantly server-side, so an id that
	// resolves to nothing aborts the batch with a not-found rather than being
	// skipped, and the applied count is the size of the resolved set. So a
	// shortfall is not a state a correct server produces today — which is exactly
	// what makes asserting it cheap and what makes a future regression loud
	// instead of invisible. It is a REFUSAL, not a fallback: nothing is retried,
	// nothing degrades, and the caller is told the delta.
	if got := resp.GetAffectedCount(); got != int64(len(ids)) {
		return fmt.Errorf(
			"UpdateBatchStatus: asked to write status %q to %d node(s) but the store reported %d applied "+
				"(%d unaccounted for); the ids named in any success message would overstate what was written",
			status, len(ids), got, int64(len(ids))-got)
	}
	return nil
}

// _ binds the kgtools import — used indirectly via gc.Call signatures.
var _ kgtools.ToolResult
