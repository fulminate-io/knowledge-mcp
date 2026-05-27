// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/msi/armmsi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// fakeFederatedPager is a test double for federatedCredentialPager.
type fakeFederatedPager struct {
	pages []armmsi.FederatedIdentityCredentialsClientListResponse
	idx   int
}

func (f *fakeFederatedPager) More() bool {
	return f.idx < len(f.pages)
}

func (f *fakeFederatedPager) NextPage(_ context.Context) (armmsi.FederatedIdentityCredentialsClientListResponse, error) {
	if f.idx >= len(f.pages) {
		return armmsi.FederatedIdentityCredentialsClientListResponse{}, nil
	}
	page := f.pages[f.idx]
	f.idx++
	return page, nil
}

func TestCollectFederatedCredentials(t *testing.T) {
	identityID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.ManagedIdentity/userAssignedIdentities/myid"

	t.Run("AKS workload identity", func(t *testing.T) {
		issuer := "https://eastus.oic.prod-aks.azure.com/tenant-id/cluster-id/"
		subject := "system:serviceaccount:default:my-sa"
		aud := "api://AzureADTokenExchange"
		pager := &fakeFederatedPager{
			pages: []armmsi.FederatedIdentityCredentialsClientListResponse{{
				FederatedIdentityCredentialsListResult: armmsi.FederatedIdentityCredentialsListResult{
					Value: []*armmsi.FederatedIdentityCredential{{
						Properties: &armmsi.FederatedIdentityCredentialProperties{
							Issuer:    &issuer,
							Subject:   &subject,
							Audiences: []*string{&aud},
						},
					}},
				},
			}},
		}
		var result cloud.SubCollectorResult
		err := collectFederatedCredentials(context.Background(), identityID, pager, &result)
		require.NoError(t, err)
		require.Len(t, result.Edges, 1)
		// No synthetic resource for AKS (SA node already exists).
		assert.Empty(t, result.Resources)

		e := result.Edges[0]
		assert.Equal(t, kgtypes.EdgeWorkloadIdentity, e.Relationship)
		// SA -> IAM direction.
		assert.Equal(t, "default/ServiceAccount/my-sa", e.SourceID)
		assert.Equal(t, identityID, e.TargetID)
		assert.Equal(t, issuer, e.Metadata["issuer"])
		assert.Equal(t, subject, e.Metadata["subject"])
		assert.Equal(t, aud, e.Metadata["audiences"])
	})

	t.Run("GitHub Actions OIDC", func(t *testing.T) {
		issuer := "https://token.actions.githubusercontent.com"
		subject := "repo:myorg/myrepo:ref:refs/heads/main"
		pager := &fakeFederatedPager{
			pages: []armmsi.FederatedIdentityCredentialsClientListResponse{{
				FederatedIdentityCredentialsListResult: armmsi.FederatedIdentityCredentialsListResult{
					Value: []*armmsi.FederatedIdentityCredential{{
						Properties: &armmsi.FederatedIdentityCredentialProperties{
							Issuer:  &issuer,
							Subject: &subject,
						},
					}},
				},
			}},
		}
		var result cloud.SubCollectorResult
		err := collectFederatedCredentials(context.Background(), identityID, pager, &result)
		require.NoError(t, err)
		require.Len(t, result.Edges, 1)
		// Synthetic GitHub resource created.
		require.Len(t, result.Resources, 1)
		assert.Equal(t, "github:myorg/myrepo", result.Resources[0].ID)
		assert.Equal(t, "github:identity", result.Resources[0].ResourceType)

		e := result.Edges[0]
		assert.Equal(t, kgtypes.EdgeWorkloadIdentity, e.Relationship)
		assert.Equal(t, "github:myorg/myrepo", e.SourceID)
		assert.Equal(t, identityID, e.TargetID)
	})

	t.Run("generic OIDC", func(t *testing.T) {
		issuer := "https://login.example.com"
		subject := "my-external-app"
		pager := &fakeFederatedPager{
			pages: []armmsi.FederatedIdentityCredentialsClientListResponse{{
				FederatedIdentityCredentialsListResult: armmsi.FederatedIdentityCredentialsListResult{
					Value: []*armmsi.FederatedIdentityCredential{{
						Properties: &armmsi.FederatedIdentityCredentialProperties{
							Issuer:  &issuer,
							Subject: &subject,
						},
					}},
				},
			}},
		}
		var result cloud.SubCollectorResult
		err := collectFederatedCredentials(context.Background(), identityID, pager, &result)
		require.NoError(t, err)
		require.Len(t, result.Edges, 1)
		// Synthetic OIDC resource created.
		require.Len(t, result.Resources, 1)
		expectedID := "oidc:https://login.example.com/my-external-app"
		assert.Equal(t, expectedID, result.Resources[0].ID)
		assert.Equal(t, "oidc:identity", result.Resources[0].ResourceType)

		e := result.Edges[0]
		assert.Equal(t, kgtypes.EdgeWorkloadIdentity, e.Relationship)
		assert.Equal(t, expectedID, e.SourceID)
		assert.Equal(t, identityID, e.TargetID)
	})

	t.Run("skips nil properties", func(t *testing.T) {
		pager := &fakeFederatedPager{
			pages: []armmsi.FederatedIdentityCredentialsClientListResponse{{
				FederatedIdentityCredentialsListResult: armmsi.FederatedIdentityCredentialsListResult{
					Value: []*armmsi.FederatedIdentityCredential{
						{Properties: nil},
						nil,
					},
				},
			}},
		}
		var result cloud.SubCollectorResult
		err := collectFederatedCredentials(context.Background(), identityID, pager, &result)
		require.NoError(t, err)
		assert.Empty(t, result.Edges)
	})

	t.Run("empty pager", func(t *testing.T) {
		pager := &fakeFederatedPager{}
		var result cloud.SubCollectorResult
		err := collectFederatedCredentials(context.Background(), identityID, pager, &result)
		require.NoError(t, err)
		assert.Empty(t, result.Edges)
	})
}

