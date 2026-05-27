// SPDX-License-Identifier: Apache-2.0

package github

import (
	"context"

	gogithub "github.com/google/go-github/v68/github"
	"golang.org/x/oauth2"
)

// newGitHubClient creates an authenticated GitHub API client from a personal
// access token. The token is set via GITHUB_TOKEN or GH_TOKEN env var.
func newGitHubClient(token string) *gogithub.Client {
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	tc := oauth2.NewClient(context.Background(), ts)
	return gogithub.NewClient(tc)
}

// --- API interfaces for testing (one per GitHub service) ---

// reposAPI abstracts the subset of github.RepositoriesService we use.
type reposAPI interface {
	ListByOrg(ctx context.Context, org string, opts *gogithub.RepositoryListByOrgOptions) ([]*gogithub.Repository, *gogithub.Response, error)
	GetContents(ctx context.Context, owner, repo, path string, opts *gogithub.RepositoryContentGetOptions) (*gogithub.RepositoryContent, []*gogithub.RepositoryContent, *gogithub.Response, error)
	ListEnvironments(ctx context.Context, owner, repo string, opts *gogithub.EnvironmentListOptions) (*gogithub.EnvResponse, *gogithub.Response, error)
	ListDeployments(ctx context.Context, owner, repo string, opts *gogithub.DeploymentsListOptions) ([]*gogithub.Deployment, *gogithub.Response, error)
}

// actionsAPI abstracts the subset of github.ActionsService we use.
type actionsAPI interface {
	ListWorkflows(ctx context.Context, owner, repo string, opts *gogithub.ListOptions) (*gogithub.Workflows, *gogithub.Response, error)
	ListRepositoryWorkflowRuns(ctx context.Context, owner, repo string, opts *gogithub.ListWorkflowRunsOptions) (*gogithub.WorkflowRuns, *gogithub.Response, error)
	ListOrganizationRunners(ctx context.Context, org string, opts *gogithub.ListRunnersOptions) (*gogithub.Runners, *gogithub.Response, error)
	ListRunners(ctx context.Context, owner, repo string, opts *gogithub.ListRunnersOptions) (*gogithub.Runners, *gogithub.Response, error)
	ListOrgSecrets(ctx context.Context, org string, opts *gogithub.ListOptions) (*gogithub.Secrets, *gogithub.Response, error)
	ListRepoSecrets(ctx context.Context, owner, repo string, opts *gogithub.ListOptions) (*gogithub.Secrets, *gogithub.Response, error)
	ListEnvSecrets(ctx context.Context, repoID int, env string, opts *gogithub.ListOptions) (*gogithub.Secrets, *gogithub.Response, error)
}

// apiClients bundles the authenticated API interfaces used by subcollectors.
type apiClients struct {
	repos   reposAPI
	actions actionsAPI
}

// newAPIClients creates apiClients from an authenticated go-github client.
func newAPIClients(client *gogithub.Client) apiClients {
	return apiClients{
		repos:   client.Repositories,
		actions: client.Actions,
	}
}
