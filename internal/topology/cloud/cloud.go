// SPDX-License-Identifier: Apache-2.0

// Package cloud holds the cloud-graph topology analyzer family: the
// event-chain tracer, the serverless dependency-depth analyzer, the
// monitoring-coverage gap detector, the certificate-expiry analyzer, the
// cross-provider blast-radius tracer, and the rule-table orphan detector.
//
// Every analyzer in this package reads its nodes and edges over the wire
// through the foundation GraphCaller seam — there is no in-process store.
// The package follows the same store-free layering rule as its parent: it
// depends only on the foundation scaffolding (Finding / Severity / Request /
// Analyzer / Register + the shared wire read-helpers), the wire proto
// vocabulary (gen/knowledge/v1 + pkg/kgtypes), and the standard library.
//
// Each analyzer's ALGORITHM is preserved verbatim from the prior store-backed
// implementation — only the data-access layer changes. Where the prior code
// did a per-node edge query inside a BFS or a presence check, this package
// fetches the relevant edge set in ONE bulk foundation.FetchEdges call and
// builds an in-memory adjacency / presence index, then walks it. This keeps
// the analyzer outputs byte-stable while honoring the one-fetch-no-N+1 rule.
package cloud

import (
	"strconv"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// metaValue reads a metadata key off a wire node, returning "" when the node
// or its metadata map is nil or the key is absent. It is the wire twin of the
// store node's Value(key) accessor the prior analyzers used.
func metaValue(n *knowledgev1.Node, key string) string {
	if n == nil || n.Metadata == nil {
		return ""
	}
	return n.Metadata[key]
}

// displayName returns the most user-recognizable label for a cloud node.
// Prefers SymbolName (e.g. "web-deployment"); falls back to ID for nodes that
// lack a symbol name (proxies, malformed records). Wire twin of the prior
// store-node displayName.
func displayName(n *knowledgev1.Node) string {
	if n == nil {
		return ""
	}
	if n.SymbolName != "" {
		return n.SymbolName
	}
	return n.Id
}

// primaryEvidence returns the first evidence node ID for a Finding, or "" if
// Evidence is empty. Used as the dedup discriminator alongside the analyzer
// name and as a stable tie-break in the per-analyzer sort comparators.
func primaryEvidence(f foundation.Finding) string {
	if len(f.Evidence) == 0 {
		return ""
	}
	return f.Evidence[0]
}

// extractExtraInt reads an integer knob from Request.Extra or returns the
// default. Values outside [1, maxVal] fall through to the default so a bad
// input never silently breaks an analyzer. The lower bound is fixed at 1 —
// every knob the cloud analyzers expose rejects zero/negative values. This
// preserves the exact clamp semantics of the prior topology helper.
func extractExtraInt(extra map[string]string, key string, def, maxVal int) int {
	raw, ok := extra[key]
	if !ok {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 1 || v > maxVal {
		return def
	}
	return v
}

// edgeIndex is an in-memory directed-edge index built once from a bulk
// foundation.FetchEdges over a node set. It answers the presence / adjacency
// questions the prior analyzers asked the scoped store via per-node IterEdges,
// without a per-node fan-out: one bulk fetch populates the whole index and
// every subsequent lookup is a map read.
//
// out[from][edgeType] holds the set of to-IDs reachable from `from` via an
// edge of that type; in[to][edgeType] holds the set of from-IDs that point at
// `to` via that type. outAny[from] records whether `from` has any outgoing
// edge at all (the "no outbound edges of any kind" orphan rules use this).
type edgeIndex struct {
	out    map[string]map[kgtypes.EdgeType]map[string]bool
	in     map[string]map[kgtypes.EdgeType]map[string]bool
	outAny map[string]bool
}

// newEdgeIndex builds an edgeIndex from a value slice of wire edges. The edges
// are ranged by index (not by value) because knowledgev1.Edge embeds a proto
// message-state mutex — ranging by value would trip copylocks.
func newEdgeIndex(edges []knowledgev1.Edge) *edgeIndex {
	idx := &edgeIndex{
		out:    make(map[string]map[kgtypes.EdgeType]map[string]bool),
		in:     make(map[string]map[kgtypes.EdgeType]map[string]bool),
		outAny: make(map[string]bool),
	}
	for i := range edges {
		e := &edges[i]
		et := kgtypes.EdgeType(e.Type)
		insertEdge(idx.out, e.FromId, et, e.ToId)
		insertEdge(idx.in, e.ToId, et, e.FromId)
		idx.outAny[e.FromId] = true
	}
	return idx
}

// insertEdge adds peer into table[key][edgeType], allocating the nested maps
// on first use.
func insertEdge(table map[string]map[kgtypes.EdgeType]map[string]bool, key string, et kgtypes.EdgeType, peer string) {
	byType, ok := table[key]
	if !ok {
		byType = make(map[kgtypes.EdgeType]map[string]bool)
		table[key] = byType
	}
	peers, ok := byType[et]
	if !ok {
		peers = make(map[string]bool)
		byType[et] = peers
	}
	peers[peer] = true
}

// hasOutgoing reports whether nodeID has at least one outgoing edge of et.
func (idx *edgeIndex) hasOutgoing(nodeID string, et kgtypes.EdgeType) bool {
	return len(idx.out[nodeID][et]) > 0
}

// hasIncoming reports whether nodeID has at least one incoming edge of et.
func (idx *edgeIndex) hasIncoming(nodeID string, et kgtypes.EdgeType) bool {
	return len(idx.in[nodeID][et]) > 0
}

// hasAnyOutgoing reports whether nodeID has at least one outgoing edge of any
// type.
func (idx *edgeIndex) hasAnyOutgoing(nodeID string) bool {
	return idx.outAny[nodeID]
}

// outgoing returns the set of to-IDs reachable from nodeID via any of the
// given edge types, deduplicated. Used by the BFS analyzers to expand one
// frontier layer in-memory.
func (idx *edgeIndex) outgoing(nodeID string, edgeTypes []kgtypes.EdgeType) []string {
	byType := idx.out[nodeID]
	if byType == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, et := range edgeTypes {
		for to := range byType[et] {
			if seen[to] {
				continue
			}
			seen[to] = true
			out = append(out, to)
		}
	}
	return out
}

// incomingCount returns the number of distinct from-IDs that point at nodeID
// via et. Used by cert_expiry to count USES_CERT dependents.
func (idx *edgeIndex) incomingCount(nodeID string, et kgtypes.EdgeType) int {
	return len(idx.in[nodeID][et])
}

// incomingFrom returns the set of from-IDs that point at nodeID via et. Used
// by the dead-workflow orphan rule to inspect each inbound edge's source
// node (it must confirm the source is a workflow_run).
func (idx *edgeIndex) incomingFrom(nodeID string, et kgtypes.EdgeType) []string {
	peers := idx.in[nodeID][et]
	if len(peers) == 0 {
		return nil
	}
	out := make([]string, 0, len(peers))
	for from := range peers {
		out = append(out, from)
	}
	return out
}
