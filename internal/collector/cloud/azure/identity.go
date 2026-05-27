// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/authorization/armauthorization/v3"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/msi/armmsi"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

type identityCollector struct {
	cred           azcore.TokenCredential
	subscriptionID string
}

func newIdentityCollector(cred azcore.TokenCredential, subID string) *identityCollector {
	return &identityCollector{cred: cred, subscriptionID: subID}
}

func (c *identityCollector) Name() string { return "azure-identity" }

func (c *identityCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	client, err := armmsi.NewUserAssignedIdentitiesClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-identity: client: %w", err)
	}

	// Best-effort: create RBAC client once for all identities.
	raClient, raErr := newRoleAssignmentsClient(c.subscriptionID, c.cred)
	if raErr != nil {
		slog.Debug("azure-identity: rbac client (best-effort)", "error", raErr)
	}

	// Best-effort: create federated credentials client once.
	ficClient, ficErr := armmsi.NewFederatedIdentityCredentialsClient(c.subscriptionID, c.cred, nil)
	if ficErr != nil {
		slog.Debug("azure-identity: federated client (best-effort)", "error", ficErr)
	}

	var result cloud.SubCollectorResult

	pager := client.NewListBySubscriptionPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return result, fmt.Errorf("azure-identity: list: %w", err)
		}

		for _, identity := range page.Value {
			if identity.ID == nil || identity.Name == nil {
				continue
			}

			content, err := json.Marshal(identity)
			if err != nil {
				continue
			}

			result.Resources = append(result.Resources, identityResourceSpec(identity, content))
			c.collectRBACForIdentity(ctx, raClient, identity, &result)
			c.collectFederatedForIdentity(ctx, ficClient, identity, &result)
		}
	}

	return result, nil
}

// collectRBACForIdentity queries role assignments for a single identity and
// appends EdgeAssumesRole edges. Failures are logged and skipped (best-effort).
func (c *identityCollector) collectRBACForIdentity(
	ctx context.Context,
	raClient *armauthorization.RoleAssignmentsClient,
	identity *armmsi.Identity,
	result *cloud.SubCollectorResult,
) {
	if raClient == nil {
		return
	}
	if identity.Properties == nil || identity.Properties.PrincipalID == nil {
		return
	}
	filter := fmt.Sprintf("principalId eq '%s'", *identity.Properties.PrincipalID)
	pager := raClient.NewListForSubscriptionPager(
		&armauthorization.RoleAssignmentsClientListForSubscriptionOptions{
			Filter: &filter,
		},
	)
	if err := collectIdentityRBAC(ctx, *identity.ID, pager, result); err != nil {
		slog.Debug("azure-identity: rbac collection", "identity", *identity.ID, "error", err)
	}
}

// collectFederatedForIdentity queries federated identity credentials for a
// single identity and appends EdgeWorkloadIdentity edges. Best-effort.
func (c *identityCollector) collectFederatedForIdentity(
	ctx context.Context,
	ficClient *armmsi.FederatedIdentityCredentialsClient,
	identity *armmsi.Identity,
	result *cloud.SubCollectorResult,
) {
	if ficClient == nil {
		return
	}
	rg := extractResourceGroup(*identity.ID)
	name := extractIdentityName(*identity.ID)
	if rg == "" || name == "" {
		slog.Debug("azure-identity: cannot parse ARM ID for federated creds", "id", *identity.ID)
		return
	}
	pager := ficClient.NewListPager(rg, name, nil)
	if err := collectFederatedCredentials(ctx, *identity.ID, pager, result); err != nil {
		slog.Debug("azure-identity: federated collection", "identity", *identity.ID, "error", err)
	}
}

func identityResourceSpec(identity *armmsi.Identity, content []byte) cloud.ResourceSpec {
	spec := cloud.ResourceSpec{
		ID:           *identity.ID,
		Name:         *identity.Name,
		ResourceType: "Microsoft.ManagedIdentity/userAssignedIdentities",
		Content:      content,
		Metadata:     map[string]string{},
	}
	if identity.Location != nil {
		spec.Region = *identity.Location
	}
	if identity.Properties != nil {
		if identity.Properties.ClientID != nil {
			spec.Metadata["clientId"] = *identity.Properties.ClientID
		}
		if identity.Properties.PrincipalID != nil {
			spec.Metadata["principalId"] = *identity.Properties.PrincipalID
		}
		if identity.Properties.TenantID != nil {
			spec.Metadata["tenantId"] = *identity.Properties.TenantID
		}
	}
	return spec
}
