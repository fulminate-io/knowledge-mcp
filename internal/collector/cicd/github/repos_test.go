// SPDX-License-Identifier: Apache-2.0

package github

import (
	"context"
	"testing"

	gogithub "github.com/google/go-github/v68/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// fakeReposAPI implements reposAPI for testing.
type fakeReposAPI struct {
	repos             []*gogithub.Repository
	getContentsResult *gogithub.RepositoryContent
	environments      map[string]*gogithub.EnvResponse // keyed by "owner/repo"
	deployments       map[string][]*gogithub.Deployment
	err               error
}

func (f *fakeReposAPI) ListByOrg(_ context.Context, _ string, _ *gogithub.RepositoryListByOrgOptions) ([]*gogithub.Repository, *gogithub.Response, error) {
	if f.err != nil {
		return nil, nil, f.err
	}
	return f.repos, &gogithub.Response{}, nil
}

func (f *fakeReposAPI) GetContents(_ context.Context, _, _, _ string, _ *gogithub.RepositoryContentGetOptions) (*gogithub.RepositoryContent, []*gogithub.RepositoryContent, *gogithub.Response, error) {
	return f.getContentsResult, nil, &gogithub.Response{}, nil
}

func (f *fakeReposAPI) ListEnvironments(_ context.Context, owner, repo string, _ *gogithub.EnvironmentListOptions) (*gogithub.EnvResponse, *gogithub.Response, error) {
	key := owner + "/" + repo
	if e, ok := f.environments[key]; ok {
		return e, &gogithub.Response{}, nil
	}
	return &gogithub.EnvResponse{}, &gogithub.Response{}, nil
}

func (f *fakeReposAPI) ListDeployments(_ context.Context, owner, repo string, _ *gogithub.DeploymentsListOptions) ([]*gogithub.Deployment, *gogithub.Response, error) {
	key := owner + "/" + repo
	return f.deployments[key], &gogithub.Response{}, nil
}

func TestReposCollector_Name(t *testing.T) {
	c := &reposCollector{org: "myorg"}
	assert.Equal(t, "github-repos", c.Name())
}

func TestReposCollector_Collect(t *testing.T) {
	archived := true
	fake := &fakeReposAPI{
		repos: []*gogithub.Repository{
			{
				FullName:      new("myorg/api"),
				Name:          new("api"),
				Visibility:    new("private"),
				DefaultBranch: new("main"),
				Language:      new("Go"),
				Topics:        []string{"backend"},
			},
			{
				FullName:      new("myorg/old-stuff"),
				Name:          new("old-stuff"),
				Archived:      &archived,
				Visibility:    new("private"),
				DefaultBranch: new("master"),
			},
		},
	}

	c := &reposCollector{client: fake, org: "myorg"}
	result, err := c.Collect(context.Background())
	require.NoError(t, err)

	// Org node + 1 repo (archived skipped)
	require.Len(t, result.Resources, 2)

	org := result.Resources[0]
	assert.Equal(t, "github:myorg/Organization/myorg", org.ID)
	assert.Equal(t, "organization", org.ResourceType)

	repo := result.Resources[1]
	assert.Equal(t, "github:myorg/Repository/myorg/api", repo.ID)
	assert.Equal(t, "repository", repo.ResourceType)
	assert.Equal(t, "private", repo.Metadata["visibility"])

	// BELONGS_TO edge from repo → org
	require.Len(t, result.Edges, 1)
	assert.Equal(t, kgtypes.EdgeBelongsTo, result.Edges[0].Relationship)
	assert.Equal(t, repo.ID, result.Edges[0].SourceID)
	assert.Equal(t, org.ID, result.Edges[0].TargetID)
}

func TestReposCollector_APIError(t *testing.T) {
	fake := &fakeReposAPI{err: assert.AnError}
	c := &reposCollector{client: fake, org: "myorg"}
	_, err := c.Collect(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "github-repos: list")
}