func TestExtractIdentityName(t *testing.T) {
	t.Run("valid ARM ID", func(t *testing.T) {
		id := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.ManagedIdentity/userAssignedIdentities/myid"
		assert.Equal(t, "myid", extractIdentityName(id))
	})
	t.Run("empty", func(t *testing.T) {
		assert.Empty(t, extractIdentityName(""))
	})
	t.Run("single segment", func(t *testing.T) {
		assert.Empty(t, extractIdentityName("x"))
	})
}

func TestParseK8sSASubject(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		ns, name, ok := parseK8sSASubject("system:serviceaccount:kube-system:coredns")
		assert.True(t, ok)
		assert.Equal(t, "kube-system", ns)
		assert.Equal(t, "coredns", name)
	})
	t.Run("invalid prefix", func(t *testing.T) {
		_, _, ok := parseK8sSASubject("system:node:mynode")
		assert.False(t, ok)
	})
	t.Run("missing name", func(t *testing.T) {
		_, _, ok := parseK8sSASubject("system:serviceaccount:ns:")
		assert.False(t, ok)
	})
}

func TestParseGitHubSubject(t *testing.T) {
	t.Run("with ref", func(t *testing.T) {
		org, repo, ok := parseGitHubSubject("repo:myorg/myrepo:ref:refs/heads/main")
		assert.True(t, ok)
		assert.Equal(t, "myorg", org)
		assert.Equal(t, "myrepo", repo)
	})
	t.Run("environment", func(t *testing.T) {
		org, repo, ok := parseGitHubSubject("repo:myorg/myrepo:environment:production")
		assert.True(t, ok)
		assert.Equal(t, "myorg", org)
		assert.Equal(t, "myrepo", repo)
	})
	t.Run("not a repo subject", func(t *testing.T) {
		_, _, ok := parseGitHubSubject("user:someone")
		assert.False(t, ok)
	})
}
