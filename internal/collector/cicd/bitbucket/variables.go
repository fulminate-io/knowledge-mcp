// SPDX-License-Identifier: Apache-2.0

package bitbucket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// variablesCollector discovers pipeline variables at workspace, repo, and
// deployment-environment scopes. CRITICAL: only names are stored, never values.
type variablesCollector struct {
	client    *Client
	workspace string
	repos     []repoInfo
}

func newVariablesCollector(
	client *Client, workspace string, repos []repoInfo,
) *variablesCollector {
	return &variablesCollector{client: client, workspace: workspace, repos: repos}
}

func (v *variablesCollector) Name() string { return "bitbucket-variables" }

// Collect fetches variables at all three scopes.
func (v *variablesCollector) Collect(ctx context.Context) (cicd.SubCollectorResult, error) {
	var result cicd.SubCollectorResult

	// Workspace variables.
	wsVars, err := v.fetchVars(ctx,
		fmt.Sprintf("workspaces/%s/pipelines-config/variables", v.workspace))
	if err != nil {
		slog.Debug("bitbucket: workspace variables skip", "error", err)
	} else {
		wsID := fmt.Sprintf("bitbucket:%s/Workspace/%s", v.workspace, v.workspace)
		v.appendVars(&result, wsVars, "workspace", wsID, "", "")
	}

	// Per-repo and deployment variables.
	for _, repo := range v.repos {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		v.collectRepoVars(ctx, &result, repo.Slug)
	}

	return result, nil
}

// collectRepoVars fetches repo-level and deployment-scoped variables.
func (v *variablesCollector) collectRepoVars(
	ctx context.Context, result *cicd.SubCollectorResult, slug string,
) {
	repoID := fmt.Sprintf("bitbucket:%s/Repository/%s", v.workspace, slug)

	// Repo variables.
	path := fmt.Sprintf("repositories/%s/%s/pipelines-config/variables",
		v.workspace, slug)
	repoVars, err := v.fetchVars(ctx, path)
	if err != nil {
		slog.Debug("bitbucket: repo variables skip",
			"repo", slug, "error", err)
	} else {
		v.appendVars(result, repoVars, "repository", repoID, slug, "")
	}

	// Deployment environment variables require environment UUIDs.
	envs := v.fetchEnvUUIDs(ctx, slug)
	for _, env := range envs {
		if ctx.Err() != nil {
			return
		}
		dpath := fmt.Sprintf(
			"repositories/%s/%s/deployments_config/environments/%s/variables",
			v.workspace, slug, env.UUID)
		depVars, err := v.fetchVars(ctx, dpath)
		if err != nil {
			slog.Debug("bitbucket: deployment variables skip",
				"repo", slug, "env", env.Name, "error", err)
			continue
		}
		envNodeID := fmt.Sprintf("bitbucket:%s/Environment/%s/%s",
			v.workspace, slug, env.Name)
		v.appendVars(result, depVars, "deployment", envNodeID, slug, env.Name)
	}
}

// fetchVars fetches variables from a paginated endpoint.
func (v *variablesCollector) fetchVars(
	ctx context.Context, path string,
) ([]apiVariable, error) {
	var vars []apiVariable
	err := v.client.GetPaginated(ctx, path, func(raw json.RawMessage) error {
		var page []apiVariable
		if err := json.Unmarshal(raw, &page); err != nil {
			return fmt.Errorf("unmarshal variables: %w", err)
		}
		vars = append(vars, page...)
		return nil
	})
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == 404 {
			return nil, nil
		}
		return nil, err
	}
	return vars, nil
}

// fetchEnvUUIDs returns environment UUIDs for a repo (lightweight re-fetch).
func (v *variablesCollector) fetchEnvUUIDs(
	ctx context.Context, slug string,
) []envRef {
	path := fmt.Sprintf("repositories/%s/%s/environments", v.workspace, slug)
	var envs []envRef
	_ = v.client.GetPaginated(ctx, path, func(raw json.RawMessage) error {
		var page []envRef
		if err := json.Unmarshal(raw, &page); err != nil {
			return err
		}
		envs = append(envs, page...)
		return nil
	})
	return envs
}

type envRef struct {
	UUID string `json:"uuid"`
	Name string `json:"name"`
}

// appendVars adds variable resources and edges to the result.
// Values are never stored — only key, scope, and secured flag.
func (v *variablesCollector) appendVars(
	result *cicd.SubCollectorResult,
	vars []apiVariable,
	scope, parentID, repoSlug, envName string,
) {
	for _, av := range vars {
		scopeKey := scope
		if repoSlug != "" {
			scopeKey = scope + "/" + repoSlug
		}
		if envName != "" {
			scopeKey = "env/" + repoSlug + "/" + envName
		}

		varID := fmt.Sprintf("bitbucket:%s/Variable/%s/%s",
			v.workspace, scopeKey, av.Key)

		// CRITICAL: content has key and secured only. Never store value.
		safeContent, _ := json.Marshal(varSafeContent{ //nolint:errchkjson // struct type cannot fail
			Key:     av.Key,
			Scope:   scope,
			Secured: av.Secured,
		})

		meta := map[string]string{
			"workspace": v.workspace,
			"key":       av.Key,
			"scope":     scope,
			"secured":   strconv.FormatBool(av.Secured),
		}
		if repoSlug != "" {
			meta["repo"] = repoSlug
		}
		if envName != "" {
			meta["environment"] = envName
		}

		result.Resources = append(result.Resources, cicd.ResourceSpec{
			ID:           varID,
			Name:         av.Key,
			ResourceType: "variable",
			Provider:     "bitbucket",
			Content:      safeContent,
			Metadata:     meta,
		})

		result.Edges = append(result.Edges, cicd.EdgeSpec{
			SourceID:     varID,
			TargetID:     parentID,
			Relationship: kgtypes.EdgeBelongsTo,
		})
	}
}

// varSafeContent is the sanitized content shape stored for variables.
// Intentionally omits the value field — only metadata is persisted.
type varSafeContent struct {
	Key     string `json:"key"`
	Scope   string `json:"scope"`
	Secured bool   `json:"secured"`
}

// apiVariable is the Bitbucket variable API response shape.
// The Value field is intentionally absent — we never read or store values.
type apiVariable struct {
	UUID    string `json:"uuid"`
	Key     string `json:"key"`
	Secured bool   `json:"secured"`
	System  bool   `json:"system"`
}
