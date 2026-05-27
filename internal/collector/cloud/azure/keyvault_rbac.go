// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/authorization/armauthorization/v3"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// newRoleAssignmentsClient wraps the SDK constructor for testability.
func newRoleAssignmentsClient(
	subID string,
	cred azcore.TokenCredential,
) (*armauthorization.RoleAssignmentsClient, error) {
	return armauthorization.NewRoleAssignmentsClient(subID, cred, nil)
}

// roleAssignmentPager abstracts the Azure SDK Pager for role assignment
// listing. The real implementation is *runtime.Pager[armauthorization.
// RoleAssignmentsClientListForScopeResponse]; tests supply a fake.
type roleAssignmentPager interface {
	More() bool
	NextPage(ctx context.Context) (armauthorization.RoleAssignmentsClientListForScopeResponse, error)
}

// kvCollectRBAC iterates RBAC role assignments scoped to the vault and
// appends EdgeAccessedBy edges from vault → each PrincipalID.
func kvCollectRBAC(
	ctx context.Context,
	vaultID string,
	pager roleAssignmentPager,
	result *cloud.SubCollectorResult,
) error {
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("azure-keyvault: rbac list: %w", err)
		}
		for _, ra := range page.Value {
			if ra == nil || ra.Properties == nil {
				continue
			}
			props := ra.Properties
			if props.PrincipalID == nil || *props.PrincipalID == "" {
				continue
			}
			md := map[string]string{"source": "rbac"}
			if props.RoleDefinitionID != nil {
				md["role_definition_id"] = *props.RoleDefinitionID
			}
			if props.PrincipalType != nil {
				md["principal_type"] = string(*props.PrincipalType)
			}
			if props.Scope != nil {
				md["scope"] = *props.Scope
			}
			result.Edges = append(result.Edges, cloud.EdgeSpec{
				SourceID:     vaultID,
				TargetID:     *props.PrincipalID,
				Relationship: kgtypes.EdgeAccessedBy,
				Metadata:     md,
			})
		}
	}
	return nil
}
