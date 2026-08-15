// SPDX-License-Identifier: Apache-2.0

package bitbucket

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
)

const (
	methodBitbucketOIDC = "bitbucket-oidc"
	bitbucketIssuerFmt  = "https://api.bitbucket.org/2.0/workspaces/%s/pipelines-config/identity/oidc"
)

// resolveOIDCFederation scans every cloud graph for IAM principals that trust
// the Bitbucket Pipelines OIDC issuer and emits EdgeFederates edges into the
// CI/CD graph (graphName, routed by Target.Account==graphName via the wire
// helper). Cloud graphs are read-only — only scanned for trust policies. All
// graph I/O rides the postpopulate wire helpers; the client holds no store engine.
func resolveOIDCFederation(ctx context.Context, gc postpopulate.GraphCaller, graphName, workspace string) error {
	issuerURL := bitbucketIssuerURL(workspace)
	cloudGraphs, err := postpopulate.ListGraphNames(ctx, gc, kgtypes.GraphCloud)
	if err != nil {
		return err
	}

	var allEdges []knowledgev1.Edge
	for _, cloudName := range cloudGraphs {
		allEdges = append(allEdges, scanCloudGraph(ctx, gc, cloudName, issuerURL, workspace)...)
	}

	if err := postpopulate.LinkEdgesBatch(ctx, gc, kgtypes.GraphCICD, graphName, allEdges); err != nil {
		return err
	}
	if len(allEdges) > 0 {
		slog.Debug("bitbucket oidc: emitted federation edges",
			"count", len(allEdges), "workspace", workspace, "graph", graphName)
	}
	return nil
}

// scanCloudGraph scans a single cloud graph for IAM principals trusting the
// Bitbucket OIDC issuer. Per-provider scan errors are logged and swallowed (a
// best-effort enrichment pass), so this returns edges only.
func scanCloudGraph(
	ctx context.Context,
	gc postpopulate.GraphCaller,
	cloudName, issuerURL, workspace string,
) []knowledgev1.Edge {
	var edges []knowledgev1.Edge

	// AWS: scan iam-role trust policies for federated principals.
	awsEdges, err := scanAWSRoles(ctx, gc, cloudName, issuerURL, workspace)
	if err != nil {
		slog.Debug("bitbucket oidc: aws scan error", "graph", cloudName, "error", err)
	}
	edges = append(edges, awsEdges...)

	// Azure: scan managed identities with federated credentials.
	azureEdges, err := scanAzureFederated(ctx, gc, cloudName, issuerURL, workspace)
	if err != nil {
		slog.Debug("bitbucket oidc: azure scan error", "graph", cloudName, "error", err)
	}
	edges = append(edges, azureEdges...)

	return edges
}

// scanAWSRoles checks IAM role trust policies for the Bitbucket OIDC issuer.
func scanAWSRoles(
	ctx context.Context, gc postpopulate.GraphCaller, cloudName, issuerURL, workspace string,
) ([]knowledgev1.Edge, error) {
	roles, err := postpopulate.BrowseAllNodes(ctx, gc, kgtypes.GraphCloud, cloudName, map[string]any{
		"type": string(kgtypes.NodeCloudResource),
		"meta": map[string]string{"resource_type": "iam-role"},
	})
	if err != nil {
		return nil, err
	}

	var edges []knowledgev1.Edge
	for _, role := range roles {
		if !strings.Contains(role.Content, issuerURL) {
			continue
		}
		// Append a fresh edge literal directly (copying an existing knowledgev1.Edge
		// value is copylocks-forbidden after the proto value-embed flip).
		edges = appendFederationEdge(edges, workspace, role.Id, issuerURL)
	}
	return edges, nil
}

// scanAzureFederated checks managed identities for federated credentials
// matching the Bitbucket OIDC issuer.
func scanAzureFederated(
	ctx context.Context, gc postpopulate.GraphCaller, cloudName, issuerURL, workspace string,
) ([]knowledgev1.Edge, error) {
	identities, err := postpopulate.BrowseAllNodes(ctx, gc, kgtypes.GraphCloud, cloudName, map[string]any{
		"type": string(kgtypes.NodeCloudResource),
		"meta": map[string]string{"resource_type": "managed-identity"},
	})
	if err != nil {
		return nil, err
	}

	var edges []knowledgev1.Edge
	for _, identity := range identities {
		if !strings.Contains(identity.Content, issuerURL) {
			continue
		}
		subject := extractFederatedSubject(identity.Content, issuerURL)
		fromID := fmt.Sprintf("bitbucket:%s/OIDCSubject/%s", workspace, subject)
		edges = append(edges, knowledgev1.Edge{
			FromId:   fromID,
			ToId:     identity.Id,
			Type:     string(kgtypes.EdgeFederates),
			Method:   methodBitbucketOIDC,
			Evidence: "Bitbucket OIDC federation: " + issuerURL,
		})
	}
	return edges, nil
}

// appendFederationEdge appends a fresh EdgeFederates edge (from a Bitbucket OIDC
// subject to a cloud IAM principal) into out and returns the extended slice. The
// literal is constructed at the append site so no existing knowledgev1.Edge value is
// copied (copylocks-clean under the proto value-embed flip).
func appendFederationEdge(out []knowledgev1.Edge, workspace, targetID, issuerURL string) []knowledgev1.Edge {
	fromID := fmt.Sprintf("bitbucket:%s/OIDCSubject/workspace", workspace)
	return append(out, knowledgev1.Edge{
		FromId:   fromID,
		ToId:     targetID,
		Type:     string(kgtypes.EdgeFederates),
		Method:   methodBitbucketOIDC,
		Evidence: "Bitbucket OIDC federation: " + issuerURL,
	})
}

// bitbucketIssuerURL constructs the OIDC issuer URL for a workspace.
func bitbucketIssuerURL(workspace string) string {
	return fmt.Sprintf(bitbucketIssuerFmt, workspace)
}

// extractFederatedSubject extracts the subject from a federated credential's
// content JSON that matches the given issuer.
func extractFederatedSubject(content, issuerURL string) string {
	var raw struct {
		FederatedCredentials []struct {
			Issuer  string `json:"issuer"`
			Subject string `json:"subject"`
		} `json:"federated_credentials"`
	}
	if err := json.Unmarshal([]byte(content), &raw); err != nil {
		return "unknown"
	}
	for _, fc := range raw.FederatedCredentials {
		if fc.Issuer == issuerURL {
			return fc.Subject
		}
	}
	return "unknown"
}
