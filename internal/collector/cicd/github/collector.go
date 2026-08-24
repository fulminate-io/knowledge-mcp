// SPDX-License-Identifier: Apache-2.0

package github

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func init() {
	collector.Register(&GitHubCollector{})
}

// GitHubCollector collects GitHub Actions workflows, environments, and runners.
// Auth is handled via GITHUB_TOKEN (falling back to GH_TOKEN).
// The graph name is "github-{org}".
type GitHubCollector struct{}

// Name returns the collector identifier used for registry lookup.
func (c *GitHubCollector) Name() string { return "github" }

// Collect discovers GitHub Actions CI/CD resources for the given org/user.
func (c *GitHubCollector) Collect(
	ctx context.Context,
	id string,
	opts collector.CollectOptions,
) (*collectorwire.CollectResult, error) {
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		token = os.Getenv("GH_TOKEN")
	}
	if token == "" {
		return nil, fmt.Errorf("github: GITHUB_TOKEN or GH_TOKEN environment variable required")
	}

	slog.Info("github: collecting", "org", id)

	subs := buildSubCollectors(token, id)

	nodes, edges, err := cicd.RunSubCollectors(ctx, subs, cicd.RunOptions{
		OnProgress: opts.OnProgress,
	})
	if err != nil {
		slog.Warn("github: partial collection errors", "error", err)
	}

	return &collectorwire.CollectResult{
		GraphType: kgtypes.GraphCICD,
		GraphName: "github-" + id,
		Nodes:     nodes,
		Edges:     edges,
		// The enumeration was complete only if no subcollector failed. A partial
		// enumeration must never assert a complete walk: walk_complete is what arms
		// the server's whole-remainder deletion basis, so a resource this run failed
		// to READ would be named as deleted. The Warn above stays the operator signal.
		WalkComplete: err == nil,
	}, nil
}

// buildSubCollectors creates all GitHub subcollectors.
func buildSubCollectors(token, org string) []cicd.SubCollector {
	client := newGitHubClient(token)
	api := newAPIClients(client)
	return []cicd.SubCollector{
		// Phase 2: repos (dependency for all others)
		&reposCollector{client: api.repos, org: org},
		// Phase 3: core CI/CD execution model
		&workflowsCollector{actions: api.actions, repos: api.repos, org: org},
		&workflowRunsCollector{actions: api.actions, repos: api.repos, org: org},
		&runnersCollector{actions: api.actions, repos: api.repos, org: org},
		// Phase 4: deployment infrastructure
		&environmentsCollector{repos: api.repos, org: org},
		&deploymentsCollector{repos: api.repos, org: org},
		&secretsCollector{actions: api.actions, repos: api.repos, org: org},
	}
}
