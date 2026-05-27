// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/keyvault/armkeyvault"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

type kvCollector struct {
	cred           azcore.TokenCredential
	subscriptionID string
}

func newKVCollector(cred azcore.TokenCredential, subID string) *kvCollector {
	return &kvCollector{cred: cred, subscriptionID: subID}
}

func (c *kvCollector) Name() string { return "azure-keyvault" }

func (c *kvCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	client, err := armkeyvault.NewVaultsClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-keyvault: client: %w", err)
	}

	var result cloud.SubCollectorResult

	pager := client.NewListBySubscriptionPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return result, fmt.Errorf("azure-keyvault: list: %w", err)
		}

		for _, vault := range page.Value {
			if vault.ID == nil || vault.Name == nil {
				continue
			}

			content, err := json.Marshal(vault)
			if err != nil {
				continue
			}

			result.Resources = append(result.Resources, kvResourceSpec(vault, content))
			result.Edges = append(result.Edges, kvEdges(vault)...)

			if kvIsRBACEnabled(vault) {
				if rbacErr := c.collectRBAC(ctx, *vault.ID, &result); rbacErr != nil {
					return result, rbacErr
				}
			}
		}
	}

	return result, nil
}

// kvIsRBACEnabled returns true when the vault uses RBAC authorization
// (nil is treated as false, matching the Azure default).
func kvIsRBACEnabled(vault *armkeyvault.Vault) bool {
	return vault.Properties != nil &&
		vault.Properties.EnableRbacAuthorization != nil &&
		*vault.Properties.EnableRbacAuthorization
}

// collectRBAC creates an armauthorization client and delegates to
// kvCollectRBAC to emit EdgeAccessedBy edges for each role assignment.
func (c *kvCollector) collectRBAC(
	ctx context.Context,
	vaultID string,
	result *cloud.SubCollectorResult,
) error {
	raClient, err := newRoleAssignmentsClient(c.subscriptionID, c.cred)
	if err != nil {
		return fmt.Errorf("azure-keyvault: rbac client: %w", err)
	}
	pager := raClient.NewListForScopePager(vaultID, nil)
	return kvCollectRBAC(ctx, vaultID, pager, result)
}

func kvResourceSpec(vault *armkeyvault.Vault, content []byte) cloud.ResourceSpec {
	spec := cloud.ResourceSpec{
		ID:           *vault.ID,
		Name:         *vault.Name,
		ResourceType: "Microsoft.KeyVault/vaults",
		Content:      content,
		Metadata:     map[string]string{},
	}
	if vault.Location != nil {
		spec.Region = *vault.Location
	}
	kvPropertiesMetadata(vault.Properties, spec.Metadata)
	return spec
}

func kvPropertiesMetadata(p *armkeyvault.VaultProperties, meta map[string]string) {
	if p == nil {
		return
	}
	if p.SKU != nil && p.SKU.Name != nil {
		meta["skuName"] = string(*p.SKU.Name)
	}
	if p.TenantID != nil {
		meta["tenantId"] = *p.TenantID
	}
	if p.EnableSoftDelete != nil {
		meta["enableSoftDelete"] = fmt.Sprintf("%t", *p.EnableSoftDelete)
	}
	if p.EnablePurgeProtection != nil {
		meta["enablePurgeProtection"] = fmt.Sprintf("%t", *p.EnablePurgeProtection)
	}
}

func kvEdges(vault *armkeyvault.Vault) []cloud.EdgeSpec {
	if vault.Properties == nil {
		return nil
	}
	var edges []cloud.EdgeSpec
	edges = append(edges, kvNetworkEdges(vault)...)
	edges = append(edges, kvAccessPolicyEdges(vault)...)
	return edges
}

// kvNetworkEdges emits USES_SUBNET from vault → subnet via NetworkACLs.
func kvNetworkEdges(vault *armkeyvault.Vault) []cloud.EdgeSpec {
	if vault.Properties.NetworkACLs == nil {
		return nil
	}
	var edges []cloud.EdgeSpec
	for _, rule := range vault.Properties.NetworkACLs.VirtualNetworkRules {
		if rule.ID != nil {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     *vault.ID,
				TargetID:     *rule.ID,
				Relationship: kgtypes.EdgeUsesSubnet,
			})
		}
	}
	return edges
}

// kvAccessPolicyEdges emits ACCESSED_BY from vault → each access policy
// principal (ObjectID). Source metadata distinguishes legacy access policies
// from RBAC assignments.
func kvAccessPolicyEdges(vault *armkeyvault.Vault) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec
	for _, ap := range vault.Properties.AccessPolicies {
		if ap == nil || ap.ObjectID == nil || *ap.ObjectID == "" {
			continue
		}
		md := map[string]string{"source": "access_policy"}
		if ap.TenantID != nil {
			md["tenant_id"] = *ap.TenantID
		}
		if ap.Permissions != nil {
			md["permissions"] = kvPermissionsCompact(ap.Permissions)
		}
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     *vault.ID,
			TargetID:     *ap.ObjectID,
			Relationship: kgtypes.EdgeAccessedBy,
			Metadata:     md,
		})
	}
	return edges
}

// kvPermissionsCompact returns a compact JSON string of non-nil permission
// slices suitable for edge metadata.
func kvPermissionsCompact(p *armkeyvault.Permissions) string {
	m := map[string]any{}
	if len(p.Keys) > 0 {
		s := make([]string, len(p.Keys))
		for i, v := range p.Keys {
			s[i] = string(*v)
		}
		m["keys"] = s
	}
	if len(p.Secrets) > 0 {
		s := make([]string, len(p.Secrets))
		for i, v := range p.Secrets {
			s[i] = string(*v)
		}
		m["secrets"] = s
	}
	if len(p.Certificates) > 0 {
		s := make([]string, len(p.Certificates))
		for i, v := range p.Certificates {
			s[i] = string(*v)
		}
		m["certificates"] = s
	}
	if len(p.Storage) > 0 {
		s := make([]string, len(p.Storage))
		for i, v := range p.Storage {
			s[i] = string(*v)
		}
		m["storage"] = s
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(b)
}
