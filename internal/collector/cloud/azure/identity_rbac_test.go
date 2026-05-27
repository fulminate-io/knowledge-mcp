// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/authorization/armauthorization/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// fakeIdentityRBACPager is a test double for identityRBACPager.
type fakeIdentityRBACPager struct {
	pages []armauthorization.RoleAssignmentsClientListForSubscriptionResponse
	idx   int
}

func (f *fakeIdentityRBACPager) More() bool {
	return f.idx < len(f.pages)
}

func (f *fakeIdentityRBACPager) NextPage(_ context.Context) (armauthorization.RoleAssignmentsClientListForSubscriptionResponse, error) {
	if f.idx >= len(f.pages) {
		return armauthorization.RoleAssignmentsClientListForSubscriptionResponse{}, nil
	}
	page := f.pages[f.idx]
	f.idx++
	return page, nil
}

func TestCollectIdentityRBAC(t *testing.T) {
	identityID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.ManagedIdentity/userAssignedIdentities/myid"
	scopeA := "/subscriptions/sub/resourceGroups/rg"
	scopeB := "/subscriptions/sub"
	roleDef := "/subscriptions/sub/providers/Microsoft.Authorization/roleDefinitions/role1"
	ptSP := armauthorization.PrincipalTypeServicePrincipal

	t.Run("single assignment", func(t *testing.T) {
		pager := &fakeIdentityRBACPager{
			pages: []armauthorization.RoleAssignmentsClientListForSubscriptionResponse{{
				RoleAssignmentListResult: armauthorization.RoleAssignmentListResult{
					Value: []*armauthorization.RoleAssignment{{
						Properties: &armauthorization.RoleAssignmentProperties{
							Scope:            &scopeA,
							RoleDefinitionID: &roleDef,
							PrincipalType:    &ptSP,
						},
					}},
				},
			}},
		}
		var result cloud.SubCollectorResult
		err := collectIdentityRBAC(context.Background(), identityID, pager, &result)
		require.NoError(t, err)
		require.Len(t, result.Edges, 1)

		e := result.Edges[0]
		assert.Equal(t, kgtypes.EdgeAssumesRole, e.Relationship)
		assert.Equal(t, identityID, e.SourceID)
		assert.Equal(t, scopeA, e.TargetID)
		assert.Equal(t, "rbac", e.Metadata["source"])
		assert.Equal(t, roleDef, e.Metadata["role_definition_id"])
		assert.Equal(t, "ServicePrincipal", e.Metadata["principal_type"])
	})

	t.Run("multiple assignments across pages", func(t *testing.T) {
		pager := &fakeIdentityRBACPager{
			pages: []armauthorization.RoleAssignmentsClientListForSubscriptionResponse{
				{RoleAssignmentListResult: armauthorization.RoleAssignmentListResult{
					Value: []*armauthorization.RoleAssignment{{
						Properties: &armauthorization.RoleAssignmentProperties{Scope: &scopeA},
					}},
				}},
				{RoleAssignmentListResult: armauthorization.RoleAssignmentListResult{
					Value: []*armauthorization.RoleAssignment{{
						Properties: &armauthorization.RoleAssignmentProperties{Scope: &scopeB},
					}},
				}},
			},
		}
		var result cloud.SubCollectorResult
		err := collectIdentityRBAC(context.Background(), identityID, pager, &result)
		require.NoError(t, err)
		require.Len(t, result.Edges, 2)
		assert.Equal(t, scopeA, result.Edges[0].TargetID)
		assert.Equal(t, scopeB, result.Edges[1].TargetID)
	})

	t.Run("skips nil properties", func(t *testing.T) {
		pager := &fakeIdentityRBACPager{
			pages: []armauthorization.RoleAssignmentsClientListForSubscriptionResponse{{
				RoleAssignmentListResult: armauthorization.RoleAssignmentListResult{
					Value: []*armauthorization.RoleAssignment{
						{Properties: nil},
						nil,
					},
				},
			}},
		}
		var result cloud.SubCollectorResult
		err := collectIdentityRBAC(context.Background(), identityID, pager, &result)
		require.NoError(t, err)
		assert.Empty(t, result.Edges)
	})

	t.Run("skips nil scope", func(t *testing.T) {
		pager := &fakeIdentityRBACPager{
			pages: []armauthorization.RoleAssignmentsClientListForSubscriptionResponse{{
				RoleAssignmentListResult: armauthorization.RoleAssignmentListResult{
					Value: []*armauthorization.RoleAssignment{{
						Properties: &armauthorization.RoleAssignmentProperties{},
					}},
				},
			}},
		}
		var result cloud.SubCollectorResult
		err := collectIdentityRBAC(context.Background(), identityID, pager, &result)
		require.NoError(t, err)
		assert.Empty(t, result.Edges)
	})

	t.Run("empty pager", func(t *testing.T) {
		pager := &fakeIdentityRBACPager{}
		var result cloud.SubCollectorResult
		err := collectIdentityRBAC(context.Background(), identityID, pager, &result)
		require.NoError(t, err)
		assert.Empty(t, result.Edges)
	})
}
