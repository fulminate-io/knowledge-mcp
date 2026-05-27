// SPDX-License-Identifier: Apache-2.0

package bitbucket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

const defaultHistoryDepth = 50

// errLimitReached signals pagination should stop (not a real error).
var errLimitReached = errors.New("limit reached")

// pipelineRunsCollector fetches recent pipeline runs per repository.
type pipelineRunsCollector struct {
	client    *Client
	workspace string
	repos     []repoInfo
	maxRuns   int
}

func newPipelineRunsCollector(
	client *Client, workspace string, repos []repoInfo,
) *pipelineRunsCollector {
	depth := defaultHistoryDepth
	if v := os.Getenv("BITBUCKET_PIPELINE_HISTORY_DEPTH"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			depth = n
		}
	}
	return &pipelineRunsCollector{
		client:    client,
		workspace: workspace,
		repos:     repos,
		maxRuns:   depth,
	}
}

func (p *pipelineRunsCollector) Name() string { return "bitbucket-pipeline-runs" }

// Collect fetches the last N pipeline runs per repo.
func (p *pipelineRunsCollector) Collect(ctx context.Context) (cicd.SubCollectorResult, error) {
	var result cicd.SubCollectorResult

	for _, repo := range p.repos {
		if err := ctx.Err(); err != nil {
			return result, err
		}

		resources, edges, err := p.collectRepo(ctx, repo.Slug)
		if err != nil {
			slog.Debug("bitbucket: pipeline runs skip",
				"repo", repo.Slug, "error", err)
			continue
		}
		result.Resources = append(result.Resources, resources...)
		result.Edges = append(result.Edges, edges...)
	}

	return result, nil
}

// collectRepo fetches pipeline runs for a single repo.
func (p *pipelineRunsCollector) collectRepo(
	ctx context.Context, slug string,
) ([]cicd.ResourceSpec, []cicd.EdgeSpec, error) {
	path := fmt.Sprintf("repositories/%s/%s/pipelines?sort=-created_on&pagelen=%d",
		p.workspace, slug, min(p.maxRuns, maxPagelen))

	runs, err := p.fetchRuns(ctx, path, slug)
	if err != nil {
		return nil, nil, err
	}

	return p.buildRunResources(slug, runs)
}

// fetchRuns fetches pipeline runs, stopping after maxRuns.
func (p *pipelineRunsCollector) fetchRuns(
	ctx context.Context, path, slug string,
) ([]apiPipelineRun, error) {
	var runs []apiPipelineRun
	err := p.client.GetPaginated(ctx, path, func(raw json.RawMessage) error {
		var page []apiPipelineRun
		if err := json.Unmarshal(raw, &page); err != nil {
			return fmt.Errorf("unmarshal pipeline runs: %w", err)
		}
		runs = append(runs, page...)
		if len(runs) >= p.maxRuns {
			runs = runs[:p.maxRuns]
			return errLimitReached
		}
		return nil
	})
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == 404 {
			return nil, nil
		}
		// errLimitReached is expected when we cap at maxRuns.
		if !errors.Is(err, errLimitReached) {
			return nil, fmt.Errorf("fetch pipeline runs for %s: %w", slug, err)
		}
	}
	return runs, nil
}

// buildRunResources converts API runs into resource and edge specs.
func (p *pipelineRunsCollector) buildRunResources(
	slug string, runs []apiPipelineRun,
) ([]cicd.ResourceSpec, []cicd.EdgeSpec, error) {
	repoID := fmt.Sprintf("bitbucket:%s/Repository/%s", p.workspace, slug)
	var resources []cicd.ResourceSpec
	var edges []cicd.EdgeSpec

	for _, run := range runs {
		runID := fmt.Sprintf("bitbucket:%s/PipelineRun/%s/%s",
			p.workspace, slug, run.UUID)

		content, _ := json.Marshal(run) //nolint:errchkjson // struct type cannot fail
		meta := map[string]string{
			"workspace": p.workspace,
			"repo":      slug,
			"status":    statusName(run.State),
		}
		if run.CreatedOn != "" {
			meta["created_on"] = run.CreatedOn
		}
		if run.CompletedOn != "" {
			meta["completed_on"] = run.CompletedOn
		}
		if run.DurationInSeconds > 0 {
			meta["duration_seconds"] = strconv.Itoa(run.DurationInSeconds)
		}
		if run.Target.RefName != "" {
			meta["branch"] = run.Target.RefName
		}
		if run.Trigger.Type != "" {
			meta["trigger_type"] = run.Trigger.Type
		}

		resources = append(resources, cicd.ResourceSpec{
			ID:           runID,
			Name:         fmt.Sprintf("%s #%d", slug, run.BuildNumber),
			ResourceType: "pipeline_run",
			Provider:     "bitbucket",
			Content:      content,
			Metadata:     meta,
		})

		edges = append(edges, cicd.EdgeSpec{
			SourceID:     runID,
			TargetID:     repoID,
			Relationship: kgtypes.EdgeBelongsTo,
		})
	}

	return resources, edges, nil
}

// statusName extracts a flat status string from the nested state object.
func statusName(state apiState) string {
	if state.Result.Name != "" {
		return state.Result.Name
	}
	if state.Stage.Name != "" {
		return state.Stage.Name
	}
	return state.Name
}

// apiPipelineRun is the Bitbucket API pipeline run response shape.
type apiPipelineRun struct {
	UUID              string     `json:"uuid"`
	BuildNumber       int        `json:"build_number"`
	CreatedOn         string     `json:"created_on"`
	CompletedOn       string     `json:"completed_on"`
	DurationInSeconds int        `json:"duration_in_seconds"`
	State             apiState   `json:"state"`
	Target            apiTarget  `json:"target"`
	Trigger           apiTrigger `json:"trigger"`
}

type apiState struct {
	Name  string `json:"name"`
	Stage struct {
		Name string `json:"name"`
	} `json:"stage"`
	Result struct {
		Name string `json:"name"`
	} `json:"result"`
}

type apiTarget struct {
	RefName string `json:"ref_name"`
	RefType string `json:"ref_type"`
}

type apiTrigger struct {
	Type string `json:"type"`
}
