// SPDX-License-Identifier: Apache-2.0

package bitbucket

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

func init() {
	collector.Register(&BitbucketCollector{})
}

// BitbucketCollector collects Bitbucket Pipelines CI/CD resources.
// Auth requires both BITBUCKET_USERNAME and BITBUCKET_APP_PASSWORD.
// The graph name is "bitbucket-{workspace}".
type BitbucketCollector struct{}

// Name returns the collector identifier used for registry lookup.
func (c *BitbucketCollector) Name() string { return "bitbucket" }

// Collect discovers Bitbucket Pipelines CI/CD resources for the given workspace.
// It fetches repos first, then passes the repo list to all other subcollectors.
func (c *BitbucketCollector) Collect(
	ctx context.Context,
	id string,
	opts collector.CollectOptions,
) (*collectorwire.CollectResult, error) {
	username := os.Getenv("BITBUCKET_USERNAME")
	appPassword := os.Getenv("BITBUCKET_APP_PASSWORD")
	if username == "" || appPassword == "" {
		return nil, fmt.Errorf("bitbucket: both BITBUCKET_USERNAME and BITBUCKET_APP_PASSWORD environment variables required")
	}

	slog.Info("bitbucket: collecting", "workspace", id)

	client := NewClient(username, appPassword)
	nodes, edges, err := collectWithSharedRepos(ctx, client, id, opts)
	if err != nil {
		slog.Warn("bitbucket: partial collection errors", "error", err)
	}

	return &collectorwire.CollectResult{
		GraphType: kgtypes.GraphCICD,
		GraphName: "bitbucket-" + id,
		Nodes:     nodes,
		Edges:     edges,
	}, nil
}

// collectWithSharedRepos runs repos subcollector first, then passes the repo
// list to all other subcollectors (OQ-1 decision: shared pre-fetched repo list).
func collectWithSharedRepos(
	ctx context.Context,
	client *Client,
	workspace string,
	opts collector.CollectOptions,
) ([]*knowledgev1.Node, []kgwire.BatchEdge, error) {
	// Phase 1: fetch repos.
	reposSub := newReposCollector(client, workspace)
	repoResult, err := reposSub.Collect(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("repos subcollector: %w", err)
	}

	repos := buildRepoInfoList(repoResult)
	slog.Info("bitbucket: discovered repos", "count", len(repos))

	// Phase 2: run remaining subcollectors with shared repo list.
	subs := buildSubCollectors(client, workspace, repos)
	nodes, edges, runErr := cicd.RunSubCollectors(ctx, subs, cicd.RunOptions{
		OnProgress: opts.OnProgress,
	})

	// Merge repo results into the output.
	repoNodes, repoEdges := convertRepoResult(repoResult)
	nodes = append(repoNodes, nodes...)
	edges = append(repoEdges, edges...)

	return nodes, edges, runErr
}

// buildSubCollectors creates the 6 non-repos subcollectors.
// OIDC federation runs as PostPopulate, not as a subcollector.
func buildSubCollectors(
	client *Client, workspace string, repos []repoInfo,
) []cicd.SubCollector {
	return []cicd.SubCollector{
		newPipelinesConfigCollector(client, workspace, repos),
		newPipelineRunsCollector(client, workspace, repos),
		newRunnersCollector(client, workspace, repos),
		newEnvironmentsCollector(client, workspace, repos),
		newVariablesCollector(client, workspace, repos),
	}
}

// convertRepoResult converts a SubCollectorResult to wire types.
func convertRepoResult(result cicd.SubCollectorResult) ([]*knowledgev1.Node, []kgwire.BatchEdge) {
	var nodes []*knowledgev1.Node
	for _, res := range result.Resources {
		nodes = append(nodes, cicd.BuildNode(res))
	}
	var edges []kgwire.BatchEdge
	for _, e := range result.Edges {
		edges = append(edges, cicd.BuildEdge(e))
	}
	return nodes, edges
}

// buildRepoInfoList extracts repoInfo from the repos subcollector result.
func buildRepoInfoList(result cicd.SubCollectorResult) []repoInfo {
	var repos []repoInfo
	for _, res := range result.Resources {
		if res.ResourceType != "repository" {
			continue
		}
		repos = append(repos, repoInfo{
			Slug:       res.Metadata["slug"],
			Mainbranch: res.Metadata["mainbranch"],
		})
	}
	return repos
}
