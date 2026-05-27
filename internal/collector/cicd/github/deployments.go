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

const defaultMaxDeployments = 20

// deploymentsCollector lists recent deployments per repo.
type deploymentsCollector struct {
	repos          reposAPI
	org            string
	maxDeployments int
}

func (c *deploymentsCollector) Name() string { return "github-deployments" }

// Collect lists deployments per repo and emits deployment nodes with edges
// to repositories and environments.
func (c *deploymentsCollector) Collect(ctx context.Context) (cicd.SubCollectorResult, error) {
	var result cicd.SubCollectorResult

	repoNames, err := listRepoNames(ctx, c.repos, c.org)
	if err != nil {
		return cicd.SubCollectorResult{}, err
	}

	maxDeploys := c.maxDeployments
	if maxDeploys <= 0 {
		maxDeploys = defaultMaxDeployments
	}

	for _, fullName := range repoNames {
		owner, repo := splitFullName(fullName)
		if err := c.collectDeploymentsForRepo(ctx, owner, repo, fullName, maxDeploys, &result); err != nil {
			slog.Warn("github-deployments: repo error", "repo", fullName, "error", err)
		}
	}
	slog.Debug("github-deployments: collected", "org", c.org, "deployments", len(result.Resources))
	return result, nil
}

// collectDeploymentsForRepo fetches recent deployments for one repo.
func (c *deploymentsCollector) collectDeploymentsForRepo(
	ctx context.Context, owner, repo, fullName string, maxDeploys int, result *cicd.SubCollectorResult,
) error {
	opts := &gogithub.DeploymentsListOptions{
		ListOptions: gogithub.ListOptions{PerPage: min(maxDeploys, 100)},
	}
	deploys, _, err := c.repos.ListDeployments(ctx, owner, repo, opts)
	if err != nil {
		return fmt.Errorf("github-deployments: list %s: %w", fullName, err)
	}
	for i, d := range deploys {
		if i >= maxDeploys {
			break
		}
		buildDeployment(c.org, fullName, d, result)
	}
	return nil
}

// buildDeployment creates a deployment node with metadata and edges.
func buildDeployment(org, fullName string, d *gogithub.Deployment, result *cicd.SubCollectorResult) {
	deployNodeID := deploymentID(org, fullName, d.GetID())

	content, _ := json.Marshal(deployMetadata{ //nolint:errchkjson // known struct
		Environment: d.GetEnvironment(),
		Ref:         d.GetRef(),
		Task:        d.GetTask(),
		Description: d.GetDescription(),
	})

	spec := cicd.ResourceSpec{
		ID:           deployNodeID,
		Name:         fmt.Sprintf("deploy/%s/%s", fullName, d.GetEnvironment()),
		ResourceType: "deployment",
		Provider:     "github",
		Content:      content,
		Metadata: map[string]string{
			"org":         org,
			"repo":        fullName,
			"environment": d.GetEnvironment(),
			"ref":         d.GetRef(),
		},
	}
	result.Resources = append(result.Resources, spec)

	// BELONGS_TO → repo
	result.Edges = append(result.Edges, cicd.EdgeSpec{
		SourceID: deployNodeID, TargetID: repoID(org, fullName),
		Relationship: kgtypes.EdgeBelongsTo,
	})

	// DEPLOYS_TO → environment
	if d.GetEnvironment() != "" {
		result.Edges = append(result.Edges, cicd.EdgeSpec{
			SourceID: deployNodeID, TargetID: environmentID(org, fullName, d.GetEnvironment()),
			Relationship: kgtypes.EdgeDeploysTo,
		})
	}
}

type deployMetadata struct {
	Environment string `json:"environment"`
	Ref         string `json:"ref,omitempty"`
	Task        string `json:"task,omitempty"`
	Description string `json:"description,omitempty"`
}

func deploymentID(org, repoFullName string, id int64) string {
	return fmt.Sprintf("github:%s/Deployment/%s/%d", org, repoFullName, id)
}
