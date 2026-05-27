// SPDX-License-Identifier: Apache-2.0

package bitbucket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// repoInfo carries the data other subcollectors need per-repo.
type repoInfo struct {
	Slug       string
	Mainbranch string
}

// pipelinesConfigCollector fetches and parses bitbucket-pipelines.yml per repo.
type pipelinesConfigCollector struct {
	client    *Client
	workspace string
	repos     []repoInfo
}

func newPipelinesConfigCollector(
	client *Client, workspace string, repos []repoInfo,
) *pipelinesConfigCollector {
	return &pipelinesConfigCollector{
		client:    client,
		workspace: workspace,
		repos:     repos,
	}
}

func (p *pipelinesConfigCollector) Name() string { return "bitbucket-pipelines-config" }

// Collect fetches bitbucket-pipelines.yml for each repo and extracts pipeline defs.
func (p *pipelinesConfigCollector) Collect(ctx context.Context) (cicd.SubCollectorResult, error) {
	var result cicd.SubCollectorResult

	for _, repo := range p.repos {
		if err := ctx.Err(); err != nil {
			return result, err
		}

		resources, edges, err := p.collectRepo(ctx, repo)
		if err != nil {
			slog.Debug("bitbucket: pipelines config skip",
				"repo", repo.Slug, "error", err)
			continue
		}
		result.Resources = append(result.Resources, resources...)
		result.Edges = append(result.Edges, edges...)
	}

	return result, nil
}

// collectRepo fetches and parses the pipeline config for a single repo.
func (p *pipelinesConfigCollector) collectRepo(
	ctx context.Context, repo repoInfo,
) ([]cicd.ResourceSpec, []cicd.EdgeSpec, error) {
	branch := repo.Mainbranch
	if branch == "" {
		branch = "main"
	}

	path := fmt.Sprintf("repositories/%s/%s/src/%s/bitbucket-pipelines.yml",
		p.workspace, repo.Slug, branch)

	data, err := p.client.GetRaw(ctx, path)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == 404 {
			return nil, nil, nil // no pipeline configured
		}
		return nil, nil, fmt.Errorf("fetch pipelines.yml for %s: %w", repo.Slug, err)
	}

	pf, err := parsePipelinesYAML(data)
	if err != nil {
		return nil, nil, fmt.Errorf("parse pipelines.yml for %s: %w", repo.Slug, err)
	}

	pipelines := extractPipelines(pf.Pipelines)
	return p.buildPipelineResources(repo.Slug, pipelines)
}

// buildPipelineResources creates ResourceSpecs and EdgeSpecs from parsed pipelines.
func (p *pipelinesConfigCollector) buildPipelineResources(
	slug string, pipelines []parsedPipeline,
) ([]cicd.ResourceSpec, []cicd.EdgeSpec, error) {
	repoID := fmt.Sprintf("bitbucket:%s/Repository/%s", p.workspace, slug)
	var resources []cicd.ResourceSpec
	var edges []cicd.EdgeSpec

	for _, pl := range pipelines {
		pipelineID := fmt.Sprintf("bitbucket:%s/Pipeline/%s/%s",
			p.workspace, slug, pl.Name)

		content, _ := json.Marshal(pl) //nolint:errchkjson // struct type cannot fail

		resources = append(resources, cicd.ResourceSpec{
			ID:           pipelineID,
			Name:         slug + "/" + pl.Name,
			ResourceType: "pipeline",
			Provider:     "bitbucket",
			Content:      content,
			Metadata: map[string]string{
				"workspace":   p.workspace,
				"repo":        slug,
				"trigger_key": pl.TriggerKey,
			},
		})

		edges = append(edges, cicd.EdgeSpec{
			SourceID:     pipelineID,
			TargetID:     repoID,
			Relationship: kgtypes.EdgeBelongsTo,
		})

		edges = append(edges, p.buildStepEdges(pipelineID, slug, pl.Steps)...)
	}

	return resources, edges, nil
}

// buildStepEdges creates edges for runner refs, secrets, and deployments.
func (p *pipelinesConfigCollector) buildStepEdges(
	pipelineID, slug string, steps []parsedStep,
) []cicd.EdgeSpec {
	var edges []cicd.EdgeSpec

	for _, step := range steps {
		// Deployment → environment edge.
		if step.Deployment != "" {
			envID := fmt.Sprintf("bitbucket:%s/Environment/%s/%s",
				p.workspace, slug, step.Deployment)
			edges = append(edges, cicd.EdgeSpec{
				SourceID:     pipelineID,
				TargetID:     envID,
				Relationship: kgtypes.EdgeDeploysTo,
			})
		}

		// Runner label → runs-on edge.
		for _, label := range step.RunsOn {
			labelID := fmt.Sprintf("bitbucket:%s/Label/%s",
				p.workspace, label)
			edges = append(edges, cicd.EdgeSpec{
				SourceID:     pipelineID,
				TargetID:     labelID,
				Relationship: kgtypes.EdgeRunsIn,
			})
		}

		// Variable references → uses-secret edges.
		for _, varRef := range step.VarRefs {
			varID := fmt.Sprintf("bitbucket:%s/Variable/%s",
				p.workspace, varRef)
			edges = append(edges, cicd.EdgeSpec{
				SourceID:     pipelineID,
				TargetID:     varID,
				Relationship: kgtypes.EdgeUsesSecret,
			})
		}
	}

	return edges
}
