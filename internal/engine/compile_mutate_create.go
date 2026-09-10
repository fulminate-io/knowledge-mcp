// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"maps"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// The practice hub vocabulary, aliased locally so this file names the shared
// constants once rather than repeating their literals per use.
const (
	sourceHubMetaKey  = kgtypes.MetaKeySourceHub
	sourceHubEdgeType = string(kgtypes.EdgeSourcedFrom)
)

// createPayload builds the NodeBody + BatchEdgeSpec payload for compileMutateCreate,
// dispatching on the create vs create_batch shape. Returns ok=false for the
// reducible-deny cases (empty batch, missing single-create type, unparseable
// edge last_validated) so the caller falls through to legacy.
func createPayload(a mutateArgs) ([]*knowledgev1.NodeBody, []*knowledgev1.BatchEdgeSpec, bool) {
	if a.Operation == "create_batch" {
		// Edges-only create_batch is a first-class shape: PostPopulate hooks
		// write structural edges referencing nodes the collector
		// already uploaded, with no new node bodies. The store's CreateBatch
		// supports it natively (edges referencing pre-existing nodes by string
		// ID). Only the truly-empty batch (0 nodes AND 0 edges) is non-reducible
		// — there is nothing to create.
		if len(a.Nodes) == 0 && len(a.Edges) == 0 {
			return nil, nil, false // nothing to create.
		}
		bodies := make([]*knowledgev1.NodeBody, 0, len(a.Nodes))
		for _, n := range a.Nodes {
			n.Metadata = withSourceHub(n.Metadata, a.SourceHub)
			bodies = append(bodies, nodeBodyToProto(n))
		}
		edges, ok := batchEdgesToProto(a.Edges)
		if !ok {
			return nil, nil, false
		}
		return bodies, append(edges, sourceHubEdges(len(bodies), a.SourceHub)...), true
	}
	// Single create → one-element NodeBodies (the engine create_batch arm runs the
	// same CreateBatch primitive for one or N).
	if a.Type == "" {
		return nil, nil, false
	}
	return []*knowledgev1.NodeBody{nodeBodyToProto(nodeBody{
		Type:        a.Type,
		Name:        a.Name,
		Description: a.Description,
		Summary:     a.Summary,
		Content:     a.Content,
		Status:      derefStatus(a.Status),
		Metadata:    withSourceHub(a.Metadata, a.SourceHub),
		ID:          a.ID,
		Source:      a.Source,
	})}, sourceHubEdges(1, a.SourceHub), true
}

// withSourceHub stamps the practice hub id onto a created node's metadata.
//
// A hub is recorded TWICE and neither recording is redundant: this metadata key
// is what a browse, a by-hub delete and the ranked search's member resolution
// all predicate on, and the sourceHubEdges below are what a traverse walks. A
// metadata key cannot be traversed and an edge cannot be a browse predicate, so
// the two answer different questions about the same fact.
//
// An explicit metadata key the caller already set WINS and is left alone. That is
// not a silent preference: the tools-side gate (guardPracticeHubBodyHubs) reads
// EVERY body a practice payload can carry — the top-level `metadata`, each
// `nodes[]` body of a create_batch, and the upsert body — and refuses one whose
// key disagrees with the call's hub before the payload reaches here, so by this
// point the two either agree or only one was supplied.
func withSourceHub(meta map[string]string, hub string) map[string]string {
	if hub == "" {
		return meta
	}
	// SIZED FROM ONE LENGTH, NOT A SUM: a make() capacity is a hint —
	// the one hub key costs at most a growth — while a size built by
	// addition is a value the allocator has to take on trust.
	out := make(map[string]string, len(meta))
	maps.Copy(out, meta)
	if _, ok := out[sourceHubMetaKey]; !ok {
		out[sourceHubMetaKey] = hub
	}
	return out
}

// sourceHubEdges emits one node→hub `sourced-from` edge per created body.
//
// THE DIRECTION IS node → hub, which is where requirement 2's cardinality lives:
// exactly one outgoing sourced-from per practice node, assertable at the node
// rather than by counting a hub's inbound fan. Members are enumerated the other
// way, with direction:"in" from the hub.
//
// IT RIDES THE SAME PLAN AS THE NODES, by slot index into the bodies just built,
// so the node and its hub link land in ONE CreateBatch. A follow-up LINK call
// would leave a window in which a node exists with no hub — invisible to every
// hub-scoped read, and precisely the state a failed collection needs to be
// deletable from.
func sourceHubEdges(bodies int, hub string) []*knowledgev1.BatchEdgeSpec {
	if hub == "" || bodies == 0 {
		return nil
	}
	out := make([]*knowledgev1.BatchEdgeSpec, 0, bodies)
	for i := range bodies {
		out = append(out, &knowledgev1.BatchEdgeSpec{
			FromIdx: int32(i),
			ToIdx:   -1,
			ToId:    hub,
			Type:    sourceHubEdgeType,
		})
	}
	return out
}

// batchEdgesToProto lowers create_batch's edgeBody list onto proto BatchEdgeSpec,
// carrying the endpoint + type + the five edge-metadata fields AS-GIVEN.
// last_validated (RFC3339) converts to int64 unix-nanos via the shared link-arm
// parseLastValidatedNanos helper; an unparseable value returns ok=false so the
// caller falls through to legacy (where the RFC3339 error surfaces).
func batchEdgesToProto(in []edgeBody) ([]*knowledgev1.BatchEdgeSpec, bool) {
	if len(in) == 0 {
		return nil, true
	}
	out := make([]*knowledgev1.BatchEdgeSpec, 0, len(in))
	for _, e := range in {
		lastNanos, ok := parseLastValidatedNanos(e.LastValidated)
		if !ok {
			return nil, false
		}
		out = append(out, &knowledgev1.BatchEdgeSpec{
			FromIdx:       int32(e.FromIdx),
			ToIdx:         int32(e.ToIdx),
			FromId:        e.FromID,
			ToId:          e.ToID,
			Type:          e.Type,
			Weight:        e.Weight,
			Confidence:    e.Confidence,
			Method:        e.Method,
			Evidence:      e.Evidence,
			LastValidated: lastNanos,
		})
	}
	return out, true
}
