// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"log/slog"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

const methodAADGroupResolve = "postpopulate:aad-group-resolve"

// resolveAADGroupAssignments scans role assignment and access policy edges,
// and when the referenced principal matches a known AAD group node in the
// graph, creates additional edges that terminate on the actual group node
// (azure:aad:group/{objectId}) rather than a raw principal ID string.
//
// This ensures topology analyzers can walk from resources through group
// nodes to individual members.
func resolveAADGroupAssignments(ctx context.Context, gc postpopulate.GraphCaller, graphName string) error {
	groupIndex := buildAADGroupIndex(ctx, gc, graphName)
	if len(groupIndex) == 0 {
		return nil
	}

	edges := resolveAccessedByGroups(ctx, gc, graphName, groupIndex)
	edges = append(edges, resolveAssumesRoleGroups(ctx, gc, graphName, groupIndex)...)

	if err := postpopulate.LinkEdgesBatch(ctx, gc, kgtypes.GraphCloud, graphName, edges); err != nil {
		return err
	}
	slog.Debug("azure aad group resolve: emitted edges", "count", len(edges))
	return nil
}

// buildAADGroupIndex queries all azure:aad:group nodes and returns a map
// from raw object ID (GUID) to the full group node ID.
func buildAADGroupIndex(ctx context.Context, gc postpopulate.GraphCaller, graphName string) map[string]string {
	groups, err := postpopulate.BrowseNodes(ctx, gc, kgtypes.GraphCloud, graphName, map[string]any{
		"type":  string(kgtypes.NodeCloudResource),
		"meta":  map[string]string{"resource_type": "azure:aad:group"},
		"limit": 0,
	})
	if err != nil {
		slog.Debug("azure aad group resolve: query groups", "err", err)
		return nil
	}

	index := make(map[string]string, len(groups))
	for _, node := range groups {
		objectID := extractObjectIDFromGroupNode(node.Id)
		if objectID != "" {
			index[objectID] = node.Id
		}
	}
	return index
}

// extractObjectIDFromGroupNode extracts the object ID from a group node ID
// of the form "azure:aad:group/{objectId}".
func extractObjectIDFromGroupNode(nodeID string) string {
	const prefix = "azure:aad:group/"
	if !strings.HasPrefix(nodeID, prefix) {
		return ""
	}
	return nodeID[len(prefix):]
}

// resolveAccessedByGroups scans all EdgeAccessedBy edges in the graph.
// When the edge target is a raw object ID (GUID) that matches a known
// group, emit a new EdgeAccessedBy from the source to the group node.
func resolveAccessedByGroups(
	ctx context.Context, gc postpopulate.GraphCaller, graphName string, groupIndex map[string]string,
) []knowledgev1.Edge {
	// Query all cloud resource nodes that have outgoing EdgeAccessedBy.
	// We iterate node types that commonly emit ACCESSED_BY (KeyVault).
	vaults, err := postpopulate.BrowseNodes(ctx, gc, kgtypes.GraphCloud, graphName, map[string]any{
		"type":  string(kgtypes.NodeCloudResource),
		"meta":  map[string]string{"resource_type": "Microsoft.KeyVault/vaults"},
		"limit": 0,
	})
	if err != nil {
		return nil
	}

	var edges []knowledgev1.Edge
	for _, node := range vaults {
		outgoing := collectOutgoingEdges(ctx, gc, graphName, node.Id, kgtypes.EdgeAccessedBy)
		for i := range outgoing {
			e := &outgoing[i]
			groupNodeID, ok := groupIndex[e.ToId]
			if !ok {
				continue
			}
			edges = append(edges, knowledgev1.Edge{
				FromId:   e.FromId,
				ToId:     groupNodeID,
				Type:     string(kgtypes.EdgeAccessedBy),
				Method:   methodAADGroupResolve,
				Evidence: e.Evidence,
			})
		}
	}
	return edges
}

// resolveAssumesRoleGroups scans managed identity nodes with outgoing
// EdgeAssumesRole edges. When the edge metadata indicates principal_type
// is "Group" and the source principal matches a known group, emit an
// edge from the group node to the scope.
func resolveAssumesRoleGroups(
	ctx context.Context, gc postpopulate.GraphCaller, graphName string, groupIndex map[string]string,
) []knowledgev1.Edge {
	identities, err := postpopulate.BrowseNodes(ctx, gc, kgtypes.GraphCloud, graphName, managedIdentityQuery())
	if err != nil {
		return nil
	}

	var edges []knowledgev1.Edge
	for _, node := range identities {
		principalID := kgtypes.Value(node, "principalId")
		if principalID == "" {
			continue
		}
		groupNodeID, ok := groupIndex[principalID]
		if !ok {
			continue
		}
		outgoing := collectOutgoingEdges(ctx, gc, graphName, node.Id, kgtypes.EdgeAssumesRole)
		for i := range outgoing {
			e := &outgoing[i]
			md := parseEdgeMetadata(e.Evidence)
			if md["principal_type"] != "Group" {
				continue
			}
			edges = append(edges, knowledgev1.Edge{
				FromId:   groupNodeID,
				ToId:     e.ToId,
				Type:     string(kgtypes.EdgeAssumesRole),
				Method:   methodAADGroupResolve,
				Evidence: e.Evidence,
			})
		}
	}
	return edges
}
