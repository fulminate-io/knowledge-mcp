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

func TestSecretsCollector_Name(t *testing.T) {
	c := &secretsCollector{org: "myorg"}
	assert.Equal(t, "github-secrets", c.Name())
}

func TestSecretsCollector_Collect(t *testing.T) {
	envID := int64(10)
	fakeRepos := &fakeReposAPI{
		repos: []*gogithub.Repository{
			{FullName: new("myorg/api"), Name: new("api")},
		},
		environments: map[string]*gogithub.EnvResponse{
			"myorg/api": {
				Environments: []*gogithub.Environment{
					{Name: new("production"), ID: &envID},
				},
			},
		},
	}
	fakeActions := &fakeActionsAPI{
		orgSecrets: &gogithub.Secrets{
			Secrets: []*gogithub.Secret{
				{Name: "ORG_TOKEN", Visibility: "all"},
			},
		},
		repoSecrets: map[string]*gogithub.Secrets{
			"myorg/api": {
				Secrets: []*gogithub.Secret{
					{Name: "DEPLOY_KEY"},
				},
			},
		},
		envSecrets: map[string]*gogithub.Secrets{
			"10/production": {
				Secrets: []*gogithub.Secret{
					{Name: "PROD_DB_PASS"},
				},
			},
		},
	}

	c := &secretsCollector{actions: fakeActions, repos: fakeRepos, org: "myorg"}
	result, err := c.Collect(context.Background())
	require.NoError(t, err)

	// 3 secrets: org + repo + env
	require.Len(t, result.Resources, 3)

	orgSecret := result.Resources[0]
	assert.Equal(t, "github:myorg/Secret/org/ORG_TOKEN", orgSecret.ID)
	assert.Equal(t, "secret", orgSecret.ResourceType)

	repoSecret := result.Resources[1]
	assert.Equal(t, "github:myorg/Secret/myorg/api/repo/DEPLOY_KEY", repoSecret.ID)

	envSecret := result.Resources[2]
	assert.Equal(t, "github:myorg/Secret/myorg/api/env/production/PROD_DB_PASS", envSecret.ID)

	// Each secret has a BELONGS_TO edge
	var belongsTo int
	for _, e := range result.Edges {
		if e.Relationship == kgtypes.EdgeBelongsTo {
			belongsTo++
		}
	}
	assert.Equal(t, 3, belongsTo)
}

func TestSecretsCollector_NoValues(t *testing.T) {
	// Verify the Secret struct only has Name, not Value fields
	s := &gogithub.Secret{Name: "MY_SECRET"}
	spec, _ := buildSecretResource("myorg", "myorg/api", "repo", s, "parent-id")
	assert.NotContains(t, string(spec.Content), "value")
	assert.NotContains(t, string(spec.Content), "Value")
}
