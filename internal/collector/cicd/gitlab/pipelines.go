// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"

	gl "gitlab.com/gitlab-org/api/client-go"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// pipelinesSub collects GitLab CI pipeline definitions by parsing
// .gitlab-ci.yml from each project's default branch.
type pipelinesSub struct {
	client *gl.Client
	group  string
	lister *projectLister
}

func (s *pipelinesSub) Name() string { return "gitlab-pipelines" }

// Collect fetches .gitlab-ci.yml for each project, parses it, and emits
// pipeline resources with edges for secrets, environments, and runner tags.
func (s *pipelinesSub) Collect(ctx context.Context) (cicd.SubCollectorResult, error) {
	projects, err := s.lister.list(ctx)
	if err != nil {
		return cicd.SubCollectorResult{}, fmt.Errorf("gitlab-pipelines: %w", err)
	}

	var result cicd.SubCollectorResult
	for _, p := range projects {
		if p.DefaultBranch == "" {
			continue
		}
		if err := s.collectProject(ctx, p, &result); err != nil {
			slog.Warn("gitlab-pipelines: project error", "project", p.PathWithNamespace, "error", err)
		}
	}
	return result, nil
}

// collectProject fetches and parses .gitlab-ci.yml for a single project.
func (s *pipelinesSub) collectProject(
	ctx context.Context, p *gl.Project, result *cicd.SubCollectorResult,
) error {
	raw, err := s.fetchCIFile(ctx, p)
	if err != nil {
		return err
	}
	if raw == nil {
		return nil // no .gitlab-ci.yml
	}

	cfg, err := parseGitLabCI(raw)
	if err != nil {
		slog.Warn("gitlab-pipelines: parse error", "project", p.PathWithNamespace, "error", err)
		return nil // skip unparseable files
	}

	pipelineID := fmt.Sprintf("gitlab:%s/Pipeline/%s/%s", s.group, p.PathWithNamespace, p.DefaultBranch)
	projectID := fmt.Sprintf("gitlab:%s/Project/%s", s.group, p.PathWithNamespace)

	result.Resources = append(result.Resources, s.buildPipelineSpec(p, pipelineID, cfg, raw))
	result.Edges = append(result.Edges, cicd.EdgeSpec{
		SourceID: pipelineID, TargetID: projectID, Relationship: kgtypes.EdgeBelongsTo,
	})

	s.emitJobEdges(p, pipelineID, cfg, result)
	return nil
}

// fetchCIFile retrieves .gitlab-ci.yml from the project's default branch.
// Returns nil, nil if the file does not exist.
func (s *pipelinesSub) fetchCIFile(ctx context.Context, p *gl.Project) ([]byte, error) {
	opts := &gl.GetFileOptions{Ref: new(p.DefaultBranch)}
	f, resp, err := s.client.RepositoryFiles.GetFile(p.ID, ".gitlab-ci.yml", opts, gl.WithContext(ctx))
	if err != nil {
		if resp != nil && resp.StatusCode == 404 {
			return nil, nil
		}
		return nil, err
	}

	content, err := base64.StdEncoding.DecodeString(f.Content)
	if err != nil {
		return nil, fmt.Errorf("decode .gitlab-ci.yml base64: %w", err)
	}
	return content, nil
}

// buildPipelineSpec creates a ResourceSpec for a pipeline definition.
func (s *pipelinesSub) buildPipelineSpec(
	p *gl.Project, pipelineID string, cfg *pipelineConfig, raw []byte,
) cicd.ResourceSpec {
	meta := map[string]string{
		"project":    p.PathWithNamespace,
		"ref":        p.DefaultBranch,
		"job_count":  fmt.Sprintf("%d", len(cfg.Jobs)),
		"has_stages": fmt.Sprintf("%t", len(cfg.Stages) > 0),
	}
	if len(cfg.Includes) > 0 {
		meta["has_includes"] = "true"
	}

	return cicd.ResourceSpec{
		ID:           pipelineID,
		Name:         fmt.Sprintf("%s pipeline", p.PathWithNamespace),
		ResourceType: "pipeline",
		Provider:     "gitlab",
		Content:      raw,
		Metadata:     meta,
	}
}

// emitJobEdges emits edges from pipeline jobs to secrets, environments,
// and runner tags.
func (s *pipelinesSub) emitJobEdges(
	p *gl.Project, pipelineID string, cfg *pipelineConfig, result *cicd.SubCollectorResult,
) {
	for _, job := range cfg.Jobs {
		s.emitSingleJobEdges(p, pipelineID, job, result)
	}
}

// emitSingleJobEdges emits edges for a single parsed job definition.
func (s *pipelinesSub) emitSingleJobEdges(
	p *gl.Project, pipelineID string, job jobDef, result *cicd.SubCollectorResult,
) {
	// Runner tag references.
	for _, tag := range job.Tags {
		tagID := fmt.Sprintf("gitlab:%s/RunnerTag/%s", s.group, tag)
		result.Edges = append(result.Edges, cicd.EdgeSpec{
			SourceID: pipelineID, TargetID: tagID, Relationship: kgtypes.EdgeRunsIn,
		})
	}

	// Environment reference.
	if job.Environment != "" {
		envID := fmt.Sprintf("gitlab:%s/Environment/%s/%s", s.group, p.PathWithNamespace, job.Environment)
		result.Edges = append(result.Edges, cicd.EdgeSpec{
			SourceID: pipelineID, TargetID: envID, Relationship: kgtypes.EdgeDeploysTo,
		})
	}

	// Variable references extracted from scripts.
	for _, varName := range job.VarRefs {
		varID := fmt.Sprintf("gitlab:%s/Variable/%s/%s", s.group, p.PathWithNamespace, varName)
		result.Edges = append(result.Edges, cicd.EdgeSpec{
			SourceID: pipelineID, TargetID: varID, Relationship: kgtypes.EdgeUsesSecret,
		})
	}
}
