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

// reposCollector lists all repositories for an organization and creates
// org + repo nodes with BELONGS_TO edges. This subcollector runs first
// because all other subcollectors reference repository nodes.
type reposCollector struct {
	client reposAPI
	org    string
}

func (c *reposCollector) Name() string { return "github-repos" }

// Collect lists org repos with pagination and emits org node, repo nodes,
// and BELONGS_TO edges from repo to org.
func (c *reposCollector) Collect(ctx context.Context) (cicd.SubCollectorResult, error) {
	var result cicd.SubCollectorResult

	orgNodeID := orgID(c.org)
	result.Resources = append(result.Resources, cicd.ResourceSpec{
		ID:           orgNodeID,
		Name:         c.org,
		ResourceType: "organization",
		Provider:     "github",
		Metadata:     map[string]string{"org": c.org},
	})

	opts := &gogithub.RepositoryListByOrgOptions{
		ListOptions: gogithub.ListOptions{PerPage: 100},
		Type:        "all",
	}
	for {
		repos, resp, err := c.client.ListByOrg(ctx, c.org, opts)
		if err != nil {
			return cicd.SubCollectorResult{}, fmt.Errorf("github-repos: list: %w", err)
		}
		for _, repo := range repos {
			if repo.GetArchived() {
				continue // skip archived repos
			}
			spec, edges := buildRepoResource(c.org, orgNodeID, repo)
			result.Resources = append(result.Resources, spec)
			result.Edges = append(result.Edges, edges...)
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	slog.Debug("github-repos: collected", "org", c.org, "repos", len(result.Resources)-1)
	return result, nil
}

// buildRepoResource creates a ResourceSpec and edges for a single repository.
func buildRepoResource(org, orgNodeID string, repo *gogithub.Repository) (cicd.ResourceSpec, []cicd.EdgeSpec) {
	repoNodeID := repoID(org, repo.GetFullName())
	content, _ := json.Marshal(repoMetadata{ //nolint:errchkjson // known struct
		DefaultBranch: repo.GetDefaultBranch(),
		Visibility:    repo.GetVisibility(),
		Language:      repo.GetLanguage(),
		Topics:        repo.Topics,
	})

	spec := cicd.ResourceSpec{
		ID:           repoNodeID,
		Name:         repo.GetFullName(),
		ResourceType: "repository",
		Provider:     "github",
		Content:      content,
		Metadata: map[string]string{
			"org":            org,
			"repo_name":      repo.GetName(),
			"visibility":     repo.GetVisibility(),
			"default_branch": repo.GetDefaultBranch(),
		},
	}

	edges := []cicd.EdgeSpec{{
		SourceID:     repoNodeID,
		TargetID:     orgNodeID,
		Relationship: kgtypes.EdgeBelongsTo,
	}}

	return spec, edges
}

// repoMetadata is serialized as JSON into the repo node's Content field.
type repoMetadata struct {
	DefaultBranch string   `json:"default_branch"`
	Visibility    string   `json:"visibility"`
	Language      string   `json:"language,omitempty"`
	Topics        []string `json:"topics,omitempty"`
}

// --- ID helpers (shared across subcollectors) ---

func orgID(org string) string {
	return fmt.Sprintf("github:%s/Organization/%s", org, org)
}

func repoID(org, fullName string) string {
	return fmt.Sprintf("github:%s/Repository/%s", org, fullName)
}
