// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"log/slog"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
)

// resolveNamespaceMembership creates IN_NAMESPACE edges from every namespaced
// cloud resource (meta["namespace"] != "" and resource_type != "Namespace") to
// its owning Namespace node. Structural membership only — no Method/metadata on
// the emitted edges.
//
// Runs as part of postPopulate. Missing Namespace targets are skipped silently
// (the collector may have partially failed; we prefer no edge over a dangling
// edge). This mirrors the defensive posture of resolveServiceSelectors, which
// similarly does not synthesize partner nodes.
func resolveNamespaceMembership(ctx context.Context, gc postpopulate.GraphCaller, graphName string) error {
	// Load all cloud resources in one pass. We need the full list because
	// any NodeCloudResource with a non-empty namespace metadata key is a
	// candidate — there is no single resource_type filter that captures
	// every namespaced kind (Pod, Service, Deployment, ConfigMap, Secret,
	// Ingress, NetworkPolicy, PDB, HPA, namespaced CRDs, ...).
	resources, err := postpopulate.BrowseAllNodes(ctx, gc, kgtypes.GraphCloud, graphName, map[string]any{
		"type": string(kgtypes.NodeCloudResource),
	})
	if err != nil {
		return err
	}

	// Load existing Namespace nodes separately so we can skip edges whose
	// target doesn't exist. IDs use resourceID("", "Namespace", name) =
	// "Namespace/<name>", so we index by the same ID we'd emit.
	nsNodes, err := postpopulate.BrowseAllNodes(ctx, gc, kgtypes.GraphCloud, graphName, k8sResourceQuery("Namespace"))
	if err != nil {
		return err
	}
	nsSet := make(map[string]bool, len(nsNodes))
	for _, n := range nsNodes {
		nsSet[n.Id] = true
	}

	edges, skipped := buildNamespaceMembershipEdges(resources, nsSet)

	if skipped > 0 {
		slog.Debug("postPopulate: skipped IN_NAMESPACE edges for missing Namespace targets",
			"count", skipped)
	}
	if len(edges) == 0 {
		return nil
	}
	if err := postpopulate.LinkEdgesBatch(ctx, gc, kgtypes.GraphCloud, graphName, edges); err != nil {
		slog.Debug("postPopulate: failed to create IN_NAMESPACE edges",
			"count", len(edges), "err", err)
		return err
	}
	slog.Debug("postPopulate: created IN_NAMESPACE edges", "count", len(edges))
	return nil
}

// buildNamespaceMembershipEdges is the pure (testable) core. It emits an
// IN_NAMESPACE edge for every
// non-Namespace resource carrying a namespace value. nsSet gates the
// target: edges whose Namespace node does not exist are dropped (the
// caller logs the count). Filtering at construction keeps the returned
// slice a single run of fresh knowledgev1.Edge literals — appending an existing
// value would copy the embedded proto lock (copylocks).
func buildNamespaceMembershipEdges(resources []*knowledgev1.Node, nsSet map[string]bool) (edges []knowledgev1.Edge, skipped int) {
	edges = make([]knowledgev1.Edge, 0, len(resources))
	for _, node := range resources {
		if kgtypes.Value(node, "resource_type") == "Namespace" {
			continue
		}
		ns := kgtypes.Value(node, "namespace")
		if ns == "" {
			continue
		}
		toID := resourceID("", "Namespace", ns)
		if !nsSet[toID] {
			skipped++
			continue
		}
		edges = append(edges, knowledgev1.Edge{
			FromId: node.Id,
			ToId:   toID,
			Type:   string(kgtypes.EdgeInNamespace),
		})
	}
	return edges, skipped
}
