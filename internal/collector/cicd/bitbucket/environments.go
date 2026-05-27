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

// environmentsCollector discovers deployment environments per repository.
type environmentsCollector struct {
	client    *Client
	workspace string
	repos     []repoInfo
}

func newEnvironmentsCollector(
	client *Client, workspace string, repos []repoInfo,
) *environmentsCollector {
	return &environmentsCollector{client: client, workspace: workspace, repos: repos}
}

func (e *environmentsCollector) Name() string { return "bitbucket-environments" }

// Collect fetches deployment environments for each repo.
func (e *environmentsCollector) Collect(ctx context.Context) (cicd.SubCollectorResult, error) {
	var result cicd.SubCollectorResult

	for _, repo := range e.repos {
		if err := ctx.Err(); err != nil {
			return result, err
		}

		resources, edges, err := e.collectRepo(ctx, repo.Slug)
		if err != nil {
			slog.Debug("bitbucket: environments skip",
				"repo", repo.Slug, "error", err)
			continue
		}
		result.Resources = append(result.Resources, resources...)
		result.Edges = append(result.Edges, edges...)
	}

	return result, nil
}

// collectRepo fetches environments for a single repo.
func (e *environmentsCollector) collectRepo(
	ctx context.Context, slug string,
) ([]cicd.ResourceSpec, []cicd.EdgeSpec, error) {
	path := fmt.Sprintf("repositories/%s/%s/environments", e.workspace, slug)

	var envs []apiEnvironment
	err := e.client.GetPaginated(ctx, path, func(raw json.RawMessage) error {
		var page []apiEnvironment
		if err := json.Unmarshal(raw, &page); err != nil {
			return fmt.Errorf("unmarshal environments: %w", err)
		}
		envs = append(envs, page...)
		return nil
	})
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == 404 {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("fetch environments for %s: %w", slug, err)
	}

	return e.buildEnvResources(slug, envs)
}

// buildEnvResources converts API environments into resource and edge specs.
func (e *environmentsCollector) buildEnvResources(
	slug string, envs []apiEnvironment,
) ([]cicd.ResourceSpec, []cicd.EdgeSpec, error) {
	repoID := fmt.Sprintf("bitbucket:%s/Repository/%s", e.workspace, slug)
	var resources []cicd.ResourceSpec
	var edges []cicd.EdgeSpec

	for _, env := range envs {
		envID := fmt.Sprintf("bitbucket:%s/Environment/%s/%s",
			e.workspace, slug, env.Name)
		content, _ := json.Marshal(env) //nolint:errchkjson // struct type cannot fail

		meta := map[string]string{
			"workspace": e.workspace,
			"repo":      slug,
		}
		if env.EnvironmentType.Name != "" {
			meta["environment_type"] = env.EnvironmentType.Name
		}
		if env.Rank > 0 {
			meta["rank"] = strconv.Itoa(env.Rank)
		}

		resources = append(resources, cicd.ResourceSpec{
			ID:           envID,
			Name:         env.Name,
			ResourceType: "environment",
			Provider:     "bitbucket",
			Content:      content,
			Metadata:     meta,
		})

		edges = append(edges, cicd.EdgeSpec{
			SourceID:     envID,
			TargetID:     repoID,
			Relationship: kgtypes.EdgeBelongsTo,
		})

		// If there are deployment restrictions, emit an approval gate.
		if env.Lock.Type == "lock" || len(env.Restrictions.AdminOnly) > 0 {
			gateID := fmt.Sprintf("bitbucket:%s/ApprovalGate/%s/%s",
				e.workspace, slug, env.Name)
			resources = append(resources, cicd.ResourceSpec{
				ID:           gateID,
				Name:         env.Name + " approval",
				ResourceType: "approval_gate",
				Provider:     "bitbucket",
				Metadata: map[string]string{
					"workspace":   e.workspace,
					"repo":        slug,
					"environment": env.Name,
				},
			})
			edges = append(edges, cicd.EdgeSpec{
				SourceID:     envID,
				TargetID:     gateID,
				Relationship: kgtypes.EdgeRequiresApproval,
			})
		}
	}

	return resources, edges, nil
}

// apiEnvironment is the Bitbucket deployment environment API shape.
type apiEnvironment struct {
	UUID            string `json:"uuid"`
	Name            string `json:"name"`
	Slug            string `json:"slug"`
	Rank            int    `json:"rank"`
	EnvironmentType struct {
		Name string `json:"name"`
	} `json:"environment_type"`
	Lock struct {
		Type string `json:"type"`
	} `json:"lock"`
	Restrictions struct {
		AdminOnly []any `json:"admin_only"`
	} `json:"restrictions"`
}
