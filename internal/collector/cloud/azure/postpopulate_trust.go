// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

const methodAzureCrossTenantTrust = "azure-cross-tenant-trust"

// resolveCrossTenantTrust scans role assignment and federated credential edges
// to detect cross-tenant trust relationships. It emits EdgeTrusts edges from
// external (guest/foreign) principals to the resources they can access.
//
// Two detection vectors:
//  1. RBAC assignments where principal_type is Guest or ForeignGroup.
//  2. Federated identity credentials whose issuer tenant differs from
//     the identity's own tenant.
func resolveCrossTenantTrust(ctx context.Context, gc postpopulate.GraphCaller, graphName string) error {
	rbacEdges, err := resolveRBACGuestTrust(ctx, gc, graphName)
	if err != nil {
		return err
	}

	fedEdges, err := resolveFederatedTenantTrust(ctx, gc, graphName)
	if err != nil {
		return err
	}

	edges := append(rbacEdges, fedEdges...)
	if err := postpopulate.LinkEdgesBatch(ctx, gc, kgtypes.GraphCloud, graphName, edges); err != nil {
		return err
	}
	slog.Debug("azure cross-tenant trust: emitted edges", "count", len(edges))
	return nil
}

// resolveRBACGuestTrust queries managed identity nodes, walks their
// EdgeAssumesRole edges, and returns EdgeTrusts for any assignment
// whose principal_type is Guest or ForeignGroup.
func resolveRBACGuestTrust(ctx context.Context, gc postpopulate.GraphCaller, graphName string) ([]knowledgev1.Edge, error) {
	identities, err := postpopulate.BrowseNodes(ctx, gc, kgtypes.GraphCloud, graphName, managedIdentityQuery())
	if err != nil {
		return nil, err
	}

	var edges []knowledgev1.Edge
	for _, node := range identities {
		outgoing := collectOutgoingEdges(ctx, gc, graphName, node.Id, kgtypes.EdgeAssumesRole)
		for i := range outgoing {
			e := &outgoing[i]
			md := parseEdgeMetadata(e.Evidence)
			if !isCrossTenantPrincipal(md["principal_type"]) {
				continue
			}
			edges = append(edges, knowledgev1.Edge{
				FromId: e.FromId,
				ToId:   e.ToId,
				Type:   string(kgtypes.EdgeTrusts),
				Method: methodAzureCrossTenantTrust,
			})
		}
	}
	return edges, nil
}

// resolveFederatedTenantTrust queries managed identity nodes and their
// outbound EdgeWorkloadIdentity edges. When the federated credential's
// issuer is from a different tenant than the identity, emit EdgeTrusts.
func resolveFederatedTenantTrust(ctx context.Context, gc postpopulate.GraphCaller, graphName string) ([]knowledgev1.Edge, error) {
	identities, err := postpopulate.BrowseNodes(ctx, gc, kgtypes.GraphCloud, graphName, managedIdentityQuery())
	if err != nil {
		return nil, err
	}

	var edges []knowledgev1.Edge
	for _, node := range identities {
		identityTenant := kgtypes.Value(node, "tenantId")
		if identityTenant == "" {
			continue
		}
		incoming := collectIncomingEdges(ctx, gc, graphName, node.Id, kgtypes.EdgeWorkloadIdentity)
		for i := range incoming {
			e := &incoming[i]
			md := parseEdgeMetadata(e.Evidence)
			issuer := md["issuer"]
			if issuer == "" {
				continue
			}
			if !isExternalIssuer(issuer, identityTenant) {
				continue
			}
			edges = append(edges, knowledgev1.Edge{
				FromId: e.FromId,
				ToId:   e.ToId,
				Type:   string(kgtypes.EdgeTrusts),
				Method: methodAzureCrossTenantTrust,
			})
		}
	}
	return edges, nil
}

// managedIdentityQuery is the BrowseNodes filter for Azure user-assigned
// managed-identity cloud nodes.
func managedIdentityQuery() map[string]any {
	return map[string]any{
		"type":  string(kgtypes.NodeCloudResource),
		"meta":  map[string]string{"resource_type": "Microsoft.ManagedIdentity/userAssignedIdentities"},
		"limit": 0,
	}
}

// collectOutgoingEdges reads a node's outgoing edges of edgeType over the wire.
func collectOutgoingEdges(ctx context.Context, gc postpopulate.GraphCaller, graphName, nodeID string, edgeType kgtypes.EdgeType) []knowledgev1.Edge {
	edges, err := postpopulate.BrowseEdges(ctx, gc, kgtypes.GraphCloud, graphName, nodeID, postpopulate.OutgoingEdges, []kgtypes.EdgeType{edgeType})
	if err != nil {
		slog.Debug("azure: browse outgoing edges failed", "node", nodeID, "err", err)
		return nil
	}
	return edges
}

// collectIncomingEdges reads a node's incoming edges of edgeType over the wire.
func collectIncomingEdges(ctx context.Context, gc postpopulate.GraphCaller, graphName, nodeID string, edgeType kgtypes.EdgeType) []knowledgev1.Edge {
	edges, err := postpopulate.BrowseEdges(ctx, gc, kgtypes.GraphCloud, graphName, nodeID, postpopulate.IncomingEdges, []kgtypes.EdgeType{edgeType})
	if err != nil {
		slog.Debug("azure: browse incoming edges failed", "node", nodeID, "err", err)
		return nil
	}
	return edges
}

// isCrossTenantPrincipal returns true if the principal type indicates a
// cross-tenant relationship (B2B guest user or foreign group).
func isCrossTenantPrincipal(principalType string) bool {
	switch principalType {
	case "Guest", "ForeignGroup":
		return true
	default:
		return false
	}
}

// isExternalIssuer checks if a federated credential issuer URL references
// a different Azure AD tenant. AKS and GitHub OIDC issuers are not
// cross-tenant by definition; only login.microsoftonline.com issuers
// with a different tenant ID are flagged.
func isExternalIssuer(issuer, identityTenant string) bool {
	// AKS and GitHub issuers are not cross-tenant.
	if isAKSIssuer(issuer) || isGitHubIssuer(issuer) {
		return false
	}
	// Check for Azure AD OIDC issuers: https://login.microsoftonline.com/{tenantID}/v2.0
	if !strings.Contains(issuer, "login.microsoftonline.com") {
		// Generic OIDC issuer from outside Azure — treat as external.
		return true
	}
	issuerTenant := extractTenantFromIssuer(issuer)
	return issuerTenant != "" && issuerTenant != identityTenant
}

// extractTenantFromIssuer extracts the tenant ID from an Azure AD OIDC
// issuer URL like https://login.microsoftonline.com/{tenantID}/v2.0
func extractTenantFromIssuer(issuer string) string {
	parts := strings.Split(issuer, "/")
	for i, p := range parts {
		if p == "login.microsoftonline.com" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

// parseEdgeMetadata deserializes the Evidence JSON field from a knowledgev1.Edge
// into a string map. Returns nil on empty or invalid input.
func parseEdgeMetadata(evidence string) map[string]string {
	if evidence == "" {
		return nil
	}
	var md map[string]string
	if err := json.Unmarshal([]byte(evidence), &md); err != nil {
		return nil
	}
	return md
}
