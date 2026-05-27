// SPDX-License-Identifier: Apache-2.0

package github

import (
	"context"
	"encoding/base64"
	"fmt"
	"testing"

	gogithub "github.com/google/go-github/v68/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// fakeActionsAPI implements actionsAPI for testing.
type fakeActionsAPI struct {
	workflows   map[string]*gogithub.Workflows // keyed by "owner/repo"
	runs        map[string]*gogithub.WorkflowRuns
	orgRunners  *gogithub.Runners
	repoRunners map[string]*gogithub.Runners
	orgSecrets  *gogithub.Secrets
	repoSecrets map[string]*gogithub.Secrets
	envSecrets  map[string]*gogithub.Secrets // keyed by "repoID/env"
	err         error
}

func (f *fakeActionsAPI) ListWorkflows(_ context.Context, owner, repo string, _ *gogithub.ListOptions) (*gogithub.Workflows, *gogithub.Response, error) {
	if f.err != nil {
		return nil, nil, f.err
	}
	key := owner + "/" + repo
	wfs := f.workflows[key]
	if wfs == nil {
		wfs = &gogithub.Workflows{}
	}
	return wfs, &gogithub.Response{}, nil
}

func (f *fakeActionsAPI) ListRepositoryWorkflowRuns(_ context.Context, owner, repo string, _ *gogithub.ListWorkflowRunsOptions) (*gogithub.WorkflowRuns, *gogithub.Response, error) {
	key := owner + "/" + repo
	runs := f.runs[key]
	if runs == nil {
		runs = &gogithub.WorkflowRuns{}
	}
	return runs, &gogithub.Response{}, nil
}

func (f *fakeActionsAPI) ListOrganizationRunners(context.Context, string, *gogithub.ListRunnersOptions) (*gogithub.Runners, *gogithub.Response, error) {
	r := f.orgRunners
	if r == nil {
		r = &gogithub.Runners{}
	}
	return r, &gogithub.Response{}, nil
}

func (f *fakeActionsAPI) ListRunners(_ context.Context, owner, repo string, _ *gogithub.ListRunnersOptions) (*gogithub.Runners, *gogithub.Response, error) {
	key := owner + "/" + repo
	r := f.repoRunners[key]
	if r == nil {
		r = &gogithub.Runners{}
	}
	return r, &gogithub.Response{}, nil
}

func (f *fakeActionsAPI) ListOrgSecrets(context.Context, string, *gogithub.ListOptions) (*gogithub.Secrets, *gogithub.Response, error) {
	s := f.orgSecrets
	if s == nil {
		s = &gogithub.Secrets{}
	}
	return s, &gogithub.Response{}, nil
}

func (f *fakeActionsAPI) ListRepoSecrets(_ context.Context, owner, repo string, _ *gogithub.ListOptions) (*gogithub.Secrets, *gogithub.Response, error) {
	key := owner + "/" + repo
	s := f.repoSecrets[key]
	if s == nil {
		s = &gogithub.Secrets{}
	}
	return s, &gogithub.Response{}, nil
}

func (f *fakeActionsAPI) ListEnvSecrets(_ context.Context, repoID int, env string, _ *gogithub.ListOptions) (*gogithub.Secrets, *gogithub.Response, error) {
	key := fmt.Sprintf("%d/%s", repoID, env)
	s := f.envSecrets[key]
	if s == nil {
		s = &gogithub.Secrets{}
	}
	return s, &gogithub.Response{}, nil
}

func TestWorkflowsCollector_Name(t *testing.T) {
	c := &workflowsCollector{org: "myorg"}
	assert.Equal(t, "github-workflows", c.Name())
}

func TestWorkflowsCollector_Collect(t *testing.T) {
	yamlContent := `name: CI
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    environment: production
    steps:
      - uses: actions/checkout@v4
      - run: echo ${{ secrets.API_KEY }}
      - run: echo ${{ secrets.DB_PASS }}
`
	encoded := base64.StdEncoding.EncodeToString([]byte(yamlContent))
	encoding := "base64"

	fakeRepos := &fakeReposAPI{
		repos: []*gogithub.Repository{
			{FullName: new("myorg/api"), Name: new("api")},
		},
		getContentsResult: &gogithub.RepositoryContent{
			Content:  &encoded,
			Encoding: &encoding,
		},
	}

	fakeActions := &fakeActionsAPI{
		workflows: map[string]*gogithub.Workflows{
			"myorg/api": {
				Workflows: []*gogithub.Workflow{
					{
						Name:    new("CI"),
						Path:    new(".github/workflows/ci.yml"),
						State:   new("active"),
						HTMLURL: new("https://github.com/myorg/api/actions/workflows/ci.yml"),
					},
					{
						Name:  new("Archived"),
						Path:  new(".github/workflows/old.yml"),
						State: new("disabled_manually"),
					},
				},
			},
		},
	}

	c := &workflowsCollector{actions: fakeActions, repos: fakeRepos, org: "myorg"}
	result, err := c.Collect(context.Background())
	require.NoError(t, err)

	// 1 active workflow (disabled skipped)
	require.Len(t, result.Resources, 1)
	wf := result.Resources[0]
	assert.Equal(t, "github:myorg/Workflow/myorg/api/.github/workflows/ci.yml", wf.ID)
	assert.Equal(t, "workflow", wf.ResourceType)

	// Edges: BELONGS_TO + 2 USES_SECRET + 1 DEPLOYS_TO
	var belongsTo, usesSecret, deploysTo int
	for _, e := range result.Edges {
		switch e.Relationship {
		case kgtypes.EdgeBelongsTo:
			belongsTo++
		case kgtypes.EdgeUsesSecret:
			usesSecret++
		case kgtypes.EdgeDeploysTo:
			deploysTo++
		}
	}
	assert.Equal(t, 1, belongsTo, "BELONGS_TO edge")
	assert.Equal(t, 2, usesSecret, "USES_SECRET edges for API_KEY and DB_PASS")
	assert.Equal(t, 1, deploysTo, "DEPLOYS_TO edge for production")
}

func TestParseSecretRefs(t *testing.T) {
	yaml := `
steps:
  - run: echo ${{ secrets.API_KEY }}
  - run: echo ${{ secrets.DB_PASS }}
  - run: echo ${{ secrets.API_KEY }}
`
	secrets := parseSecretRefs(yaml)
	assert.Equal(t, []string{"API_KEY", "DB_PASS"}, secrets)
}

func TestParseEnvironmentRefs(t *testing.T) {
	yaml := `
jobs:
  deploy:
    environment: production
  staging:
    environment: "staging"
`
	envs := parseEnvironmentRefs(yaml)
	assert.Equal(t, []string{"production", "staging"}, envs)
}
