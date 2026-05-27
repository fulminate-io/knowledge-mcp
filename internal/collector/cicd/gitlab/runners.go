// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"

	gl "gitlab.com/gitlab-org/api/client-go"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// runnersSub collects GitLab runners (group-level and project-level).
type runnersSub struct {
	client *gl.Client
	group  string
	lister *projectLister
}

func (s *runnersSub) Name() string { return "gitlab-runners" }

// Collect discovers group runners and project runners, emitting runner nodes
// with EdgeHasLabel edges for runner tags.
func (s *runnersSub) Collect(ctx context.Context) (cicd.SubCollectorResult, error) {
	var result cicd.SubCollectorResult
	seen := make(map[int64]bool)

	// Group-level runners.
	if err := s.collectGroupRunners(ctx, &result, seen); err != nil {
		slog.Warn("gitlab-runners: group runners error", "error", err)
	}

	// Project-level runners.
	projects, err := s.lister.list(ctx)
	if err != nil {
		return cicd.SubCollectorResult{}, fmt.Errorf("gitlab-runners: %w", err)
	}
	for _, p := range projects {
		if err := s.collectProjectRunners(ctx, p, &result, seen); err != nil {
			slog.Warn("gitlab-runners: project runners error", "project", p.PathWithNamespace, "error", err)
		}
	}

	return result, nil
}

// collectGroupRunners lists all runners available at the group level.
func (s *runnersSub) collectGroupRunners(ctx context.Context, result *cicd.SubCollectorResult, seen map[int64]bool) error {
	opts := &gl.ListGroupsRunnersOptions{
		ListOptions: gl.ListOptions{PerPage: 100},
	}
	for {
		runners, resp, err := s.client.Runners.ListGroupsRunners(s.group, opts, gl.WithContext(ctx))
		if err != nil {
			return err
		}
		for _, r := range runners {
			s.addRunner(ctx, r, "group", result, seen)
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return nil
}

// collectProjectRunners lists runners available to a specific project.
func (s *runnersSub) collectProjectRunners(
	ctx context.Context, p *gl.Project, result *cicd.SubCollectorResult, seen map[int64]bool,
) error {
	opts := &gl.ListProjectRunnersOptions{
		ListOptions: gl.ListOptions{PerPage: 100},
	}
	for {
		runners, resp, err := s.client.Runners.ListProjectRunners(p.ID, opts, gl.WithContext(ctx))
		if err != nil {
			return err
		}
		for _, r := range runners {
			s.addRunner(ctx, r, "project", result, seen)
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return nil
}

// addRunner emits a runner resource and tag edges if not already seen.
// Fetches RunnerDetails to get the tag list.
func (s *runnersSub) addRunner(
	ctx context.Context, r *gl.Runner, scope string, result *cicd.SubCollectorResult, seen map[int64]bool,
) {
	if seen[r.ID] {
		return
	}
	seen[r.ID] = true

	runnerID := fmt.Sprintf("gitlab:%s/Runner/%d", s.group, r.ID)
	meta := map[string]string{
		"runner_type": scope,
		"status":      r.Status,
		"active":      strconv.FormatBool(!r.Paused),
		"online":      strconv.FormatBool(r.Online),
	}
	if r.Description != "" {
		meta["description"] = r.Description
	}

	result.Resources = append(result.Resources, cicd.ResourceSpec{
		ID:           runnerID,
		Name:         fmt.Sprintf("runner-%d", r.ID),
		ResourceType: "runner",
		Provider:     "gitlab",
		Metadata:     meta,
	})

	// Fetch details to get tag list.
	details, _, err := s.client.Runners.GetRunnerDetails(r.ID, gl.WithContext(ctx))
	if err != nil {
		slog.Debug("gitlab-runners: failed to get runner details", "id", r.ID, "error", err)
		return
	}

	for _, tag := range details.TagList {
		tagID := fmt.Sprintf("gitlab:%s/RunnerTag/%s", s.group, tag)
		result.Resources = append(result.Resources, cicd.ResourceSpec{
			ID:           tagID,
			Name:         tag,
			ResourceType: "runner-tag",
			Provider:     "gitlab",
		})
		result.Edges = append(result.Edges, cicd.EdgeSpec{
			SourceID:     runnerID,
			TargetID:     tagID,
			Relationship: kgtypes.EdgeHasLabel,
		})
	}
}
