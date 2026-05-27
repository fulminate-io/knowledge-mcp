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

// runnersCollector lists org-level and repo-level self-hosted runners.
// Each runner node links to its org/repo via BELONGS_TO and to each
// runner label via HAS_LABEL.
type runnersCollector struct {
	actions actionsAPI
	repos   reposAPI
	org     string
}

func (c *runnersCollector) Name() string { return "github-runners" }

// Collect lists org runners and per-repo runners.
func (c *runnersCollector) Collect(ctx context.Context) (cicd.SubCollectorResult, error) {
	var result cicd.SubCollectorResult

	if err := c.collectOrgRunners(ctx, &result); err != nil {
		slog.Warn("github-runners: org error", "org", c.org, "error", err)
	}

	repoNames, err := listRepoNames(ctx, c.repos, c.org)
	if err != nil {
		return cicd.SubCollectorResult{}, err
	}
	for _, fullName := range repoNames {
		owner, repo := splitFullName(fullName)
		if err := c.collectRepoRunners(ctx, owner, repo, fullName, &result); err != nil {
			slog.Warn("github-runners: repo error", "repo", fullName, "error", err)
		}
	}

	slog.Debug("github-runners: collected", "org", c.org, "runners", len(result.Resources))
	return result, nil
}

// collectOrgRunners lists org-level self-hosted runners.
func (c *runnersCollector) collectOrgRunners(ctx context.Context, result *cicd.SubCollectorResult) error {
	opts := &gogithub.ListRunnersOptions{ListOptions: gogithub.ListOptions{PerPage: 100}}
	runners, _, err := c.actions.ListOrganizationRunners(ctx, c.org, opts)
	if err != nil {
		return fmt.Errorf("github-runners: list org: %w", err)
	}
	if runners == nil {
		return nil
	}
	for _, r := range runners.Runners {
		spec, edges := buildRunnerResource(c.org, "", r, orgID(c.org))
		result.Resources = append(result.Resources, spec)
		result.Edges = append(result.Edges, edges...)
	}
	return nil
}

// collectRepoRunners lists repo-level self-hosted runners.
func (c *runnersCollector) collectRepoRunners(
	ctx context.Context, owner, repo, fullName string, result *cicd.SubCollectorResult,
) error {
	opts := &gogithub.ListRunnersOptions{ListOptions: gogithub.ListOptions{PerPage: 100}}
	runners, _, err := c.actions.ListRunners(ctx, owner, repo, opts)
	if err != nil {
		return fmt.Errorf("github-runners: list %s: %w", fullName, err)
	}
	if runners == nil {
		return nil
	}
	for _, r := range runners.Runners {
		spec, edges := buildRunnerResource(c.org, fullName, r, repoID(c.org, fullName))
		result.Resources = append(result.Resources, spec)
		result.Edges = append(result.Edges, edges...)
	}
	return nil
}

// buildRunnerResource creates a runner ResourceSpec and its edges.
func buildRunnerResource(org, repoFullName string, r *gogithub.Runner, parentID string) (cicd.ResourceSpec, []cicd.EdgeSpec) {
	runnerNodeID := runnerID(org, r.GetID())
	content, _ := json.Marshal(runnerMetadata{ //nolint:errchkjson // known struct
		Name:   r.GetName(),
		OS:     r.GetOS(),
		Status: r.GetStatus(),
		Busy:   r.GetBusy(),
		Labels: runnerLabelNames(r),
	})

	meta := map[string]string{
		"org":    org,
		"status": r.GetStatus(),
		"os":     r.GetOS(),
	}
	if repoFullName != "" {
		meta["repo"] = repoFullName
	}

	spec := cicd.ResourceSpec{
		ID:           runnerNodeID,
		Name:         r.GetName(),
		ResourceType: "runner",
		Provider:     "github",
		Content:      content,
		Metadata:     meta,
	}

	edges := []cicd.EdgeSpec{{
		SourceID: runnerNodeID, TargetID: parentID,
		Relationship: kgtypes.EdgeBelongsTo,
	}}

	for _, label := range runnerLabelNames(r) {
		labelNodeID := labelID(org, label)
		edges = append(edges, cicd.EdgeSpec{
			SourceID: runnerNodeID, TargetID: labelNodeID,
			Relationship: kgtypes.EdgeHasLabel,
		})
	}

	return spec, edges
}

func runnerLabelNames(r *gogithub.Runner) []string {
	labels := r.Labels
	names := make([]string, 0, len(labels))
	for _, l := range labels {
		if l.GetName() != "" {
			names = append(names, l.GetName())
		}
	}
	return names
}

type runnerMetadata struct {
	Name   string   `json:"name"`
	OS     string   `json:"os"`
	Status string   `json:"status"`
	Busy   bool     `json:"busy"`
	Labels []string `json:"labels"`
}

func runnerID(org string, id int64) string {
	return fmt.Sprintf("github:%s/Runner/%d", org, id)
}

func labelID(org, label string) string {
	return fmt.Sprintf("github:%s/Label/%s", org, label)
}
