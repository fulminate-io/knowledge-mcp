// SPDX-License-Identifier: Apache-2.0

package gitlab

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
	collector.Register(&GitLabCollector{})
}

// GitLabCollector collects GitLab CI/CD pipelines, environments, and runners.
// Auth is handled via GITLAB_TOKEN (falling back to GITLAB_PRIVATE_TOKEN).
// The graph name is "gitlab-{group}".
type GitLabCollector struct{}

// Name returns the collector identifier used for registry lookup.
func (c *GitLabCollector) Name() string { return "gitlab" }

// Collect discovers GitLab CI/CD resources for the given group/project.
func (c *GitLabCollector) Collect(
	ctx context.Context,
	id string,
	opts collector.CollectOptions,
) (*collectorwire.CollectResult, error) {
	token := os.Getenv("GITLAB_TOKEN")
	if token == "" {
		token = os.Getenv("GITLAB_PRIVATE_TOKEN")
	}
	if token == "" {
		return nil, fmt.Errorf("gitlab: GITLAB_TOKEN or GITLAB_PRIVATE_TOKEN environment variable required")
	}

	slog.Info("gitlab: collecting", "group", id)

	subs := buildSubCollectors(token, id)
	if subs == nil {
		return nil, fmt.Errorf("gitlab: failed to initialize subcollectors")
	}

	nodes, edges, err := cicd.RunSubCollectors(ctx, subs, cicd.RunOptions{
		OnProgress: opts.OnProgress,
	})
	if err != nil {
		slog.Warn("gitlab: partial collection errors", "error", err)
	}

	return &collectorwire.CollectResult{
		GraphType: kgtypes.GraphCICD,
		GraphName: GraphName(id),
		Nodes:     nodes,
		Edges:     edges,
		// The enumeration was complete only if no subcollector failed. A partial
		// enumeration must never assert a complete walk: walk_complete is what arms
		// the server's whole-remainder deletion basis, so a resource this run failed
		// to READ would be named as deleted. The Warn above stays the operator signal.
		WalkComplete: err == nil,
		// OIDC federation runs as a PostPopulate hook (register_postpopulate.go),
		// fired after upload so it reads cloud graphs over the wire — not as a
		// collect-time subcollector reading the (nil) client store engine.
	}, nil
}

// buildSubCollectors creates all GitLab subcollectors with shared client
// and project lister. The project lister uses sync.Once to fetch group
// projects exactly once regardless of how many subcollectors need the list.
func buildSubCollectors(token, group string) []cicd.SubCollector {
	client, _, err := newClient(token)
	if err != nil {
		slog.Error("gitlab: failed to create client", "error", err)
		return nil
	}

	lister := &projectLister{client: client, group: group}

	subs := []cicd.SubCollector{
		&projectsSub{client: client, group: group, lister: lister},
		&pipelinesSub{client: client, group: group, lister: lister},
		&pipelineRunsSub{client: client, group: group, lister: lister},
		&runnersSub{client: client, group: group, lister: lister},
		&environmentsSub{client: client, group: group, lister: lister},
		&deploymentsSub{client: client, group: group, lister: lister},
		&variablesSub{client: client, group: group, lister: lister},
	}

	return subs
}
