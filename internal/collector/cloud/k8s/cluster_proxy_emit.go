// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"log/slog"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// emitClusterLinkageClientSide appends a cross-graph cluster proxy +
// RUNS_IN_CLUSTER edges to result.Nodes / result.Edges when contextName
// names an EKS or AKS managed cluster.
//
// EKS branch: contextName parses as an EKS cluster ARN via
// parseEKSClusterARN. The full ARN is the proxy's foreign_id, the AWS
// account is the proxy account name. No ResolutionMap lookup needed.
//
// AKS branch: contextName is the bare cluster name (aksKubecontext at
// azure/aks.go returns *cluster.Name). The full ARM resource path is
// recorded by the Azure cascade dispatcher into cloud.ResolutionMap on
// ctx. We look it up here, then recover the subscription ID (account)
// from parts[2] of the ARM path.
//
// GKE is intentionally NOT handled here — the existing server-side
// resolveClusterLinkage at postpopulate_cluster.go covers GKE and stays
// in place. A future ticket can consolidate all three providers.
//
// The emitted proxy mirrors what the shared
// crossgraph.BuildCrossGraphProxy produces for the GraphCloud branch
// byte-for-byte, so re-collect is idempotent
// against any existing GKE-emitted proxy at the same ID.
//
// Failure modes are silent skips so k8s collection always continues.
// Non-EKS, non-AKS contextName: skip (no proxy added). AKS contextName
// with no ResolutionMap on ctx (k8s collector run directly, not via
// Azure cascade): skip and slog.Debug. AKS contextName but
// ResolutionMap.Lookup returns ok=false: skip and slog.Debug. ARM path
// malformed (missing /subscriptions/<sub>): skip and slog.Debug.
func emitClusterLinkageClientSide(ctx context.Context, contextName string, result *collectorwire.CollectResult) {
	if result == nil || contextName == "" {
		return
	}

	proxy, ok := buildManagedClusterProxy(ctx, contextName)
	if !ok {
		return
	}

	// Skip if a proxy at this exact ID already exists in result.Nodes —
	// prevents double-emission when callers invoke the helper twice.
	for _, n := range result.Nodes {
		if n.Id == proxy.Id {
			return
		}
	}

	result.Nodes = append(result.Nodes, proxy)
	result.Edges = append(result.Edges, buildClusterLinkageBatchEdges(result.Nodes, proxy.Id)...)
}

// buildManagedClusterProxy constructs the cluster proxy node for an
// EKS or AKS contextName. Returns ok=false for any other context shape.
func buildManagedClusterProxy(ctx context.Context, contextName string) (*knowledgev1.Node, bool) {
	if region, account, cluster, ok := parseEKSClusterARN(contextName); ok {
		clusterID := eksClusterARN(region, account, cluster)
		source := managedClusterSourceNode(cluster, "eks-cluster", "aws", region)
		return makeCloudProxy(account, clusterID, source), true
	}

	if proxy, ok := buildAKSClusterProxy(ctx, contextName); ok {
		return proxy, true
	}

	slog.Debug("emitClusterLinkageClientSide: contextName is not an EKS or AKS cluster, skipping",
		"context", contextName)
	return nil, false
}

// buildAKSClusterProxy looks up the AKS ARM resource path on the
// ResolutionMap installed by the Azure cascade dispatcher, then builds
// the proxy. Returns ok=false when the map is absent, the lookup misses,
// or the ARM path is malformed.
func buildAKSClusterProxy(ctx context.Context, contextName string) (*knowledgev1.Node, bool) {
	rm := cloud.ResolutionMapFrom(ctx)
	if rm == nil {
		slog.Debug("emitClusterLinkageClientSide: AKS contextName but no ResolutionMap on ctx; skipping",
			"context", contextName)
		return nil, false
	}
	armPath, ok := rm.Lookup(contextName)
	if !ok {
		slog.Debug("emitClusterLinkageClientSide: AKS contextName not found in ResolutionMap; skipping",
			"context", contextName)
		return nil, false
	}
	subscription, ok := parseAKSSubscriptionFromARMPath(armPath)
	if !ok {
		slog.Debug("emitClusterLinkageClientSide: AKS ARM path malformed; skipping",
			"context", contextName, "arm_path", armPath)
		return nil, false
	}
	source := managedClusterSourceNode(contextName, "Microsoft.ContainerService/managedClusters", "azure", "")
	return makeCloudProxy(subscription, armPath, source), true
}

