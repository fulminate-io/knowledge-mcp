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

func TestWorkflowRunsCollector_Name(t *testing.T) {
	c := &workflowRunsCollector{org: "myorg"}
	assert.Equal(t, "github-workflow-runs", c.Name())
}

func TestWorkflowRunsCollector_Collect(t *testing.T) {
	fakeRepos := &fakeReposAPI{
		repos: []*gogithub.Repository{
			{FullName: new("myorg/api"), Name: new("api")},
		},
	}
	fakeActions := &fakeActionsAPI{
		runs: map[string]*gogithub.WorkflowRuns{
			"myorg/api": {
				WorkflowRuns: []*gogithub.WorkflowRun{
					{
						ID:         int64Ptr(100),
						Name:       new("CI"),
						RunNumber:  new(42),
						Status:     new("completed"),
						Conclusion: new("success"),
						HeadBranch: new("main"),
						Event:      new("push"),
						Path:       new(".github/workflows/ci.yml"),
					},
				},
			},
		},
	}

	c := &workflowRunsCollector{
		actions: fakeActions, repos: fakeRepos, org: "myorg", maxRuns: 10,
	}
	result, err := c.Collect(context.Background())
	require.NoError(t, err)

	require.Len(t, result.Resources, 1)
	run := result.Resources[0]
	assert.Equal(t, "github:myorg/WorkflowRun/myorg/api/100", run.ID)
	assert.Equal(t, "workflow_run", run.ResourceType)
	assert.Equal(t, "completed", run.Metadata["status"])

	// BELONGS_TO (workflow) + TRIGGERED_BY (repo)
	require.Len(t, result.Edges, 2)
	assert.Equal(t, kgtypes.EdgeBelongsTo, result.Edges[0].Relationship)
	assert.Equal(t, kgtypes.EdgeTriggeredBy, result.Edges[1].Relationship)
}

func int64Ptr(v int64) *int64 { return new(v) }
