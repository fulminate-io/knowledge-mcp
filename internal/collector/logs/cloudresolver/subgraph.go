// SPDX-License-Identifier: Apache-2.0

// Package cloudresolver implements logs.CloudResolver and
// logs.DependencyChecker over an in-memory cloud-graph slice produced
// by IngestService.FetchCloudSubgraph. The package never touches a
// store engine — every walk runs against pre-fetched node + edge
// slices (the typed wire knowledgev1.Node / knowledgev1.Edge), keeping
// the resolver/dep-checker independent of the server's store.
package cloudresolver

import knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

// GraphSlice is one cloud graph's nodes + edges, as returned by the
// FetchCloudSubgraph RPC. Each slice carries BOTH NodeCloudResource
// nodes (the matchable resources) and NodeProxy nodes (cross-graph
// pointers that the BFS walks transparently). Nodes are pointers
// because knowledgev1.Node embeds a sync-locked proto MessageState
// (a value slice would trip the copylocks vet analyzer).
type GraphSlice struct {
	Name  string
	Nodes []*knowledgev1.Node
	Edges []knowledgev1.Edge
}

// CloudSubgraph is the multi-graph in-memory view the resolver and
// dep-checker query against. Slices are keyed by graph name (the same
// string used as cloud-graph file name and ResolvedResource.Account).
type CloudSubgraph struct {
	slices map[string]*GraphSlice
	// nodesByID indexes the GraphSlice.Nodes pointers directly. The
	// elements are already *knowledgev1.Node, so the index simply aliases
	// each slice entry — no addressing of a backing array is needed.
	nodesByID map[graphKey]*knowledgev1.Node
	// edgesByNode maps (account, nodeID) → undirected adjacency list of
	// outgoing+incoming edge pairs for BothEdges-style traversal. Keyed
	// separately per-graph because the same node ID can legitimately
	// exist in two graphs (e.g., "Namespace/default"). Entries point into
	// the GraphSlice.Edges backing array — knowledgev1.Edge embeds a
	// sync-locked proto MessageState, so pointers (not values) avoid a
	// copylocks violation.
	edgesByNode map[graphKey][]*knowledgev1.Edge
}

// graphKey is the (account, id) tuple that uniquely identifies a node
// across loaded cloud graphs. Carried over from
// tools_logs_dep_checker.go:108 — same shape, internal to this package.
type graphKey struct {
	account string
	id      string
}

func (gk graphKey) String() string { return gk.account + ":" + gk.id }

// NewCloudSubgraph builds the indexed view from the raw slices returned
// by FetchCloudSubgraph. Index construction is O(N+E) over the slice;
// run-time lookups are O(1) per node + O(degree) per edge walk.
func NewCloudSubgraph(slices []GraphSlice) *CloudSubgraph {
	sg := &CloudSubgraph{
		slices:      make(map[string]*GraphSlice, len(slices)),
		nodesByID:   map[graphKey]*knowledgev1.Node{},
		edgesByNode: map[graphKey][]*knowledgev1.Edge{},
	}
	for i := range slices {
		s := &slices[i]
		sg.slices[s.Name] = s
		for j := range s.Nodes {
			n := s.Nodes[j]
			sg.nodesByID[graphKey{account: s.Name, id: n.Id}] = n
		}
		for j := range s.Edges {
			// BothEdges-style adjacency — append to both endpoints'
			// lists so frontier expansion can walk in either direction
			// without re-scanning. duplicates ok; visited set dedupes.
			e := &s.Edges[j]
			from := graphKey{account: s.Name, id: e.FromId}
			to := graphKey{account: s.Name, id: e.ToId}
			sg.edgesByNode[from] = append(sg.edgesByNode[from], e)
			sg.edgesByNode[to] = append(sg.edgesByNode[to], e)
		}
	}
	return sg
}

// GraphNames returns every cloud-graph name present in the subgraph,
// in arbitrary order. Replaces the resolver's old store.ListGraphs
// call. Safe to call on a nil receiver (returns nil).
func (sg *CloudSubgraph) GraphNames() []string {
	if sg == nil {
		return nil
	}
	out := make([]string, 0, len(sg.slices))
	for name := range sg.slices {
		out = append(out, name)
	}
	return out
}

// Nodes returns ALL nodes in the named graph's slice — both
// NodeCloudResource and NodeProxy types are mixed, in slice order.
// Callers that need a type-filtered view must filter at call site
// (e.g. resolver's resolveByTypePrefixes only considers
// NodeCloudResource; BFS routes NodeProxy through expandThroughProxy).
//
// Returns nil when the graph is absent or the receiver is nil. Caller
// must NOT mutate the returned slice.
func (sg *CloudSubgraph) Nodes(graphName string) []*knowledgev1.Node {
	if sg == nil {
		return nil
	}
	if s, ok := sg.slices[graphName]; ok {
		return s.Nodes
	}
	return nil
}

// Node looks up a single node by (account, id). Returns ok=false when
// either the graph or the node is absent, or when the receiver is nil.
// The returned pointer aliases the underlying GraphSlice backing array;
// callers must NOT mutate it.
func (sg *CloudSubgraph) Node(account, id string) (*knowledgev1.Node, bool) {
	if sg == nil {
		return nil, false
	}
	n, ok := sg.nodesByID[graphKey{account: account, id: id}]
	return n, ok
}

// Edges returns BothEdges adjacency for (account, id) — the same
// envelope IterEdges(BothEdges) returns server-side. Out + in edges
// are interleaved; callers dedupe peer IDs themselves. Safe to call on
// a nil receiver (returns nil). Entries point into the GraphSlice
// backing array; callers must NOT mutate them.
func (sg *CloudSubgraph) Edges(account, id string) []*knowledgev1.Edge {
	if sg == nil {
		return nil
	}
	return sg.edgesByNode[graphKey{account: account, id: id}]
}
