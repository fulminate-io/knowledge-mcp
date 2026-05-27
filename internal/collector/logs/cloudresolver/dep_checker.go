// SPDX-License-Identifier: Apache-2.0

package cloudresolver

import (
	"context"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/logs"
)

// maxDependencyHops caps the BFS traversal depth performed by
// depChecker.HasDependency. Cloud dependency graphs typically fan out
// aggressively (a VPC reaches hundreds of resources in 2-3 hops), so we
// bound the walk to prevent runaway work on pathological fixtures.
//
// Three hops is enough to connect "ECS service" → "task definition" →
// "target group" → "load balancer", which is the longest realistic chain we
// expect correlation logic to care about. Anything further away is not a
// meaningful "dependency" in the correlation sense.
//
// Cross-graph proxy traversals are TRANSPARENT: resolving a proxy into
// its target graph counts as zero additional hops. A workload that
// reaches the cluster proxy in one hop and a sibling workload back
// through the same proxy counts as two semantic hops, not three —
// proxies are pointers, not relationships.
const maxDependencyHops = 3

// depChecker implements logs.DependencyChecker by walking cloud graphs
// (plural) to find reachability between two ResolvedResource addresses
// against an in-memory CloudSubgraph slice (produced by
// IngestService.FetchCloudSubgraph).
//
// The walker is multi-graph: when it encounters a cross-graph proxy
// during neighbor expansion it follows the proxy into its target cloud
// graph and continues BFS from there. The classic same-account path
// (both endpoints share an Account) is a degenerate case of the
// multi-graph walk where no proxies are ever resolved.
//
// Safe for concurrent use: the checker holds no mutable state and the
// CloudSubgraph is read-only after construction.
type depChecker struct {
	subgraph *CloudSubgraph
}

// Compile-time interface check — renames to logs.DependencyChecker will
// fail the build rather than silently at runtime.
var _ logs.DependencyChecker = (*depChecker)(nil)

// NewDependencyChecker returns a logs.DependencyChecker that performs
// bounded reachability queries across whichever cloud graphs the
// supplied CloudSubgraph holds. A nil sg yields a checker that reports
// "no dependency" for every query without panicking.
func NewDependencyChecker(sg *CloudSubgraph) logs.DependencyChecker {
	return &depChecker{subgraph: sg}
}

// HasDependency reports whether resourceA reaches resourceB within
// maxDependencyHops hops across one or more cloud graphs. "Dependency"
// is intentionally broad: the correlation layer wants to gate on
// "these two resources are related at all" rather than on a specific
// edge semantics.
//
// Cross-graph reachability is handled by following cross-graph proxy
// edges transparently. When the BFS encounters a NodeProxy it resolves
// the proxy to its target graph and continues expansion from the
// target node — the proxy hop itself does NOT consume a dependency
// hop. This is what makes "workload → cluster_proxy → workload" score
// as two semantic hops instead of three.
//
// Returns false on empty inputs, a missing starting graph, or when the
// walk terminates without finding the target — callers treat a false
// result as "uncorroborated" rather than "definitely independent".
func (c *depChecker) HasDependency(ctx context.Context, a, b logs.ResolvedResource) bool {
	if a.ID == "" || b.ID == "" {
		return false
	}
	if a == b {
		return true
	}
	if c.subgraph == nil {
		return false
	}
	if !c.subgraph.hasGraph(a.Account) {
		return false
	}
	return bfsReaches(ctx, c.subgraph, graphKeyFromResource(a), graphKeyFromResource(b), maxDependencyHops)
}

// hasGraph reports whether the named cloud graph is loaded in the
// subgraph. Used by depChecker to fail-fast when the starting account
// isn't materialized — equivalent to the original retrieveCloudDB miss.
func (sg *CloudSubgraph) hasGraph(name string) bool {
	if sg == nil {
		return false
	}
	_, ok := sg.slices[name]
	return ok
}

// graphKeyFromResource constructs the visited-set key from a
// ResolvedResource.
func graphKeyFromResource(r logs.ResolvedResource) graphKey {
	return graphKey{account: r.Account, id: r.ID}
}
