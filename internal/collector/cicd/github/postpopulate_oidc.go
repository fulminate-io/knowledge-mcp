// SPDX-License-Identifier: Apache-2.0

package github

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
)

const (
	githubOIDCIssuer = "token.actions.githubusercontent.com"
	methodOIDC       = "github-oidc"
)

// postPopulateOIDC scans every cloud graph for IAM roles trusting the GitHub
// Actions OIDC provider and emits EdgeFederates edges from CI/CD workflow
// subjects to the cloud IAM principals. This mirrors the IRSA PostPopulate
// pattern in cloud/aws/postpopulate_irsa.go.
//
// graphName is the cicd GitHub graph the hook owns; the federation edges land
// in THAT graph (routed by Target.Account==graphName via the wire helper) so
// the per-account topology analyzers see the subject→role edges intra-graph.
// Cloud graphs are read-only here — only scanned for trust policies.
func postPopulateOIDC(ctx context.Context, gc postpopulate.GraphCaller, graphName string) error {
	cloudGraphs, err := postpopulate.ListGraphNames(ctx, gc, kgtypes.GraphCloud)
	if err != nil {
		return err
	}

	var allEdges []knowledgev1.Edge
	for _, cloudName := range cloudGraphs {
		edges, err := scanCloudGraphForGitHubOIDC(ctx, gc, cloudName)
		if err != nil {
			slog.Warn("github oidc: scan error", "graph", cloudName, "error", err)
			continue
		}
		allEdges = append(allEdges, edges...)
	}

	if err := postpopulate.LinkEdgesBatch(ctx, gc, kgtypes.GraphCICD, graphName, allEdges); err != nil {
		return err
	}
	if len(allEdges) > 0 {
		slog.Debug("github oidc: emitted federation edges", "count", len(allEdges), "graph", graphName)
	}
	return nil
}

// scanCloudGraphForGitHubOIDC scans a single cloud account for IAM roles
// trusting the GitHub OIDC issuer.
func scanCloudGraphForGitHubOIDC(ctx context.Context, gc postpopulate.GraphCaller, accountName string) ([]knowledgev1.Edge, error) {
	roles, err := postpopulate.BrowseAllNodes(ctx, gc, kgtypes.GraphCloud, accountName, map[string]any{
		"type": string(kgtypes.NodeCloudResource),
		"meta": map[string]string{"resource_type": "iam-role"},
	})
	if err != nil {
		return nil, err
	}

	var edges []knowledgev1.Edge
	for _, role := range roles {
		roleEdges := matchGitHubFederatedPrincipals(role)
		edges = append(edges, roleEdges...)
	}
	return edges, nil
}

// matchGitHubFederatedPrincipals checks an IAM role's trust policy for
// principals referencing the GitHub OIDC provider. Returns EdgeFederates
// edges from GitHub subject nodes to the IAM role.
func matchGitHubFederatedPrincipals(role *knowledgev1.Node) []knowledgev1.Edge {
	policy := extractTrustPolicy(role)
	if policy == nil {
		return nil
	}

	var edges []knowledgev1.Edge
	for i := range policy.Statements {
		stmt := &policy.Statements[i]
		if !strings.EqualFold(stmt.Effect, "Allow") || stmt.Principal == nil {
			continue
		}
		for _, fedARN := range stmt.Principal.Federated {
			if !strings.Contains(fedARN, githubOIDCIssuer) {
				continue
			}
			subjects := extractGitHubOIDCSubjects(stmt.Condition)
			for _, subj := range subjects {
				edges = append(edges, buildFederationEdge(subj, role.Id, fedARN))
			}
		}
	}
	return edges
}

// extractGitHubOIDCSubjects extracts repo:org/repo:ref:* subjects from
// the trust policy condition block.
func extractGitHubOIDCSubjects(cond map[string]map[string]stringOrSlice) []string {
	if len(cond) == 0 {
		return nil
	}
	subKey := githubOIDCIssuer + ":sub"
	var subjects []string
	for _, opValues := range cond {
		vals, ok := opValues[subKey]
		if !ok {
			continue
		}
		for _, v := range vals {
			if strings.HasPrefix(v, "repo:") {
				subjects = append(subjects, v)
			}
		}
	}
	return subjects
}

// buildFederationEdge creates an EdgeFederates edge from a GitHub OIDC
// subject to a cloud IAM role.
func buildFederationEdge(subject, roleID, providerARN string) knowledgev1.Edge {
	fromID := githubSubjectToNodeID(subject)
	evidence := "GitHub OIDC: " + subject + " via " + providerARN
	return knowledgev1.Edge{
		FromId:   fromID,
		ToId:     roleID,
		Type:     string(kgtypes.EdgeFederates),
		Method:   methodOIDC,
		Evidence: evidence,
	}
}

// githubSubjectToNodeID converts a GitHub OIDC subject claim like
// "repo:org/repo:ref:refs/heads/main" into a graph node ID.
func githubSubjectToNodeID(subject string) string {
	// "repo:org/repo:ref:refs/heads/main" → "org/repo"
	trimmed := strings.TrimPrefix(subject, "repo:")
	parts := strings.SplitN(trimmed, ":", 2)
	if len(parts) == 0 || parts[0] == "" {
		return "github:oidc-subject/" + subject
	}
	repoFullName := parts[0]
	orgParts := strings.SplitN(repoFullName, "/", 2)
	if len(orgParts) != 2 {
		return "github:oidc-subject/" + subject
	}
	org := orgParts[0]
	return repoID(org, repoFullName)
}

// --- Trust policy parsing (shared with IRSA, simplified for GitHub) ---

type trustPolicy struct {
	Statements []trustStatement `json:"Statement"`
}

type trustStatement struct {
	Effect    string                              `json:"Effect"`
	Principal *trustPrincipal                     `json:"Principal"`
	Condition map[string]map[string]stringOrSlice `json:"Condition"`
}

type trustPrincipal struct {
	Federated stringOrSlice `json:"Federated"`
	Service   stringOrSlice `json:"Service"`
}

// stringOrSlice handles JSON values that can be either a single string or
// an array of strings (common in IAM policy documents).
type stringOrSlice []string

func (s *stringOrSlice) UnmarshalJSON(data []byte) error {
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		*s = []string{single}
		return nil
	}
	var multi []string
	if err := json.Unmarshal(data, &multi); err != nil {
		return err
	}
	*s = multi
	return nil
}

// extractTrustPolicy parses an IAM role's Content as an AssumeRolePolicyDocument.
func extractTrustPolicy(role *knowledgev1.Node) *trustPolicy {
	if role.Content == "" {
		return nil
	}
	// The trust policy can be nested under AssumeRolePolicyDocument
	var wrapper struct {
		AssumeRolePolicyDocument json.RawMessage `json:"AssumeRolePolicyDocument"`
	}
	if err := json.Unmarshal([]byte(role.Content), &wrapper); err != nil {
		return nil
	}
	raw := wrapper.AssumeRolePolicyDocument
	if len(raw) == 0 {
		// Try top-level
		raw = []byte(role.Content)
	}

	// The policy document might be a JSON string or a JSON object
	var policyStr string
	if json.Unmarshal(raw, &policyStr) == nil && policyStr != "" {
		raw = []byte(policyStr)
	}

	var policy trustPolicy
	if err := json.Unmarshal(raw, &policy); err != nil {
		return nil
	}
	return &policy
}
