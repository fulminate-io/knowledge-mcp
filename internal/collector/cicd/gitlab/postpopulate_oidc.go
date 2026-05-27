// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
)

const methodGitLabOIDC = "gitlab-oidc"

// resolveOIDCFederation scans every cloud graph for IAM principals that trust
// the GitLab OIDC issuer and emits an OIDC-issuer node + EdgeFederates edges
// into the CI/CD graph (graphName, routed by Target.Account==graphName via the
// wire helper). Cloud graphs are read-only — only scanned for trust policies.
// All graph I/O rides the postpopulate wire helpers; the client holds no
// store engine. This mirrors the github/bitbucket OIDC PostPopulate hooks.
//
// Unlike github/bitbucket — whose federation-edge source node is created by the
// regular subcollectors at collect time — GitLab's OIDC-issuer node has no other
// producer, so the issuer node is materialized here alongside its edges in a
// single create_batch (LinkNodesAndEdgesBatch).
func resolveOIDCFederation(ctx context.Context, gc postpopulate.GraphCaller, graphName, group, issuer string) error {
	cloudGraphs, err := postpopulate.ListGraphNames(ctx, gc, kgtypes.GraphCloud)
	if err != nil {
		return err
	}

	oidcID := fmt.Sprintf("gitlab:%s/OIDCIssuer", group)
	var edges []knowledgev1.Edge
	for _, cloudName := range cloudGraphs {
		cloudEdges, scanErr := scanCloudGraph(ctx, gc, cloudName, oidcID, issuer)
		if scanErr != nil {
			slog.Warn("gitlab-oidc: cloud graph scan error", "graph", cloudName, "error", scanErr)
			continue
		}
		edges = append(edges, cloudEdges...)
	}

	if len(edges) == 0 {
		return nil
	}

	issuerNode := buildIssuerNode(oidcID, group, issuer)
	if err := postpopulate.LinkNodesAndEdgesBatch(
		ctx, gc, kgtypes.GraphCICD, graphName, []*knowledgev1.Node{issuerNode}, edges,
	); err != nil {
		return err
	}
	slog.Debug("gitlab-oidc: emitted federation edges",
		"count", len(edges), "group", group, "graph", graphName)
	return nil
}

// scanCloudGraph scans a single cloud account graph for resources whose content
// references the GitLab OIDC issuer and returns one EdgeFederates edge per match
// (from the GitLab OIDC-issuer node to the trusting cloud resource).
func scanCloudGraph(
	ctx context.Context, gc postpopulate.GraphCaller, cloudName, oidcID, issuer string,
) ([]knowledgev1.Edge, error) {
	resources, err := postpopulate.BrowseNodes(ctx, gc, kgtypes.GraphCloud, cloudName, map[string]any{
		"type":  string(kgtypes.NodeCloudResource),
		"limit": 0,
	})
	if err != nil {
		return nil, err
	}

	var edges []knowledgev1.Edge
	for _, n := range resources {
		if !isOIDCRelevantType(kgtypes.Value(n, "resource_type")) {
			continue
		}
		// matchGitLabTrust appends a fresh edge literal directly into edges
		// (avoids copying an existing knowledgev1.Edge value, which copylocks forbids
		// now that knowledgev1.Edge value-embeds the proto MessageState).
		edges = matchGitLabTrust(edges, n, oidcID, issuer)
	}
	return edges, nil
}

// matchGitLabTrust inspects a cloud resource node for the GitLab OIDC issuer
// and, on a match, appends a fresh federation edge into out, returning the
// extended slice. The edge literal is constructed in-place at the append site
// so no existing knowledgev1.Edge value is copied (copylocks-clean).
func matchGitLabTrust(out []knowledgev1.Edge, n *knowledgev1.Node, oidcID, issuer string) []knowledgev1.Edge {
	content := n.Content
	if content == "" {
		return out
	}

	var raw map[string]any
	if err := json.Unmarshal([]byte(content), &raw); err != nil {
		return out
	}

	jsonBytes, err := json.Marshal(raw)
	if err != nil || !strings.Contains(string(jsonBytes), issuer) {
		return out
	}

	return append(out, knowledgev1.Edge{
		FromId:   oidcID,
		ToId:     n.Id,
		Type:     string(kgtypes.EdgeFederates),
		Method:   methodGitLabOIDC,
		Evidence: "GitLab OIDC federation: " + issuer,
	})
}

// buildIssuerNode materializes the GitLab OIDC-issuer resource node that the
// federation edges originate from.
func buildIssuerNode(oidcID, group, issuer string) *knowledgev1.Node {
	n := &knowledgev1.Node{
		Id:         oidcID,
		Type:       string(kgtypes.NodeCICDResource),
		SymbolName: fmt.Sprintf("GitLab OIDC (%s)", group),
		Source:     "cicd",
	}
	kgtypes.SetValue(n, "resource_type", "oidc-issuer")
	kgtypes.SetValue(n, "provider", "gitlab")
	kgtypes.SetValue(n, "issuer", issuer)
	kgtypes.SetValue(n, "group", group)
	return n
}

// gitlabOIDCIssuer derives the OIDC issuer URL from the GitLab base URL. GitLab
// uses the base URL (without trailing slash) as the OIDC issuer. The base URL is
// re-derived from the same GITLAB_URL env var the collector's newClient reads —
// an environment read, not a store access — so the hook reproduces collect-time
// issuer derivation without holding any collect-time state.
func gitlabOIDCIssuer() string {
	baseURL := os.Getenv("GITLAB_URL")
	if baseURL == "" {
		baseURL = defaultGitLabURL
	}
	return normalizeIssuer(baseURL)
}

// normalizeIssuer derives the OIDC issuer URL from the base URL.
// GitLab uses the base URL (without trailing slash) as the OIDC issuer.
func normalizeIssuer(baseURL string) string {
	return strings.TrimRight(baseURL, "/")
}

// isOIDCRelevantType checks if a cloud resource type could contain OIDC config.
func isOIDCRelevantType(rt string) bool {
	switch rt {
	case "iam-role", "iam-openid-connect-provider",
		"managed-identity", "federated-credential",
		"workload-identity-pool", "workload-identity-provider":
		return true
	}
	return false
}
