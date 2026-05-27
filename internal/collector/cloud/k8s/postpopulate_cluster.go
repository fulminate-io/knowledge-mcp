// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"log/slog"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
)

// resolveClusterLinkage wires every cloud resource in a GKE-shaped cloud
// graph to a proxy of its owning GCP Cluster node. Runs as part of
// postPopulate, right after namespace membership resolution.
//
// The flow:
//  1. Identify this graph via db.WriteTargetGraphName. If it isn't a
//     cloud/{gke_*} graph the resolver is a silent no-op — non-GKE
//     Kubernetes contexts (EKS, AKS, plain kubeconfigs) get their own
//     postPopulate in follow-up tickets.
//  2. Parse the graph name into (project, region, cluster) via
//     parseGKEGraphName. A failure here means the name does not follow
//     the gke_{project}_{region}_{cluster} convention — also a no-op.
//  3. Create a cross-graph proxy of the GCP Cluster node in THIS graph
//     via crossgraph.BuildCrossGraphProxy. The proxy is idempotent so
//     repeat runs reuse the same proxy ID.
//  4. Emit one RUNS_IN_CLUSTER edge from every cloud resource in the
//     graph (except the proxy itself and any other pre-existing
//     cross-graph proxies) to the cluster proxy.
//
// The scope decision — "every resource, not just workloads" — follows
// the ticket OQ#1 resolution: every K8s resource depends on the control
// plane, so the cluster is a universally-correct parent. Filtering is
// done at query time (NodeCloudResource only) plus a type guard that
// skips NodeProxy to avoid proxy→proxy self-loops.
func resolveClusterLinkage(ctx context.Context, gc postpopulate.GraphCaller, graphName string) error {
	project, region, cluster, ok := parseGKEGraphName(graphName)
	if !ok {
		slog.Debug("postPopulate: graph is not GKE-shaped, skipping cluster linkage")
		return nil
	}

	selfLink := gkeClusterSelfLink(project, region, cluster)
	source := clusterProxySourceNode(cluster, region)

	proxies := newProxyAccumulator()
	proxyID, err := proxies.proxy(&knowledgev1.ProxyTarget{
		GraphType: string(kgtypes.GraphCloud),
		Name:      project,
		NodeId:    selfLink,
	}, source)
	if err != nil {
		return err
	}

	resources, err := postpopulate.BrowseNodes(ctx, gc, kgtypes.GraphCloud, graphName, map[string]any{
		"type":  string(kgtypes.NodeCloudResource),
		"limit": 0,
	})
	if err != nil {
		return err
	}

	edges := buildClusterLinkageEdges(resources, proxyID)
	if len(edges) == 0 {
		slog.Debug("postPopulate: no RUNS_IN_CLUSTER edges to emit",
			"project", project, "region", region, "cluster", cluster)
		return nil
	}

	// Cluster proxy node + RUNS_IN_CLUSTER edges land in ONE create_batch.
	if err := postpopulate.LinkNodesAndEdgesBatch(ctx, gc, kgtypes.GraphCloud, graphName, proxies.nodes(), edges); err != nil {
		slog.Debug("postPopulate: failed to create RUNS_IN_CLUSTER edges",
			"count", len(edges), "err", err)
		return err
	}
	slog.Debug("postPopulate: created RUNS_IN_CLUSTER edges",
		"count", len(edges), "cluster", cluster, "project", project)
	return nil
}

// clusterProxySourceNode constructs the Node that BuildCrossGraphProxy
// uses to seed the proxy's display fields. None of the fields are
// load-bearing for resolution (that goes by foreign_id), so we keep
// this minimal and let ProxyInfo pick up the metadata we set.
func clusterProxySourceNode(cluster, region string) *knowledgev1.Node {
	summary := "gcp:container:cluster " + cluster
	if region != "" {
		summary += " in " + region
	}
	n := &knowledgev1.Node{
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: cluster,
		Source:     "cloud",
		Summary:    summary,
	}
	kgtypes.SetValue(n, "resource_type", "gcp:container:cluster")
	kgtypes.SetValue(n, "region", region)
	kgtypes.SetValue(n, "provider", "gcp")
	return n
}

// buildClusterLinkageEdges is the pure (testable) core. Given the full
// NodeCloudResource list and the cluster proxy's ID, it returns one
// RUNS_IN_CLUSTER edge per eligible resource.
//
// Skipped:
//   - The proxy itself (proxy→proxy self-loop)
//   - Any other cross-graph proxy (NodeProxy type). We don't want a
//     Service's proxy to also gain a RUNS_IN_CLUSTER edge — the real
//     resource lives in its own graph and will get its own edge there.
//   - Any node whose ID equals the proxy ID (defensive double-guard)
//
// Included:
//   - Every other NodeCloudResource regardless of resource_type. The
//     ticket resolution is that EVERY resource depends on the control
//     plane — Namespaces, Services, ConfigMaps, CRDs, the lot — so
//     there is no allowlist, just an "is a proxy?" guard.
func buildClusterLinkageEdges(resources []*knowledgev1.Node, proxyID string) []knowledgev1.Edge {
	if proxyID == "" {
		return nil
	}
	edges := make([]knowledgev1.Edge, 0, len(resources))
	for _, node := range resources {
		if node.Id == proxyID {
			continue
		}
		if kgtypes.NodeType(node.Type) == kgtypes.NodeProxy {
			continue
		}
		edges = append(edges, knowledgev1.Edge{
			FromId: node.Id,
			ToId:   proxyID,
			Type:   string(kgtypes.EdgeRunsInCluster),
		})
	}
	return edges
}
