// SPDX-License-Identifier: Apache-2.0

// Package exposure holds the cross-graph exposure analyzer family of the
// client-side topology suite: IAM privilege-escalation, AWS security-group /
// NACL / cross-VPC reachability, Kubernetes NetworkPolicy reachability, and
// the public-exposure path walkers (AWS, K8s, and the unified cross-graph
// composer). Every analyzer reads its nodes and edges over the wire through
// foundation.GraphCaller — there is no in-process store.
//
// The family is "cross-graph" because several analyzers reach beyond the one
// account they were dispatched against: the IAM escalation walker resolves
// trust-policy principals across every loaded cloud account, the public
// exposure sensitive-terminal classifier consults persisted iam_escalation
// findings on the knowledge graph, and the unified walker follows linkage
// bridge edges into the foreign-id namespace. reader.go owns the thin
// wire-access shims that decompose those reads into the foundation helpers.
package exposure

import (
	"context"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// edgeDirection selects which incident edges a cloudReader.iterEdges walk
// keeps for a pivot node. It mirrors the legacy store.EdgeDirection vocabulary
// (outgoing / incoming / both) so the ported algorithm bodies read identically
// to the store-backed originals — the only change is the receiver.
type edgeDirection int

const (
	// outgoingEdges keeps edges where the pivot is the source (FromId == pivot).
	outgoingEdges edgeDirection = iota
	// incomingEdges keeps edges where the pivot is the target (ToId == pivot).
	incomingEdges
	// bothEdges keeps every incident edge regardless of orientation.
	bothEdges
)

// cloudReader is the per-account wire shim every exposure analyzer reads
// through. It pins a foundation.GraphCaller plus the cloud-graph instance name
// (account) so the ported algorithms can issue the same "edges out of this
// node" / "node by id" / "every cloud resource" reads they used to issue
// against a store-scoped DB, now over the wire. One legacy scoped.IterEdges /
// scoped.Query call maps to one bounded wire read here (the edge read pages
// internally) — no new N+1 fan-out is introduced.
//
// A cloudReader is cheap to construct (it holds two references) so analyzers
// build a fresh one per account; the cross-account IAM walk builds one per
// other-account graph exactly as the store-backed loop scoped one DB per
// account.
type cloudReader struct {
	caller  foundation.GraphCaller
	graph   kgtypes.GraphType
	account string
}

// newCloudReader binds a caller + account into a cloud-graph reader.
func newCloudReader(caller foundation.GraphCaller, account string) *cloudReader {
	return &cloudReader{caller: caller, graph: kgtypes.GraphCloud, account: account}
}

// newLinkageReader binds a caller into a linkage-graph reader. The linkage
// graph has a single "default" instance — the unified walker's bridge lookup
// reads through this rather than the per-account cloud reader.
func newLinkageReader(caller foundation.GraphCaller) *cloudReader {
	return &cloudReader{caller: caller, graph: kgtypes.GraphLinkage, account: "default"}
}

// iterEdges returns every edge incident to nodeID in the reader's graph,
// filtered to edgeTypes and the requested direction. It is the wire twin of
// the legacy scoped.IterEdges(EdgeIterRequest{NodeID, Direction, EdgeTypes})
// loop: one foundation.FetchEdges Execute over the single-element id set,
// followed by an in-memory direction filter. foundation.FetchEdges returns the
// both-direction union for the id set, so we keep only the orientation the
// caller asked for. The returned slice is freshly allocated and owned by the
// caller.
//
// The result is a slice of POINTERS into the FetchEdges value slice rather
// than a value slice: knowledgev1.Edge embeds a proto message-state mutex, so
// returning (and ranging over) values would trip go vet's copylocks check.
// Callers iterate with `for _, e := range edges` where e is already a *Edge —
// no lock is ever copied.
func (r *cloudReader) iterEdges(ctx context.Context, nodeID string, dir edgeDirection, edgeTypes []kgtypes.EdgeType) ([]*knowledgev1.Edge, error) {
	if r == nil || r.caller == nil || nodeID == "" {
		return nil, nil
	}
	edges, err := foundation.FetchEdges(ctx, r.caller, r.graph, r.account, []string{nodeID}, edgeTypes)
	if err != nil {
		return nil, err
	}
	out := make([]*knowledgev1.Edge, 0, len(edges))
	for i := range edges {
		e := &edges[i]
		switch dir {
		case outgoingEdges:
			if e.FromId != nodeID {
				continue
			}
		case incomingEdges:
			if e.ToId != nodeID {
				continue
			}
		case bothEdges:
			// keep either orientation
		}
		out = append(out, e)
	}
	return out, nil
}

// nodeByID returns the single node with the given id in the reader's graph, or
// nil when it is absent. It is the wire twin of scoped.Query(store.ByID(id)) /
// rctx.RootDB.Scope(...).Query(ByID(...)). Errors are surfaced; an absent node
// returns (nil, nil) so callers can treat "not found" identically to the
// legacy len(qr.NodeList) == 0 check.
func (r *cloudReader) nodeByID(ctx context.Context, id string) (*knowledgev1.Node, error) {
	if r == nil || r.caller == nil || id == "" {
		return nil, nil
	}
	n, ok, err := foundation.FetchNodeByID(ctx, r.caller, r.graph, r.account, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return n, nil
}

// cloudResources returns every cloud-resource node in the reader's account in
// one Execute. It is the wire twin of scoped.IterateAll(NodeCloudResource, …)
// and scoped.Query(Match(NodeCloudResource).Limit(0)) — both legacy shapes
// enumerated the same set, so both collapse onto this single browse.
func (r *cloudReader) cloudResources(ctx context.Context) ([]*knowledgev1.Node, error) {
	if r == nil || r.caller == nil {
		return nil, nil
	}
	return foundation.FetchNodesByType(ctx, r.caller, r.graph, r.account, kgtypes.NodeCloudResource)
}

// nodeMeta reads a metadata value off a wire node. Wire-fetched nodes carry
// their fully-resolved metadata in the inline Metadata map (the server
// materializes edge-mode values into the map before serving), so this is the
// wire twin of the legacy *store.Node.Value(key): a plain map lookup with the
// nil-node and nil-map guards the store method also applied. Returns the empty
// string for an absent key or a nil node.
func nodeMeta(n *knowledgev1.Node, key string) string {
	if n == nil || n.Metadata == nil {
		return ""
	}
	return n.Metadata[key]
}

// primaryEvidence returns the first evidence node ID for a Finding, or ""
// if Evidence is empty. Used as the dedup discriminator and as the stable
// tie-breaker in the exposure-family sort functions. This is the
// exposure-family local copy of the helper that lives in pkg/topology's
// shared findings.go — that file is dispatcher scaffolding (it also owns
// EmitFindingsForGraph, which persists Findings server-side) and is NOT
// part of the relocated analyzer family, so the family carries the tiny
// pure accessor it needs.
func primaryEvidence(f Finding) string {
	if len(f.Evidence) == 0 {
		return ""
	}
	return f.Evidence[0]
}

// resolveNodeName looks up a node's display name (SymbolName) for use in
// human-readable Finding titles, falling back to the raw nodeID when the read
// fails or the node carries no SymbolName. This is the exposure-family local
// copy of the legacy topology.ResolveNodeName: that helper was shared
// scaffolding in pkg/topology, but the foundation wire surface deliberately
// folds name resolution into FetchNodeByID (see foundation/wire.go), and the
// per-family client packages are disjoint, so each family rebuilds the thin
// resolver on top of FetchNodeByID. Cost: one Execute per call, matching the
// store-backed original's one query per call.
func resolveNodeName(ctx context.Context, caller foundation.GraphCaller, graphType kgtypes.GraphType, name, nodeID string) string {
	if caller == nil || nodeID == "" {
		return nodeID
	}
	n, ok, err := foundation.FetchNodeByID(ctx, caller, graphType, name, nodeID)
	if err != nil || !ok || n == nil {
		return nodeID
	}
	if n.SymbolName != "" {
		return n.SymbolName
	}
	return nodeID
}
