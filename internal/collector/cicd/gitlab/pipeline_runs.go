// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	gl "gitlab.com/gitlab-org/api/client-go"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

const maxPipelineRuns = 20

// pipelineRunsSub collects recent pipeline runs and their jobs for each project.
type pipelineRunsSub struct {
	client *gl.Client
	group  string
	lister *projectLister
}

func (s *pipelineRunsSub) Name() string { return "gitlab-pipeline-runs" }

// Collect fetches the last maxPipelineRuns pipeline runs per project,
// plus their jobs.
func (s *pipelineRunsSub) Collect(ctx context.Context) (cicd.SubCollectorResult, error) {
	projects, err := s.lister.list(ctx)
	if err != nil {
		return cicd.SubCollectorResult{}, fmt.Errorf("gitlab-pipeline-runs: %w", err)
	}

	var result cicd.SubCollectorResult
	for _, p := range projects {
		if err := s.collectProject(ctx, p, &result); err != nil {
			slog.Warn("gitlab-pipeline-runs: project error", "project", p.PathWithNamespace, "error", err)
		}
	}
	return result, nil
}

// collectProject fetches recent pipelines and their jobs for a single project.
func (s *pipelineRunsSub) collectProject(
	ctx context.Context, p *gl.Project, result *cicd.SubCollectorResult,
) error {
	pipelines, err := s.listRecentPipelines(ctx, p.ID)
	if err != nil {
		return err
	}

	projectID := fmt.Sprintf("gitlab:%s/Project/%s", s.group, p.PathWithNamespace)
	for _, pl := range pipelines {
		s.addPipelineRun(p, pl, projectID, result)
		if err := s.collectJobs(ctx, p, pl, result); err != nil {
			slog.Warn("gitlab-pipeline-runs: jobs error",
				"project", p.PathWithNamespace, "pipeline", pl.ID, "error", err)
		}
	}
	return nil
}

// listRecentPipelines fetches the most recent pipeline runs for a project.
func (s *pipelineRunsSub) listRecentPipelines(ctx context.Context, projectID int64) ([]*gl.PipelineInfo, error) {
	opts := &gl.ListProjectPipelinesOptions{
		ListOptions: gl.ListOptions{PerPage: maxPipelineRuns},
		Sort:        new("desc"),
		OrderBy:     new("id"),
	}
	pipelines, _, err := s.client.Pipelines.ListProjectPipelines(projectID, opts, gl.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	return pipelines, nil
}

// addPipelineRun emits a pipeline run resource and its edge to the project.
func (s *pipelineRunsSub) addPipelineRun(
	p *gl.Project, pl *gl.PipelineInfo, projectID string, result *cicd.SubCollectorResult,
) {
	runID := fmt.Sprintf("gitlab:%s/PipelineRun/%s/%d", s.group, p.PathWithNamespace, pl.ID)
	content, _ := json.Marshal(pl) //nolint:errchkjson // gitlab API struct

	result.Resources = append(result.Resources, cicd.ResourceSpec{
		ID:           runID,
		Name:         fmt.Sprintf("pipeline #%d", pl.ID),
		ResourceType: "pipeline-run",
		Provider:     "gitlab",
		Content:      content,
		Metadata: map[string]string{
			"project": p.PathWithNamespace,
			"ref":     pl.Ref,
			"status":  pl.Status,
			"source":  pl.Source,
			"sha":     pl.SHA,
		},
	})
	result.Edges = append(result.Edges, cicd.EdgeSpec{
		SourceID: runID, TargetID: projectID, Relationship: kgtypes.EdgeBelongsTo,
	})
}

// collectJobs fetches all jobs for a pipeline run.
func (s *pipelineRunsSub) collectJobs(
	ctx context.Context, p *gl.Project, pl *gl.PipelineInfo, result *cicd.SubCollectorResult,
) error {
	opts := &gl.ListJobsOptions{
		ListOptions: gl.ListOptions{PerPage: 100},
	}
	runID := fmt.Sprintf("gitlab:%s/PipelineRun/%s/%d", s.group, p.PathWithNamespace, pl.ID)

	for {
		jobs, resp, err := s.client.Jobs.ListPipelineJobs(p.ID, pl.ID, opts, gl.WithContext(ctx))
		if err != nil {
			return err
		}
		for _, job := range jobs {
			s.addJob(p, job, runID, result)
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return nil
}

// addJob emits a job resource and its edge to the pipeline run.
func (s *pipelineRunsSub) addJob(
	p *gl.Project, job *gl.Job, runID string, result *cicd.SubCollectorResult,
) {
	jobID := fmt.Sprintf("gitlab:%s/Job/%s/%d", s.group, p.PathWithNamespace, job.ID)
	content, _ := json.Marshal(job) //nolint:errchkjson // gitlab API struct

	meta := map[string]string{
		"project": p.PathWithNamespace,
		"stage":   job.Stage,
		"status":  job.Status,
		"name":    job.Name,
	}
	if job.Runner.ID != 0 {
		meta["runner_id"] = fmt.Sprintf("%d", job.Runner.ID)
	}

	result.Resources = append(result.Resources, cicd.ResourceSpec{
		ID:           jobID,
		Name:         job.Name,
		ResourceType: "job",
		Provider:     "gitlab",
		Content:      content,
		Metadata:     meta,
	})
	result.Edges = append(result.Edges, cicd.EdgeSpec{
		SourceID: jobID, TargetID: runID, Relationship: kgtypes.EdgeBelongsTo,
	})

	// Link job to runner if available.
	if job.Runner.ID != 0 {
		rID := fmt.Sprintf("gitlab:%s/Runner/%d", s.group, job.Runner.ID)
		result.Edges = append(result.Edges, cicd.EdgeSpec{
			SourceID: jobID, TargetID: rID, Relationship: kgtypes.EdgeRunsIn,
		})
	}
}
