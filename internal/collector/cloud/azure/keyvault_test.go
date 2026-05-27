// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/authorization/armauthorization/v3"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/keyvault/armkeyvault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestKVAccessPolicyEdges(t *testing.T) {
	vaultID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.KeyVault/vaults/myvault"
	objID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	tenantID := "11111111-2222-3333-4444-555555555555"

	t.Run("emits AccessedBy for legacy access policy", func(t *testing.T) {
		kGet := armkeyvault.KeyPermissionsGet
		vault := &armkeyvault.Vault{
			ID: &vaultID,
			Properties: &armkeyvault.VaultProperties{
				AccessPolicies: []*armkeyvault.AccessPolicyEntry{{
					ObjectID: &objID,
					TenantID: &tenantID,
					Permissions: &armkeyvault.Permissions{
						Keys: []*armkeyvault.KeyPermissions{&kGet},
					},
				}},
			},
		}
		edges := kvEdges(vault)

		var found bool
		for _, e := range edges {
			if e.Relationship == kgtypes.EdgeAccessedBy {
				assert.Equal(t, vaultID, e.SourceID)
				assert.Equal(t, objID, e.TargetID)
				assert.Equal(t, "access_policy", e.Metadata["source"])
				assert.Equal(t, tenantID, e.Metadata["tenant_id"])
				assert.Contains(t, e.Metadata["permissions"], "get")
				found = true
			}
		}
		assert.True(t, found, "expected EdgeAccessedBy edge")
	})

	t.Run("no edge when ObjectID nil", func(t *testing.T) {
		vault := &armkeyvault.Vault{
			ID: &vaultID,
			Properties: &armkeyvault.VaultProperties{
				AccessPolicies: []*armkeyvault.AccessPolicyEntry{{
					TenantID: &tenantID,
				}},
			},
		}
		edges := kvAccessPolicyEdges(vault)
		assert.Empty(t, edges)
	})

	t.Run("no edge when properties nil", func(t *testing.T) {
		vault := &armkeyvault.Vault{ID: &vaultID}
		edges := kvEdges(vault)
		assert.Empty(t, edges)
	})
}

// fakeRBACPager is a test double for roleAssignmentPager.
type fakeRBACPager struct {
	pages []armauthorization.RoleAssignmentsClientListForScopeResponse
	idx   int
}

func (f *fakeRBACPager) More() bool {
	return f.idx < len(f.pages)
}

func (f *fakeRBACPager) NextPage(_ context.Context) (armauthorization.RoleAssignmentsClientListForScopeResponse, error) {
	if f.idx >= len(f.pages) {
		return armauthorization.RoleAssignmentsClientListForScopeResponse{}, nil
	}
	page := f.pages[f.idx]
	f.idx++
	return page, nil
}