// parseAKSSubscriptionFromARMPath extracts the subscription ID from an
// Azure Resource Manager path of the shape
// "/subscriptions/{sub}/resourceGroups/{rg}/providers/.../managedClusters/{name}".
// Returns ok=false for any string that does not start with
// "/subscriptions/" followed by a non-empty subscription segment.
func parseAKSSubscriptionFromARMPath(armPath string) (string, bool) {
	const prefix = "/subscriptions/"
	if !strings.HasPrefix(armPath, prefix) {
		return "", false
	}
	rest := armPath[len(prefix):]
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		rest = rest[:i]
	}
	if rest == "" {
		return "", false
	}
	return rest, true
}

// managedClusterSourceNode constructs the seed node passed to
// makeCloudProxy. The seed mirrors the source node that the
// crossgraph.BuildCrossGraphProxy GraphCloud branch reads — none of the fields
// are load-bearing for resolution (proxy keys off foreign_id), but
// matching them keeps the persisted proxy byte-identical.
func managedClusterSourceNode(cluster, resourceType, provider, region string) *knowledgev1.Node {
	summary := resourceType + " " + cluster
	if region != "" {
		summary += " in " + region
	}
	n := &knowledgev1.Node{
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: cluster,
		Source:     "cloud",
		Summary:    summary,
	}
	kgtypes.SetValue(n, "resource_type", resourceType)
	kgtypes.SetValue(n, "provider", provider)
	if region != "" {
		kgtypes.SetValue(n, "region", region)
	}
	return n
}

// makeCloudProxy mirrors the GraphCloud branch of
// crossgraph.BuildCrossGraphProxy byte-for-byte.
// The persisted proxy must be identical to what the server would have
// produced, so the server's AddNode upsert sees no diff against an
// existing proxy at the same ID.
func makeCloudProxy(account, clusterID string, source *knowledgev1.Node) *knowledgev1.Node {
	proxy := &knowledgev1.Node{
		Id:          "proxy:cloud:" + account + ":" + clusterID,
		Type:        string(kgtypes.NodeProxy),
		SymbolName:  source.SymbolName,
		FilePath:    source.FilePath,
		Description: source.Description,
		Language:    source.Language,
		Source:      "proxy:cloud:" + account,
	}
	if source.Summary != "" && proxy.Description == "" {
		proxy.Description = source.Summary
	}
	kgtypes.SetValue(proxy, "foreign_graph", string(kgtypes.GraphCloud))
	kgtypes.SetValue(proxy, "foreign_id", clusterID)
	kgtypes.SetValue(proxy, "account", account)
	kgtypes.SetValue(proxy, "foreign_type", source.Type)
	if rt := kgtypes.Value(source, "resource_type"); rt != "" {
		kgtypes.SetValue(proxy, "resource_type", rt)
	}
	if region := kgtypes.Value(source, "region"); region != "" {
		kgtypes.SetValue(proxy, "region", region)
	}
	if provider := kgtypes.Value(source, "provider"); provider != "" {
		kgtypes.SetValue(proxy, "provider", provider)
	}
	return proxy
}

// buildClusterLinkageBatchEdges is the BatchEdge-shaped sibling of the
// GKE buildClusterLinkageEdges helper at postpopulate_cluster.go. Given
// the full result.Nodes list and the proxy ID, it returns one
// RUNS_IN_CLUSTER BatchEdge per eligible resource.
//
// Skipped:
//   - The proxy itself (proxy → proxy self-loop)
//   - Any other NodeProxy in the result (cross-graph proxies belong to
//     their own graph and get their own edges there)
//   - Any node whose ID equals the proxy ID (defensive double-guard)
//
// Included: every other NodeCloudResource regardless of resource_type
// — the ticket resolution is that EVERY resource depends on the
// control plane.
func buildClusterLinkageBatchEdges(nodes []*knowledgev1.Node, proxyID string) []kgwire.BatchEdge {
	if proxyID == "" {
		return nil
	}
	edges := make([]kgwire.BatchEdge, 0, len(nodes))
	for _, n := range nodes {
		if n.Id == proxyID {
			continue
		}
		if kgtypes.NodeType(n.Type) == kgtypes.NodeProxy {
			continue
		}
		if kgtypes.NodeType(n.Type) != kgtypes.NodeCloudResource {
			continue
		}
		edges = append(edges, kgwire.BatchEdge{
			FromIdx: -1,
			ToIdx:   -1,
			FromID:  n.Id,
			ToID:    proxyID,
			Type:    kgtypes.EdgeRunsInCluster,
		})
	}
	return edges
}
