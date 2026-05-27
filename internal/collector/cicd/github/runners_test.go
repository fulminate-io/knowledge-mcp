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

func TestRunnersCollector_Name(t *testing.T) {
	c := &runnersCollector{org: "myorg"}
	assert.Equal(t, "github-runners", c.Name())
}

func TestRunnersCollector_Collect(t *testing.T) {
	fakeRepos := &fakeReposAPI{
		repos: []*gogithub.Repository{
			{FullName: new("myorg/api"), Name: new("api")},
		},
	}
	fakeActions := &fakeActionsAPI{
		orgRunners: &gogithub.Runners{
			Runners: []*gogithub.Runner{
				{
					ID:     int64Ptr(1),
					Name:   new("org-runner-1"),
					OS:     new("linux"),
					Status: new("online"),
					Busy:   new(false),
					Labels: []*gogithub.RunnerLabels{
						{Name: new("self-hosted")},
						{Name: new("linux")},
					},
				},
			},
		},
		repoRunners: map[string]*gogithub.Runners{
			"myorg/api": {
				Runners: []*gogithub.Runner{
					{
						ID:     int64Ptr(2),
						Name:   new("repo-runner-1"),
						OS:     new("macos"),
						Status: new("offline"),
						Busy:   new(false),
						Labels: []*gogithub.RunnerLabels{
							{Name: new("self-hosted")},
						},
					},
				},
			},
		},
	}

	c := &runnersCollector{actions: fakeActions, repos: fakeRepos, org: "myorg"}
	result, err := c.Collect(context.Background())
	require.NoError(t, err)

	require.Len(t, result.Resources, 2)

	orgRunner := result.Resources[0]
	assert.Equal(t, "github:myorg/Runner/1", orgRunner.ID)
	assert.Equal(t, "runner", orgRunner.ResourceType)
	assert.Equal(t, "online", orgRunner.Metadata["status"])

	repoRunner := result.Resources[1]
	assert.Equal(t, "github:myorg/Runner/2", repoRunner.ID)
	assert.Equal(t, "offline", repoRunner.Metadata["status"])

	// Org runner: BELONGS_TO(org) + HAS_LABEL(self-hosted) + HAS_LABEL(linux)
	// Repo runner: BELONGS_TO(repo) + HAS_LABEL(self-hosted)
	var belongsTo, hasLabel int
	for _, e := range result.Edges {
		switch e.Relationship {
		case kgtypes.EdgeBelongsTo:
			belongsTo++
		case kgtypes.EdgeHasLabel:
			hasLabel++
		}
	}
	assert.Equal(t, 2, belongsTo, "2 BELONGS_TO edges")
	assert.Equal(t, 3, hasLabel, "3 HAS_LABEL edges total")
}
