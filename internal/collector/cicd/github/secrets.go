// SPDX-License-Identifier: Apache-2.0

package github

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	gogithub "github.com/google/go-github/v68/github"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// secretsCollector lists secret names (never values) at org, repo, and
// environment scopes.
type secretsCollector struct {
	actions actionsAPI
	repos   reposAPI
	org     string
}

func (c *secretsCollector) Name() string { return "github-secrets" }

// Collect lists org-level, repo-level, and environment-level secrets.
// Only secret names and metadata are collected — never values.
func (c *secretsCollector) Collect(ctx context.Context) (cicd.SubCollectorResult, error) {
	var result cicd.SubCollectorResult

	c.collectOrgSecrets(ctx, &result)

	repoNames, err := listRepoNames(ctx, c.repos, c.org)
	if err != nil {
		return cicd.SubCollectorResult{}, err
	}

	for _, fullName := range repoNames {
		owner, repo := splitFullName(fullName)
		c.collectRepoSecrets(ctx, owner, repo, fullName, &result)
		c.collectEnvSecrets(ctx, owner, repo, fullName, &result)
	}
	slog.Debug("github-secrets: collected", "org", c.org, "secrets", len(result.Resources))
	return result, nil
}

// collectOrgSecrets lists org-level secrets.
func (c *secretsCollector) collectOrgSecrets(ctx context.Context, result *cicd.SubCollectorResult) {
	opts := &gogithub.ListOptions{PerPage: 100}
	secrets, _, err := c.actions.ListOrgSecrets(ctx, c.org, opts)
	if err != nil {
		slog.Warn("github-secrets: org secrets error", "org", c.org, "error", err)
		return
	}
	if secrets == nil {
		return
	}
	for _, s := range secrets.Secrets {
		spec, edges := buildSecretResource(c.org, "", "org", s, orgID(c.org))
		result.Resources = append(result.Resources, spec)
		result.Edges = append(result.Edges, edges...)
	}
}

// collectRepoSecrets lists repo-level secrets.
func (c *secretsCollector) collectRepoSecrets(
	ctx context.Context, owner, repo, fullName string, result *cicd.SubCollectorResult,
) {
	opts := &gogithub.ListOptions{PerPage: 100}
	secrets, _, err := c.actions.ListRepoSecrets(ctx, owner, repo, opts)
	if err != nil {
		slog.Warn("github-secrets: repo secrets error", "repo", fullName, "error", err)
		return
	}
	if secrets == nil {
		return
	}
	for _, s := range secrets.Secrets {
		spec, edges := buildSecretResource(c.org, fullName, "repo", s, repoID(c.org, fullName))
		result.Resources = append(result.Resources, spec)
		result.Edges = append(result.Edges, edges...)
	}
}

// collectEnvSecrets lists environment-level secrets for all environments.
func (c *secretsCollector) collectEnvSecrets(
	ctx context.Context, owner, repo, fullName string, result *cicd.SubCollectorResult,
) {
	envResp, _, err := c.repos.ListEnvironments(ctx, owner, repo, nil)
	if err != nil || envResp == nil {
		return
	}
	for _, env := range envResp.Environments {
		envName := env.GetName()
		if envName == "" || env.ID == nil {
			continue
		}
		opts := &gogithub.ListOptions{PerPage: 100}
		secrets, _, err := c.actions.ListEnvSecrets(ctx, int(*env.ID), envName, opts)
		if err != nil {
			slog.Warn("github-secrets: env secrets error", "env", envName, "error", err)
			continue
		}
		if secrets == nil {
			continue
		}
		envNodeID := environmentID(c.org, fullName, envName)
		for _, s := range secrets.Secrets {
			spec, edges := buildSecretResource(c.org, fullName, "env/"+envName, s, envNodeID)
			result.Resources = append(result.Resources, spec)
			result.Edges = append(result.Edges, edges...)
		}
	}
}

// buildSecretResource creates a secret node (name only, no value) and edges.
func buildSecretResource(
	org, repoFullName, scope string, s *gogithub.Secret, parentID string,
) (cicd.ResourceSpec, []cicd.EdgeSpec) {
	nodeID := secretID(org, repoFullName, scope, s.Name)
	if repoFullName == "" {
		nodeID = fmt.Sprintf("github:%s/Secret/%s/%s", org, scope, s.Name)
	}

	content, _ := json.Marshal(secretMetadata{ //nolint:errchkjson // known struct
		Name:       s.Name,
		Scope:      scope,
		Visibility: s.Visibility,
	})

	spec := cicd.ResourceSpec{
		ID:           nodeID,
		Name:         s.Name,
		ResourceType: "secret",
		Provider:     "github",
		Content:      content,
		Metadata: map[string]string{
			"org":   org,
			"scope": scope,
		},
	}
	if repoFullName != "" {
		spec.Metadata["repo"] = repoFullName
	}

	edges := []cicd.EdgeSpec{{
		SourceID: nodeID, TargetID: parentID,
		Relationship: kgtypes.EdgeBelongsTo,
	}}
	return spec, edges
}

type secretMetadata struct {
	Name       string `json:"name"`
	Scope      string `json:"scope"`
	Visibility string `json:"visibility,omitempty"`
}
