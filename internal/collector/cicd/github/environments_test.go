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

func TestEnvironmentsCollector_Name(t *testing.T) {
	c := &environmentsCollector{org: "myorg"}
	assert.Equal(t, "github-environments", c.Name())
}

func TestEnvironmentsCollector_Collect(t *testing.T) {
	ruleType := "required_reviewers"
	fakeRepos := &fakeReposAPI{
		repos: []*gogithub.Repository{
			{FullName: new("myorg/api"), Name: new("api")},
		},
		environments: map[string]*gogithub.EnvResponse{
			"myorg/api": {
				Environments: []*gogithub.Environment{
					{
						Name: new("production"),
						ProtectionRules: []*gogithub.ProtectionRule{
							{
								Type:      &ruleType,
								Reviewers: []*gogithub.RequiredReviewer{},
							},
						},
					},
					{
						Name: new("staging"),
					},
				},
			},
		},
	}

	c := &environmentsCollector{repos: fakeRepos, org: "myorg"}
	result, err := c.Collect(context.Background())
	require.NoError(t, err)

	require.Len(t, result.Resources, 2)
	assert.Equal(t, "github:myorg/Environment/myorg/api/production", result.Resources[0].ID)
	assert.Equal(t, "environment", result.Resources[0].ResourceType)
	assert.Equal(t, "github:myorg/Environment/myorg/api/staging", result.Resources[1].ID)

	// BELONGS_TO edges
	var belongsTo int
	for _, e := range result.Edges {
		if e.Relationship == kgtypes.EdgeBelongsTo {
			belongsTo++
		}
	}
	assert.Equal(t, 2, belongsTo, "each environment has BELONGS_TO repo")
}
