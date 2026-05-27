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

func TestDeploymentsCollector_Name(t *testing.T) {
	c := &deploymentsCollector{org: "myorg"}
	assert.Equal(t, "github-deployments", c.Name())
}

func TestDeploymentsCollector_Collect(t *testing.T) {
	fakeRepos := &fakeReposAPI{
		repos: []*gogithub.Repository{
			{FullName: new("myorg/api"), Name: new("api")},
		},
		deployments: map[string][]*gogithub.Deployment{
			"myorg/api": {
				{
					ID:          int64Ptr(500),
					Environment: new("production"),
					Ref:         new("main"),
					Task:        new("deploy"),
					Description: new("deploy to prod"),
				},
			},
		},
	}

	c := &deploymentsCollector{repos: fakeRepos, org: "myorg", maxDeployments: 10}
	result, err := c.Collect(context.Background())
	require.NoError(t, err)

	require.Len(t, result.Resources, 1)
	d := result.Resources[0]
	assert.Equal(t, "github:myorg/Deployment/myorg/api/500", d.ID)
	assert.Equal(t, "deployment", d.ResourceType)
	assert.Equal(t, "production", d.Metadata["environment"])

	// BELONGS_TO(repo) + DEPLOYS_TO(env)
	require.Len(t, result.Edges, 2)
	assert.Equal(t, kgtypes.EdgeBelongsTo, result.Edges[0].Relationship)
	assert.Equal(t, kgtypes.EdgeDeploysTo, result.Edges[1].Relationship)
	assert.Equal(t, "github:myorg/Environment/myorg/api/production", result.Edges[1].TargetID)
}
