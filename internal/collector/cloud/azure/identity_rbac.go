// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/authorization/armauthorization/v3"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// identityRBACPager abstracts the Azure SDK pager for subscription-wide role
// assignment listing. The real implementation is
// *runtime.Pager[armauthorization.RoleAssignmentsClientListForSubscriptionResponse];
// tests supply a fake.
type identityRBACPager interface {
	More() bool
	NextPage(ctx context.Context) (armauthorization.RoleAssignmentsClientListForSubscriptionResponse, error)
}

// collectIdentityRBAC iterates RBAC role assignments for a managed identity
// (filtered by principalId at the subscription level) and appends
// EdgeAssumesRole edges from identity -> scope for each assignment.
func collectIdentityRBAC(
	ctx context.Context,
	identityID string,
	pager identityRBACPager,
	result *cloud.SubCollectorResult,
) error {
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("azure-identity: rbac list: %w", err)
		}
		for _, ra := range page.Value {
			if ra == nil || ra.Properties == nil {
				continue
			}
			props := ra.Properties
			if props.Scope == nil || *props.Scope == "" {
				continue
			}
			md := map[string]string{"source": "rbac"}
			if props.RoleDefinitionID != nil {
				md["role_definition_id"] = *props.RoleDefinitionID
			}
			if props.PrincipalType != nil {
				md["principal_type"] = string(*props.PrincipalType)
			}
			result.Edges = append(result.Edges, cloud.EdgeSpec{
				SourceID:     identityID,
				TargetID:     *props.Scope,
				Relationship: kgtypes.EdgeAssumesRole,
				Metadata:     md,
			})
		}
	}
	return nil
}
