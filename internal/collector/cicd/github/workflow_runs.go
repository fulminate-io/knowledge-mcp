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

const defaultMaxRuns = 10

// workflowRunsCollector lists recent workflow runs per repo. Each run node
// links to its parent workflow via BELONGS_TO and to the trigger event via
// TRIGGERED_BY.
type workflowRunsCollector struct {
	actions actionsAPI
	repos   reposAPI
	org     string
	maxRuns int
}

func (c *workflowRunsCollector) Name() string { return "github-workflow-runs" }

// Collect lists recent runs per repo and emits run nodes + edges.
func (c *workflowRunsCollector) Collect(ctx context.Context) (cicd.SubCollectorResult, error) {
	var result cicd.SubCollectorResult

	repoNames, err := listRepoNames(ctx, c.repos, c.org)
	if err != nil {
		return cicd.SubCollectorResult{}, err
	}

	maxRuns := c.maxRuns
	if maxRuns <= 0 {
		maxRuns = defaultMaxRuns
	}

	for _, fullName := range repoNames {
		owner, repo := splitFullName(fullName)
		if err := c.collectRunsForRepo(ctx, owner, repo, fullName, maxRuns, &result); err != nil {
			slog.Warn("github-workflow-runs: repo error", "repo", fullName, "error", err)
		}
	}
	slog.Debug("github-workflow-runs: collected", "org", c.org, "runs", len(result.Resources))
	return result, nil
}

// collectRunsForRepo fetches recent runs for one repo.
func (c *workflowRunsCollector) collectRunsForRepo(
	ctx context.Context, owner, repo, fullName string, maxRuns int, result *cicd.SubCollectorResult,
) error {
	opts := &gogithub.ListWorkflowRunsOptions{
		ListOptions: gogithub.ListOptions{PerPage: min(maxRuns, 100)},
	}
	runs, _, err := c.actions.ListRepositoryWorkflowRuns(ctx, owner, repo, opts)
	if err != nil {
		return fmt.Errorf("github-workflow-runs: list %s: %w", fullName, err)
	}
	if runs == nil {
		return nil
	}
	for i, run := range runs.WorkflowRuns {
		if i >= maxRuns {
			break
		}
		buildWorkflowRun(c.org, fullName, run, result)
	}
	return nil
}

// buildWorkflowRun creates a run node with metadata and edges.
func buildWorkflowRun(org, fullName string, run *gogithub.WorkflowRun, result *cicd.SubCollectorResult) {
	runNodeID := workflowRunID(org, fullName, run.GetID())

	content, _ := json.Marshal(runMetadata{ //nolint:errchkjson // known struct
		Status:     run.GetStatus(),
		Conclusion: run.GetConclusion(),
		Branch:     run.GetHeadBranch(),
		Event:      run.GetEvent(),
		HTMLURL:    run.GetHTMLURL(),
		RunNumber:  run.GetRunNumber(),
	})

	spec := cicd.ResourceSpec{
		ID:           runNodeID,
		Name:         fmt.Sprintf("%s #%d", run.GetName(), run.GetRunNumber()),
		ResourceType: "workflow_run",
		Provider:     "github",
		Content:      content,
		Metadata: map[string]string{
			"org":        org,
			"repo":       fullName,
			"status":     run.GetStatus(),
			"conclusion": run.GetConclusion(),
			"event":      run.GetEvent(),
		},
	}
	result.Resources = append(result.Resources, spec)

	// BELONGS_TO → workflow
	wfPath := run.GetPath()
	if wfPath != "" {
		result.Edges = append(result.Edges, cicd.EdgeSpec{
			SourceID: runNodeID, TargetID: workflowID(org, fullName, wfPath),
			Relationship: kgtypes.EdgeBelongsTo,
		})
	}

	// TRIGGERED_BY event type
	if run.GetEvent() != "" {
		result.Edges = append(result.Edges, cicd.EdgeSpec{
			SourceID:     runNodeID,
			TargetID:     repoID(org, fullName),
			Relationship: kgtypes.EdgeTriggeredBy,
			Metadata:     map[string]string{"event": run.GetEvent()},
		})
	}
}

type runMetadata struct {
	Status     string `json:"status"`
	Conclusion string `json:"conclusion,omitempty"`
	Branch     string `json:"branch,omitempty"`
	Event      string `json:"event"`
	HTMLURL    string `json:"html_url,omitempty"`
	RunNumber  int    `json:"run_number"`
}

func workflowRunID(org, repoFullName string, runID int64) string {
	return fmt.Sprintf("github:%s/WorkflowRun/%s/%d", org, repoFullName, runID)
}

// listRepoNames is a shared helper that lists non-archived repo full names.
func listRepoNames(ctx context.Context, client reposAPI, org string) ([]string, error) {
	var names []string
	opts := &gogithub.RepositoryListByOrgOptions{
		ListOptions: gogithub.ListOptions{PerPage: 100}, Type: "all",
	}
	for {
		repos, resp, err := client.ListByOrg(ctx, org, opts)
		if err != nil {
			return nil, fmt.Errorf("list repos: %w", err)
		}
		for _, r := range repos {
			if !r.GetArchived() {
				names = append(names, r.GetFullName())
			}
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return names, nil
}