func TestKVCollectRBAC(t *testing.T) {
	vaultID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.KeyVault/vaults/myvault"
	principalA := "principal-aaa"
	principalB := "principal-bbb"
	roleDef := "/subscriptions/sub/providers/Microsoft.Authorization/roleDefinitions/role1"
	scope := vaultID
	ptUser := armauthorization.PrincipalTypeUser
	ptSP := armauthorization.PrincipalTypeServicePrincipal

	t.Run("single assignment", func(t *testing.T) {
		pager := &fakeRBACPager{
			pages: []armauthorization.RoleAssignmentsClientListForScopeResponse{{
				RoleAssignmentListResult: armauthorization.RoleAssignmentListResult{
					Value: []*armauthorization.RoleAssignment{{
						Properties: &armauthorization.RoleAssignmentProperties{
							PrincipalID:      &principalA,
							PrincipalType:    &ptUser,
							RoleDefinitionID: &roleDef,
							Scope:            &scope,
						},
					}},
				},
			}},
		}
		var result cloud.SubCollectorResult
		err := kvCollectRBAC(context.Background(), vaultID, pager, &result)
		require.NoError(t, err)
		require.Len(t, result.Edges, 1)

		e := result.Edges[0]
		assert.Equal(t, kgtypes.EdgeAccessedBy, e.Relationship)
		assert.Equal(t, vaultID, e.SourceID)
		assert.Equal(t, principalA, e.TargetID)
		assert.Equal(t, "rbac", e.Metadata["source"])
		assert.Equal(t, roleDef, e.Metadata["role_definition_id"])
		assert.Equal(t, "User", e.Metadata["principal_type"])
		assert.Equal(t, scope, e.Metadata["scope"])
	})

	t.Run("multiple principal types", func(t *testing.T) {
		pager := &fakeRBACPager{
			pages: []armauthorization.RoleAssignmentsClientListForScopeResponse{{
				RoleAssignmentListResult: armauthorization.RoleAssignmentListResult{
					Value: []*armauthorization.RoleAssignment{
						{Properties: &armauthorization.RoleAssignmentProperties{
							PrincipalID:   &principalA,
							PrincipalType: &ptUser,
						}},
						{Properties: &armauthorization.RoleAssignmentProperties{
							PrincipalID:   &principalB,
							PrincipalType: &ptSP,
						}},
					},
				},
			}},
		}
		var result cloud.SubCollectorResult
		err := kvCollectRBAC(context.Background(), vaultID, pager, &result)
		require.NoError(t, err)
		require.Len(t, result.Edges, 2)
		assert.Equal(t, principalA, result.Edges[0].TargetID)
		assert.Equal(t, "User", result.Edges[0].Metadata["principal_type"])
		assert.Equal(t, principalB, result.Edges[1].TargetID)
		assert.Equal(t, "ServicePrincipal", result.Edges[1].Metadata["principal_type"])
	})

	t.Run("skips nil principal", func(t *testing.T) {
		pager := &fakeRBACPager{
			pages: []armauthorization.RoleAssignmentsClientListForScopeResponse{{
				RoleAssignmentListResult: armauthorization.RoleAssignmentListResult{
					Value: []*armauthorization.RoleAssignment{
						{Properties: &armauthorization.RoleAssignmentProperties{}},
						{Properties: nil},
						nil,
					},
				},
			}},
		}
		var result cloud.SubCollectorResult
		err := kvCollectRBAC(context.Background(), vaultID, pager, &result)
		require.NoError(t, err)
		assert.Empty(t, result.Edges)
	})

	t.Run("empty pager", func(t *testing.T) {
		pager := &fakeRBACPager{}
		var result cloud.SubCollectorResult
		err := kvCollectRBAC(context.Background(), vaultID, pager, &result)
		require.NoError(t, err)
		assert.Empty(t, result.Edges)
	})
}

func TestKVIsRBACEnabled(t *testing.T) {
	vaultID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.KeyVault/vaults/v"
	tr := true
	fa := false

	t.Run("true when enabled", func(t *testing.T) {
		v := &armkeyvault.Vault{ID: &vaultID, Properties: &armkeyvault.VaultProperties{
			EnableRbacAuthorization: &tr,
		}}
		assert.True(t, kvIsRBACEnabled(v))
	})

	t.Run("false when disabled", func(t *testing.T) {
		v := &armkeyvault.Vault{ID: &vaultID, Properties: &armkeyvault.VaultProperties{
			EnableRbacAuthorization: &fa,
		}}
		assert.False(t, kvIsRBACEnabled(v))
	})

	t.Run("false when nil", func(t *testing.T) {
		v := &armkeyvault.Vault{ID: &vaultID, Properties: &armkeyvault.VaultProperties{}}
		assert.False(t, kvIsRBACEnabled(v))
	})

	t.Run("false when properties nil", func(t *testing.T) {
		v := &armkeyvault.Vault{ID: &vaultID}
		assert.False(t, kvIsRBACEnabled(v))
	})
}
