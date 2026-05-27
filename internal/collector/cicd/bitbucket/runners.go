// SPDX-License-Identifier: Apache-2.0

package bitbucket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// runnersCollector discovers Bitbucket Pipelines runners at workspace and
// repository levels.
type runnersCollector struct {
	client    *Client
	workspace string
	repos     []repoInfo
}

func newRunnersCollector(
	client *Client, workspace string, repos []repoInfo,
) *runnersCollector {
	return &runnersCollector{client: client, workspace: workspace, repos: repos}
}

func (r *runnersCollector) Name() string { return "bitbucket-runners" }

// Collect fetches workspace-level and per-repo runners, deduplicating by UUID.
func (r *runnersCollector) Collect(ctx context.Context) (cicd.SubCollectorResult, error) {
	seen := make(map[string]struct{})
	var result cicd.SubCollectorResult

	// Workspace runners.
	wsRunners, err := r.fetchRunners(ctx,
		fmt.Sprintf("workspaces/%s/pipelines-config/runners", r.workspace))
	if err != nil {
		slog.Debug("bitbucket: workspace runners skip", "error", err)
	} else {
		r.appendRunners(&result, wsRunners, "workspace", "", seen)
	}

	// Per-repo runners.
	for _, repo := range r.repos {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		path := fmt.Sprintf("repositories/%s/%s/pipelines-config/runners",
			r.workspace, repo.Slug)
		repoRunners, err := r.fetchRunners(ctx, path)
		if err != nil {
			slog.Debug("bitbucket: repo runners skip",
				"repo", repo.Slug, "error", err)
			continue
		}
		r.appendRunners(&result, repoRunners, "repository", repo.Slug, seen)
	}

	return result, nil
}

// fetchRunners fetches runners from a paginated endpoint.
func (r *runnersCollector) fetchRunners(
	ctx context.Context, path string,
) ([]apiRunner, error) {
	var runners []apiRunner
	err := r.client.GetPaginated(ctx, path, func(raw json.RawMessage) error {
		var page []apiRunner
		if err := json.Unmarshal(raw, &page); err != nil {
			return fmt.Errorf("unmarshal runners: %w", err)
		}
		runners = append(runners, page...)
		return nil
	})
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == 404 {
			return nil, nil
		}
		return nil, err
	}
	return runners, nil
}

// appendRunners adds runner resources and edges, deduplicating by UUID.
func (r *runnersCollector) appendRunners(
	result *cicd.SubCollectorResult,
	runners []apiRunner,
	scope, repoSlug string,
	seen map[string]struct{},
) {
	wsID := fmt.Sprintf("bitbucket:%s/Workspace/%s", r.workspace, r.workspace)

	for _, runner := range runners {
		if _, dup := seen[runner.UUID]; dup {
			continue
		}
		seen[runner.UUID] = struct{}{}

		runnerID := fmt.Sprintf("bitbucket:%s/Runner/%s",
			r.workspace, runner.UUID)
		content, _ := json.Marshal(runner) //nolint:errchkjson // struct type cannot fail

		labels := extractLabelNames(runner.Labels)
		meta := map[string]string{
			"workspace": r.workspace,
			"state":     runner.State.Status,
			"scope":     scope,
		}
		if repoSlug != "" {
			meta["repo"] = repoSlug
		}
		if len(labels) > 0 {
			meta["labels"] = strings.Join(labels, ",")
		}

		result.Resources = append(result.Resources, cicd.ResourceSpec{
			ID:           runnerID,
			Name:         runner.Name,
			ResourceType: "runner",
			Provider:     "bitbucket",
			Content:      content,
			Metadata:     meta,
		})

		// BelongsTo → workspace or repo.
		belongsTo := wsID
		if repoSlug != "" {
			belongsTo = fmt.Sprintf("bitbucket:%s/Repository/%s",
				r.workspace, repoSlug)
		}
		result.Edges = append(result.Edges, cicd.EdgeSpec{
			SourceID:     runnerID,
			TargetID:     belongsTo,
			Relationship: kgtypes.EdgeBelongsTo,
		})

		// HasLabel → label nodes.
		for _, label := range labels {
			labelID := fmt.Sprintf("bitbucket:%s/Label/%s",
				r.workspace, label)
			result.Resources = append(result.Resources, cicd.ResourceSpec{
				ID:           labelID,
				Name:         label,
				ResourceType: "label",
				Provider:     "bitbucket",
				Metadata:     map[string]string{"workspace": r.workspace},
			})
			result.Edges = append(result.Edges, cicd.EdgeSpec{
				SourceID:     runnerID,
				TargetID:     labelID,
				Relationship: kgtypes.EdgeHasLabel,
			})
		}
	}
}

// extractLabelNames returns label name strings from the API label objects.
func extractLabelNames(labels []apiLabel) []string {
	out := make([]string, 0, len(labels))
	for _, l := range labels {
		if l.Name != "" {
			out = append(out, l.Name)
		}
	}
	return out
}

// apiRunner is the Bitbucket runner API response shape.
type apiRunner struct {
	UUID   string     `json:"uuid"`
	Name   string     `json:"name"`
	Labels []apiLabel `json:"labels"`
	State  struct {
		Status string `json:"status"`
	} `json:"state"`
}

// apiLabel is a runner label from the API.
type apiLabel struct {
	Name string `json:"name"`
}
