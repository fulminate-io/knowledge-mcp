// SPDX-License-Identifier: Apache-2.0

// Package tools — pre-fetched log graph state for the
// wire-fetch handler architecture.
//
// logState replaces an earlier proposal of a 50-method
// `prefetchedLogDB` store.DB shim (the client must not host store-shaped
// wrappers). Instead, the
// 7 moved log handler families receive a `*logState` value type that
// materializes every template/stream/chunk node and every edge a single
// formatter call needs. No store.DB surface. No panicking methods. The
// state is built once per MCP call via getOrFetchLogState in
// tools_logs_wire_fetch.go and discarded after the response is rendered
// (no engine cache).

package tools

import (
	"slices"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// logState is the pre-fetched view of one log graph. Built once per MCP
// call, consumed by every moved handler in cmd/knowledge/internal/tools.
// Plain fields — handlers reach for slices and maps directly.
//
// Templates / Streams / Chunks are the three primary log-graph node
// slices the formatters iterate. Labels + Proxies are auxiliary: labels
// surface via HAS_LABEL edges from streams; proxies surface via
// EMITTED_BY edges from labels. Both are indexed in byID for the ID-
// lookup hot path but the handlers don't enumerate them directly.
type logState struct {
	Templates []*knowledgev1.Node
	Streams   []*knowledgev1.Node
	Chunks    []*knowledgev1.Node
	Labels    []*knowledgev1.Node
	Proxies   []*knowledgev1.Node

	// byID indexes every node above so handler ByID lookups become O(1).
	byID map[string]*knowledgev1.Node

	// OutEdges / InEdges pre-group edges by source / target node ID so
	// handler calls of the form
	//   logDB.IterEdges(ctx, EdgeIterRequest{NodeID:X,
	//                                        Direction:OutgoingEdges,
	//                                        EdgeTypes:[T]})
	// become OutEdges[X] filtered by T — see EdgesOf below.
	OutEdges map[string][]knowledgev1.Edge
	InEdges  map[string][]knowledgev1.Edge
}

// newLogState constructs a logState from already-fetched node slices and
// edge slices. Callers own the fetch — this is the pure index-builder.
//
// labels + proxies are passed separately from templates/streams/chunks
// because the formatters never enumerate them but do need them resolvable
// in byID for ID-keyed peer hydration.
func newLogState(templates, streams, chunks, labels, proxies []*knowledgev1.Node, edges []knowledgev1.Edge) *logState {
	totalNodes := len(templates) + len(streams) + len(chunks) + len(labels) + len(proxies)
	st := &logState{
		Templates: templates,
		Streams:   streams,
		Chunks:    chunks,
		Labels:    labels,
		Proxies:   proxies,
		byID:      make(map[string]*knowledgev1.Node, totalNodes),
		OutEdges:  make(map[string][]knowledgev1.Edge),
		InEdges:   make(map[string][]knowledgev1.Edge),
	}
	for _, group := range [][]*knowledgev1.Node{templates, streams, chunks, labels, proxies} {
		for _, n := range group {
			st.byID[n.Id] = n
		}
	}
	for i := range edges {
		e := &edges[i]
		st.OutEdges[e.FromId] = append(st.OutEdges[e.FromId], copyEdge(e))
		st.InEdges[e.ToId] = append(st.InEdges[e.ToId], copyEdge(e))
	}
	return st
}

// NodeByID returns the node with id when present. The bool mirrors the
// QueryResult-empty case in the existing handler dispatch shape.
func (st *logState) NodeByID(id string) (*knowledgev1.Node, bool) {
	if st == nil {
		return nil, false
	}
	n, ok := st.byID[id]
	return n, ok
}

// EdgesOf returns the slice of edges adjacent to nodeID in the requested
// direction, optionally narrowed by edgeTypes. Empty edgeTypes returns
// every adjacent edge (matches IterEdges' "any-type" semantics). The
// returned slice is freshly allocated when a type filter is applied;
// callers must not assume aliasing.
func (st *logState) EdgesOf(nodeID string, direction kgwire.EdgeDirection, edgeTypes []kgtypes.EdgeType) []knowledgev1.Edge {
	if st == nil {
		return nil
	}
	var src []knowledgev1.Edge
	switch direction {
	case kgwire.OutgoingEdges:
		src = st.OutEdges[nodeID]
	case kgwire.IncomingEdges:
		src = st.InEdges[nodeID]
	default:
		return nil
	}
	if len(edgeTypes) == 0 {
		return src
	}
	out := make([]knowledgev1.Edge, 0, len(src))
	for i := range src {
		e := &src[i]
		if slices.Contains(edgeTypes, kgtypes.EdgeType(e.Type)) {
			out = append(out, copyEdge(e))
		}
	}
	return out
}
