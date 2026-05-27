// SPDX-License-Identifier: Apache-2.0

package bitbucket

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// reposCollector discovers Bitbucket repositories in a workspace.
type reposCollector struct {
	client    *Client
	workspace string
}

func newReposCollector(client *Client, workspace string) *reposCollector {
	return &reposCollector{client: client, workspace: workspace}
}

// Collect lists all repositories in the workspace via paginated GET.
func (r *reposCollector) Collect(ctx context.Context) (cicd.SubCollectorResult, error) {
	path := fmt.Sprintf("repositories/%s", r.workspace)

	var repos []apiRepo
	err := r.client.GetPaginated(ctx, path, func(raw json.RawMessage) error {
		var page []apiRepo
		if err := json.Unmarshal(raw, &page); err != nil {
			return fmt.Errorf("unmarshal repos page: %w", err)
		}
		repos = append(repos, page...)
		return nil
	})
	if err != nil {
		return cicd.SubCollectorResult{}, fmt.Errorf("list repos: %w", err)
	}

	return r.buildResult(repos), nil
}

// buildResult converts API repos into ResourceSpecs and EdgeSpecs.
func (r *reposCollector) buildResult(repos []apiRepo) cicd.SubCollectorResult {
	if len(repos) == 0 {
		return cicd.SubCollectorResult{}
	}

	wsID := fmt.Sprintf("bitbucket:%s/Workspace/%s", r.workspace, r.workspace)

	// Workspace root node.
	resources := []cicd.ResourceSpec{{
		ID:           wsID,
		Name:         r.workspace,
		ResourceType: "workspace",
		Provider:     "bitbucket",
		Metadata:     map[string]string{"workspace": r.workspace},
	}}

	var edges []cicd.EdgeSpec
	for _, repo := range repos {
		repoID := fmt.Sprintf("bitbucket:%s/Repository/%s", r.workspace, repo.Slug)
		content, _ := json.Marshal(repo) //nolint:errchkjson // struct type cannot fail

		meta := map[string]string{
			"workspace": r.workspace,
			"slug":      repo.Slug,
		}
		if repo.IsPrivate {
			meta["is_private"] = "true"
		} else {
			meta["is_private"] = "false"
		}
		if repo.Mainbranch.Name != "" {
			meta["mainbranch"] = repo.Mainbranch.Name
		}
		if repo.Language != "" {
			meta["language"] = repo.Language
		}
		if repo.SCM != "" {
			meta["scm"] = repo.SCM
		}

		resources = append(resources, cicd.ResourceSpec{
			ID:           repoID,
			Name:         repo.FullName,
			ResourceType: "repository",
			Provider:     "bitbucket",
			Content:      content,
			Metadata:     meta,
		})

		edges = append(edges, cicd.EdgeSpec{
			SourceID:     repoID,
			TargetID:     wsID,
			Relationship: kgtypes.EdgeBelongsTo,
		})
	}

	return cicd.SubCollectorResult{Resources: resources, Edges: edges}
}

// apiRepo is the minimal Bitbucket API repository response shape.
type apiRepo struct {
	UUID       string `json:"uuid"`
	Slug       string `json:"slug"`
	FullName   string `json:"full_name"`
	IsPrivate  bool   `json:"is_private"`
	SCM        string `json:"scm"`
	Language   string `json:"language"`
	Mainbranch struct {
		Name string `json:"name"`
	} `json:"mainbranch"`
}
