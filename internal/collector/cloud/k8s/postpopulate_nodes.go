// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"log/slog"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
)

// resolveNodeVMLinkage wires every K8s Node in the cloud graph to a
// cross-graph proxy of the cloud VM it runs on. Mirrors the resolver
// pattern from postpopulate_cluster.go:
//
//  1. Query every NodeCloudResource with resource_type=Node.
//  2. For each Node, parse Spec.ProviderID (preserved as metadata in
//     sub_nodes.go) to determine the target cloud provider, account,
//     and resource ID.
//  3. For AWS, the providerID does not carry the account — recover it
//     from the enclosing graph name (EKS kubecontexts arrive as the
//     cluster ARN per cloud/aws/eks.go:93).
//  4. Create a cross-graph proxy in this graph via
//     crossgraph.BuildCrossGraphProxy and emit one EdgeBackedByVM edge
//     from the Node to the proxy.
//
// Runs AFTER resolveClusterLinkage inside postPopulate (OQ2 decision):
// both resolvers are independent, but grouping them keeps cross-graph
// linkage contiguous.
func resolveNodeVMLinkage(ctx context.Context, gc postpopulate.GraphCaller, graphName string) error {
	nodes, err := postpopulate.BrowseNodes(ctx, gc, kgtypes.GraphCloud, graphName, k8sResourceQuery("Node"))
	if err != nil {
		return err
	}
	if len(nodes) == 0 {
		return nil
	}

	proxies := newProxyAccumulator()
	edges := make([]knowledgev1.Edge, 0, len(nodes))
	var skipped int
	for _, n := range nodes {
		next, appended, err := buildNodeVMProxy(edges, n, graphName, proxies)
		if err != nil {
			return err
		}
		edges = next
		if !appended {
			skipped++
		}
	}

	if skipped > 0 {
		slog.Debug("postPopulate: Node→VM linkage skipped nodes",
			"skipped", skipped, "total", len(nodes))
	}
	if len(edges) == 0 {
		return nil
	}
	if err := postpopulate.LinkNodesAndEdgesBatch(ctx, gc, kgtypes.GraphCloud, graphName, proxies.nodes(), edges); err != nil {
		slog.Debug("postPopulate: failed to create BACKED_BY_VM edges",
			"count", len(edges), "err", err)
		return err
	}
	slog.Debug("postPopulate: created BACKED_BY_VM edges", "count", len(edges))
	return nil
}

// buildNodeVMProxy constructs the proxy for a single Node and appends the
// BACKED_BY_VM edge that links the Node to it to out, returning the grown
// slice. appended=false (with no error) for Nodes whose providerID is
// missing, malformed, or whose AWS account cannot be recovered from the
// graph name — those are operational gaps, not bugs, and the caller logs
// an aggregated count. The edge is built as a fresh knowledgev1.Edge literal at
// the append site: a populated knowledgev1.Edge value embeds a proto lock, so
// any by-value copy (return + append) would trip copylocks.
func buildNodeVMProxy(out []knowledgev1.Edge, n *knowledgev1.Node, graphName string, proxies *proxyAccumulator) ([]knowledgev1.Edge, bool, error) {
	providerID := kgtypes.Value(n, "provider_id")
	if providerID == "" {
		return out, false, nil
	}

	target, ok := parseProviderID(providerID)
	if !ok {
		slog.Debug("postPopulate: unrecognized providerID",
			"node", n.Id, "provider_id", providerID)
		return out, false, nil
	}

	if target.Provider == "aws" && target.Account == "" {
		_, account, _, arnOK := parseEKSClusterARN(graphName)
		if !arnOK {
			slog.Debug("postPopulate: cannot resolve AWS account for Node — graph name is not an EKS cluster ARN",
				"node", n.Id, "graph", graphName)
			return out, false, nil
		}
		target = finalizeAWSTarget(target, account)
	}

	if target.Account == "" || target.ID == "" {
		slog.Debug("postPopulate: skipping Node — target missing account or ID after parse",
			"node", n.Id, "provider", target.Provider)
		return out, false, nil
	}

	source := vmProxySource(target)
	proxyID, err := proxies.proxy(&knowledgev1.ProxyTarget{
		GraphType: string(kgtypes.GraphCloud),
		Name:      target.Account,
		NodeId:    target.ID,
	}, source)
	if err != nil {
		return out, false, err
	}

	return append(out, knowledgev1.Edge{
		FromId: n.Id,
		ToId:   proxyID,
		Type:   string(kgtypes.EdgeBackedByVM),
	}), true, nil
}

// vmProxySource builds the proxy-source Node carrying the display
// fields (SymbolName, resource_type, provider, region) so the proxy is
// readable without resolving the upstream graph. None of the fields
// are load-bearing for proxy resolution — that keys off foreign_id.
func vmProxySource(target providerVMTarget) *knowledgev1.Node {
	name := lastPathSegment(target.ID)
	n := &knowledgev1.Node{
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: name,
		Source:     "cloud",
		Summary:    target.ResourceType + " " + name,
	}
	kgtypes.SetValue(n, "resource_type", target.ResourceType)
	kgtypes.SetValue(n, "provider", target.Provider)
	if target.Region != "" {
		kgtypes.SetValue(n, "region", target.Region)
	}
	return n
}

// lastPathSegment returns the final "/..." segment of an ID, used as a
// human-friendly display name on the proxy. For a GCE selfLink this is
// the instance name; for an AWS ARN it is "instance/<id>"; for an
// Azure resource ID it is the VM name. Returns the whole input when
// there is no "/" (defensive — not expected).
func lastPathSegment(id string) string {
	if id == "" {
		return ""
	}
	i := strings.LastIndex(id, "/")
	if i < 0 || i == len(id)-1 {
		return id
	}
	return id[i+1:]
}
