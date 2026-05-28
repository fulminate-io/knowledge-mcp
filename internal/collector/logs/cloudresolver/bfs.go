// SPDX-License-Identifier: Apache-2.0

package cloudresolver

import (
	"context"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// bfsReaches performs a bounded breadth-first walk starting at `start`
// across outgoing AND incoming edges (and across cross-graph proxy
// boundaries), returning true if target is encountered within maxHops
// hops. Edge types are not filtered — dependencies in the cloud graph
// cover many relationships (DEPENDS_ON, CONNECTS_TO, USES, SINK,
// PROTECTS, RUNS_IN_CLUSTER, ...) and the correlation layer treats any
// of them as a real relationship.
func bfsReaches(
	ctx context.Context,
	sg *CloudSubgraph,
	start graphKey,
	target graphKey,
	maxHops int,
) bool {
	if maxHops <= 0 {
		return false
	}
	visited := map[graphKey]struct{}{start: {}}
	frontier := []graphKey{start}

	for hop := 0; hop < maxHops && len(frontier) > 0; hop++ {
		next, found := expandFrontier(ctx, sg, frontier, visited, target)
		if found {
			return true
		}
		frontier = next
	}
	return false
}

// expandFrontier walks one BFS layer, yielding the next frontier and a
// "found" flag. Proxy hops are transparent: when a neighbor is a proxy
// we resolve it and continue expanding from the target node in the
// SAME layer, consuming no hop budget.
func expandFrontier(
	ctx context.Context,
	sg *CloudSubgraph,
	frontier []graphKey,
	visited map[graphKey]struct{},
	target graphKey,
) ([]graphKey, bool) {
	var next []graphKey
	for _, fn := range frontier {
		for _, neighbor := range expandNeighbors(ctx, sg, fn, visited) {
			if neighbor == target {
				return nil, true
			}
			next = append(next, neighbor)
		}
	}
	return next, false
}

// expandNeighbors returns every (cross-graph) neighbor reachable from
// fn in a single semantic hop. It iterates edges of fn in its own
// graph; each peer is categorized:
//
//   - Plain peer: marked visited and returned as a one-hop neighbor.
//   - Cross-graph proxy peer: marked visited and EXPANDED TRANSPARENTLY
//     — the proxy's own outbound edges in the current graph AND the
//     proxy's target node (in another cloud graph) all become one-hop
//     neighbors of fn, recursively. This is the "proxies are pointers,
//     not semantic hops" invariant: a workload reaching the cluster
//     proxy is considered a direct neighbor of every other workload
//     sharing that proxy, AND of the parent graph's cluster node.
//
// Transparency does NOT traverse unbounded chains: visited-set
// bookkeeping stops us from re-entering the same proxy or target
// node, so a cycle through proxies terminates after one visit per
// unique (account, id).
func expandNeighbors(
	ctx context.Context,
	sg *CloudSubgraph,
	fn graphKey,
	visited map[graphKey]struct{},
) []graphKey {
	var out []graphKey
	collectNeighbors(ctx, sg, fn, visited, &out)
	return out
}

// collectNeighbors walks one layer of fn's edges and appends
// one-hop-equivalent neighbors to out. When a peer is a cloud proxy
// we recurse through it transparently — the proxy's own peers and its
// resolved target are folded in at the same layer.
func collectNeighbors(
	ctx context.Context,
	sg *CloudSubgraph,
	fn graphKey,
	visited map[graphKey]struct{},
	out *[]graphKey,
) {
	for _, e := range sg.Edges(fn.account, fn.id) {
		peerID := edgePeer(e, fn.id)
		if peerID == "" || peerID == fn.id {
			continue
		}
		peerKey := graphKey{account: fn.account, id: peerID}
		if _, seen := visited[peerKey]; seen {
			continue
		}
		visited[peerKey] = struct{}{}

		peerNode, ok := fetchNode(sg, fn.account, peerID)
		if !ok {
			// Dangling edge: expose the peer so the target check still
			// has a chance if the target ID equals peerID.
			*out = append(*out, peerKey)
			continue
		}
		if !isProxy(peerNode) || isBranchProxy(peerNode) {
			*out = append(*out, peerKey)
			continue
		}
		// Proxy: fold in its same-graph peers and its resolved target
		// without consuming a hop.
		expandThroughProxy(ctx, sg, peerNode, peerKey, visited, out)
	}
}

// expandThroughProxy transparently folds a cloud proxy into fn's
// one-hop neighbor list. Two things happen at the same semantic layer:
//
//  1. The proxy's OWN edges in fn's graph are walked, so every other
//     resource linked to the proxy (e.g., a sibling workload sharing
//     the cluster proxy) becomes a direct neighbor of fn.
//  2. The proxy's resolved target (in a potentially different cloud
//     graph) is added as a neighbor, so BFS can continue into the
//     target graph from the next hop onward.
//
// Non-cloud proxies (code, practice, knowledge, branch) are dropped —
// they cannot host cloud dependency chains and following them would
// pull the walker outside the cloud-graph universe.
func expandThroughProxy(
	ctx context.Context,
	sg *CloudSubgraph,
	proxyNode *knowledgev1.Node,
	proxyKey graphKey,
	visited map[graphKey]struct{},
	out *[]graphKey,
) {
	// 1. Recurse into the proxy's peers in fn's own graph.
	collectNeighbors(ctx, sg, proxyKey, visited, out)

	// 2. Resolve the proxy and fold in its target node.
	targetKey, ok := resolveCloudProxy(sg, proxyNode)
	if !ok {
		return
	}
	if _, seen := visited[targetKey]; seen {
		return
	}
	visited[targetKey] = struct{}{}
	*out = append(*out, targetKey)
}

// resolveCloudProxy resolves a proxy node to its target's (account, id)
// address. Only cloud proxies whose target graph is loaded in the
// subgraph are followed — version overlay proxies, practice proxies,
// and code proxies cannot host cloud dependency chains so we drop them
// to preserve the invariant that the walker stays inside cloud graphs.
//
// The cloud-proxy metadata convention (foreign_graph="cloud",
// account=<graph>, foreign_id=<node>) is stamped by
// store.BuildCrossGraphProxy's cloud branch; we read it directly off the
// wire node via kgtypes.Value rather than the store-typed store.ProxyInfo
// switch — only the cloud case is meaningful here.
func resolveCloudProxy(sg *CloudSubgraph, proxy *knowledgev1.Node) (graphKey, bool) {
	if kgtypes.Value(proxy, "foreign_graph") != string(kgtypes.GraphCloud) {
		return graphKey{}, false
	}
	account := kgtypes.Value(proxy, "account")
	nodeID := kgtypes.Value(proxy, "foreign_id")
	if account == "" || nodeID == "" {
		return graphKey{}, false
	}
	if !sg.hasGraph(account) {
		return graphKey{}, false
	}
	return graphKey{account: account, id: nodeID}, true
}

// fetchNode loads a single node by (account, id) from the in-memory
// subgraph. Returns ok=false when the node is missing — BFS continues
// rather than treating a dangling edge as an error.
func fetchNode(sg *CloudSubgraph, account, id string) (*knowledgev1.Node, bool) {
	return sg.Node(account, id)
}

// isProxy reports whether n is a proxy node (a lightweight cross-graph
// pointer). Wire-node twin of store.IsProxy over the *knowledgev1.Node
// carrier — a plain Type-field check.
func isProxy(n *knowledgev1.Node) bool {
	return kgtypes.NodeType(n.Type) == kgtypes.NodeProxy
}

// isBranchProxy reports whether n is a branch overlay proxy (foreign_graph
// ="main"), which the BFS must NOT walk transparently. Wire-node twin of
// store.IsBranchProxy.
func isBranchProxy(n *knowledgev1.Node) bool {
	return kgtypes.NodeType(n.Type) == kgtypes.NodeProxy && kgtypes.Value(n, "foreign_graph") == "main"
}

// edgePeer returns the "other" endpoint of an edge given one known
// endpoint. BothEdges iteration yields edges in both directions so we
// must handle either FromId or ToId being the known side.
func edgePeer(e *knowledgev1.Edge, known string) string {
	if e.FromId == known {
		return e.ToId
	}
	if e.ToId == known {
		return e.FromId
	}
	return ""
}
