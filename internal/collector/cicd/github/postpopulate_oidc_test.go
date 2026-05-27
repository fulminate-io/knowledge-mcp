// SPDX-License-Identifier: Apache-2.0

package github

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestExtractGitHubOIDCSubjects(t *testing.T) {
	cond := map[string]map[string]stringOrSlice{
		"StringLike": {
			"token.actions.githubusercontent.com:sub": {"repo:myorg/api:ref:refs/heads/main"},
			"token.actions.githubusercontent.com:aud": {"sts.amazonaws.com"},
		},
	}
	subjects := extractGitHubOIDCSubjects(cond)
	require.Len(t, subjects, 1)
	assert.Equal(t, "repo:myorg/api:ref:refs/heads/main", subjects[0])
}

func TestExtractGitHubOIDCSubjects_MultipleSubjects(t *testing.T) {
	cond := map[string]map[string]stringOrSlice{
		"StringLike": {
			"token.actions.githubusercontent.com:sub": {
				"repo:myorg/api:*",
				"repo:myorg/web:ref:refs/heads/main",
			},
		},
	}
	subjects := extractGitHubOIDCSubjects(cond)
	require.Len(t, subjects, 2)
}

func TestExtractGitHubOIDCSubjects_EmptyCond(t *testing.T) {
	subjects := extractGitHubOIDCSubjects(nil)
	assert.Nil(t, subjects)
}

func TestGitHubSubjectToNodeID(t *testing.T) {
	tests := []struct {
		subject string
		want    string
	}{
		{
			subject: "repo:myorg/api:ref:refs/heads/main",
			want:    "github:myorg/Repository/myorg/api",
		},
		{
			subject: "repo:myorg/api:*",
			want:    "github:myorg/Repository/myorg/api",
		},
		{
			subject: "repo:myorg/api:environment:production",
			want:    "github:myorg/Repository/myorg/api",
		},
	}
	for _, tt := range tests {
		t.Run(tt.subject, func(t *testing.T) {
			assert.Equal(t, tt.want, githubSubjectToNodeID(tt.subject))
		})
	}
}

func TestBuildFederationEdge(t *testing.T) {
	edge := buildFederationEdge(
		"repo:myorg/api:ref:refs/heads/main",
		"arn:aws:iam::123456789012:role/deploy-role",
		"arn:aws:iam::123456789012:oidc-provider/token.actions.githubusercontent.com",
	)
	assert.Equal(t, "github:myorg/Repository/myorg/api", edge.FromId)
	assert.Equal(t, "arn:aws:iam::123456789012:role/deploy-role", edge.ToId)
	assert.Equal(t, kgtypes.EdgeFederates, kgtypes.EdgeType(edge.Type))
	assert.Equal(t, methodOIDC, edge.Method)
	assert.Contains(t, edge.Evidence, "GitHub OIDC")
}

func TestMatchGitHubFederatedPrincipals(t *testing.T) {
	trustPolicyJSON := `{
		"AssumeRolePolicyDocument": "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Principal\":{\"Federated\":\"arn:aws:iam::123456789012:oidc-provider/token.actions.githubusercontent.com\"},\"Action\":\"sts:AssumeRoleWithWebIdentity\",\"Condition\":{\"StringLike\":{\"token.actions.githubusercontent.com:sub\":\"repo:myorg/api:*\"}}}]}"
	}`
	role := knowledgev1.Node{
		Id:      "arn:aws:iam::123456789012:role/deploy-role",
		Content: trustPolicyJSON,
	}

	edges := matchGitHubFederatedPrincipals(&role)
	require.Len(t, edges, 1)
	assert.Equal(t, kgtypes.EdgeFederates, kgtypes.EdgeType(edges[0].Type))
	assert.Equal(t, "github:myorg/Repository/myorg/api", edges[0].FromId)
}

func TestMatchGitHubFederatedPrincipals_NoMatch(t *testing.T) {
	// A role with a non-GitHub OIDC provider
	trustPolicyJSON := `{
		"AssumeRolePolicyDocument": "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Principal\":{\"Federated\":\"arn:aws:iam::123:oidc-provider/other.provider.com\"},\"Action\":\"sts:AssumeRoleWithWebIdentity\"}]}"
	}`
	role := knowledgev1.Node{
		Id:      "arn:aws:iam::123:role/other-role",
		Content: trustPolicyJSON,
	}

	edges := matchGitHubFederatedPrincipals(&role)
	assert.Empty(t, edges)
}

func TestExtractTrustPolicy_Nil(t *testing.T) {
	assert.Nil(t, extractTrustPolicy(&knowledgev1.Node{}))
}
