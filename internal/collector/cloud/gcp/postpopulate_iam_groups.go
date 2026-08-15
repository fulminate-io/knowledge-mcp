// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"
	"log/slog"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
)

const methodGCPIAMGroupResolve = "gcp-iam-group-resolve"

// resolveIAMBindingGroups scans EdgeGrants edges in the GCP graph and resolves
// "group:{email}" targets to actual group node IDs. For each matching edge,
// a new EdgeGrants edge is created from the role to the resolved group node
// and the original raw-target edge is removed so the graph holds only
// resolved linkage (no permanently dangling group:{email} placeholders).
func resolveIAMBindingGroups(ctx context.Context, gc postpopulate.GraphCaller, graphName string) error {
	// Build email→nodeID map from all group nodes.
	groupIndex := buildGroupIndex(ctx, gc, graphName)
	if len(groupIndex) == 0 {
		return nil // no groups in graph — nothing to resolve
	}

	// Find all nodes that have outgoing GRANTS edges to "group:" targets.
	// These are role strings used as node IDs by the IAM subcollector.
	var resolved []knowledgev1.Edge
	var stale []knowledgev1.Edge
	visited := make(map[string]bool) // dedup by "fromID:groupNodeID"

	// Scan all cloud resource nodes for outgoing GRANTS edges to group: targets.
	nodes, err := postpopulate.BrowseAllNodes(ctx, gc, kgtypes.GraphCloud, graphName, map[string]any{
		"type": string(kgtypes.NodeCloudResource),
	})
	if err != nil {
		return err
	}

	for _, node := range nodes {
		add, drop := resolveGroupEdgesForNode(ctx, gc, graphName, node.Id, groupIndex, visited)
		resolved = append(resolved, add...)
		stale = append(stale, drop...)
	}

	// Also check role-string nodes (they may not be NodeCloudResource).
	add, drop := resolveGroupEdgesFromRoles(ctx, gc, graphName, groupIndex, visited)
	resolved = append(resolved, add...)
	stale = append(stale, drop...)

	if err := postpopulate.LinkEdgesBatch(ctx, gc, kgtypes.GraphCloud, graphName, resolved); err != nil {
		return err
	}
	if err := postpopulate.UnlinkEdgesBatch(ctx, gc, kgtypes.GraphCloud, graphName, stale); err != nil {
		return err
	}

	slog.Debug("gcp iam-group-resolve: resolved group bindings",
		"resolved", len(resolved), "removed_placeholders", len(stale))
	return nil
}

// buildGroupIndex queries all Cloud Identity group nodes and returns an
// email→nodeID map for lookups.
func buildGroupIndex(ctx context.Context, gc postpopulate.GraphCaller, graphName string) map[string]string {
	groups, err := postpopulate.BrowseAllNodes(ctx, gc, kgtypes.GraphCloud, graphName, map[string]any{
		"type": string(kgtypes.NodeCloudResource),
		"meta": map[string]string{"resource_type": "gcp:cloudidentity:group"},
	})
	if err != nil || len(groups) == 0 {
		return nil
	}

	index := make(map[string]string, len(groups))
	for _, g := range groups {
		email := kgtypes.Value(g, "email")
		if email != "" {
			index[email] = g.Id
		}
	}
	return index
}

// resolveGroupEdgesForNode scans outgoing GRANTS edges from a node and
// returns (resolved edges to add, raw edges to drop) for "group:" targets
// found in the group index.
func resolveGroupEdgesForNode(
	ctx context.Context, gc postpopulate.GraphCaller, graphName string,
	nodeID string, groupIndex map[string]string,
	visited map[string]bool,
) (add, drop []knowledgev1.Edge) {
	outgoing, err := postpopulate.BrowseEdges(ctx, gc, kgtypes.GraphCloud, graphName, nodeID, postpopulate.OutgoingEdges, []kgtypes.EdgeType{kgtypes.EdgeGrants})
	if err != nil {
		slog.Debug("gcp iam-group-resolve: browse outgoing edges failed", "node", nodeID, "err", err)
		return nil, nil
	}
	for i := range outgoing {
		e := &outgoing[i]
		if !strings.HasPrefix(e.ToId, "group:") {
			continue
		}
		email := strings.TrimPrefix(e.ToId, "group:")
		groupNodeID, ok := groupIndex[email]
		if !ok {
			continue
		}

		key := e.FromId + ":" + groupNodeID
		if visited[key] {
			continue
		}
		visited[key] = true

		add = append(add, knowledgev1.Edge{
			FromId: e.FromId,
			ToId:   groupNodeID,
			Type:   string(kgtypes.EdgeGrants),
			Method: methodGCPIAMGroupResolve,
		})
		drop = append(drop, knowledgev1.Edge{
			FromId: e.FromId,
			ToId:   e.ToId,
			Type:   string(kgtypes.EdgeGrants),
		})
	}

	return add, drop
}

// resolveGroupEdgesFromRoles scans all edges in the graph that target
// "group:" prefixed IDs by querying incoming edges on known group email
// patterns. This catches edges from role-string sources that are not
// NodeCloudResource. Returns (resolved edges to add, raw edges to drop).
func resolveGroupEdgesFromRoles(
	ctx context.Context, gc postpopulate.GraphCaller, graphName string,
	groupIndex map[string]string,
	visited map[string]bool,
) (add, drop []knowledgev1.Edge) {
	for email, groupNodeID := range groupIndex {
		rawTarget := "group:" + email
		// Check if any edges point to this raw group target (incoming over the wire).
		incoming, err := postpopulate.BrowseEdges(ctx, gc, kgtypes.GraphCloud, graphName, rawTarget, postpopulate.IncomingEdges, []kgtypes.EdgeType{kgtypes.EdgeGrants})
		if err != nil {
			slog.Debug("gcp iam-group-resolve: browse incoming edges failed", "target", rawTarget, "err", err)
			continue
		}
		for i := range incoming {
			e := &incoming[i]
			key := e.FromId + ":" + groupNodeID
			if visited[key] {
				continue
			}
			visited[key] = true

			add = append(add, knowledgev1.Edge{
				FromId: e.FromId,
				ToId:   groupNodeID,
				Type:   string(kgtypes.EdgeGrants),
				Method: methodGCPIAMGroupResolve,
			})
			drop = append(drop, knowledgev1.Edge{
				FromId: e.FromId,
				ToId:   rawTarget,
				Type:   string(kgtypes.EdgeGrants),
			})
		}
	}

	return add, drop
}
