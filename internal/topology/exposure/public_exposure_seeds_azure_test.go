// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// public_exposure_seeds_azure_test.go covers Azure seed rules:
// Application Gateway with public IP and Front Door endpoint.

const azureSeedsAccount = "azure-seeds-test"

func buildAzureSeedsFixture(t *testing.T) *cloudFixture {
	t.Helper()
	fx := newCloudFixture(t)
	fx.account(azureSeedsAccount)
	return fx
}

// addSeedNode creates a cloud resource node with JSON content in the
// specified account. Unlike seedFromFixture (which is hardcoded to
// peSeedsAccount), this helper takes an explicit account parameter.
func addSeedNode(t *testing.T, fx *cloudFixture, account, id, resourceType string, content any) {
	t.Helper()
	body, err := json.Marshal(content)
	require.NoError(t, err)
	fx.AddCloudResourceWithContent(account, id, id, resourceType, string(body), nil)
}

// TestAzureSeedRules_AppGateway_PublicIP verifies an Application Gateway
// with a public frontend IP configuration fires the seed rule.
func TestAzureSeedRules_AppGateway_PublicIP(t *testing.T) {
	fx := buildAzureSeedsFixture(t)
	addSeedNode(t, fx, azureSeedsAccount, "azure:appgw:public",
		"Microsoft.Network/applicationGateways", map[string]any{
			"properties": map[string]any{
				"frontendIPConfigurations": []map[string]any{
					{
						"properties": map[string]any{
							"publicIPAddress": map[string]any{
								"id": "/subscriptions/abc/resourceGroups/rg/providers/Microsoft.Network/publicIPAddresses/pip-1",
							},
						},
					},
				},
			},
		})

	scoped := fx.reader(azureSeedsAccount)
	seeds := enumerateSeeds(newTestCtx(t), scoped, "azure")

	require.Len(t, seeds, 1)
	assert.Equal(t, "azure:appgw:public", seeds[0].NodeID)
	assert.InDelta(t, 0.9, seeds[0].EntryScore, 0.0001)
	assert.Contains(t, seeds[0].Reason, "Application Gateway")
}

// TestAzureSeedRules_AppGateway_InternalOnly verifies an Application
// Gateway with no public IP does NOT fire.
func TestAzureSeedRules_AppGateway_InternalOnly(t *testing.T) {
	fx := buildAzureSeedsFixture(t)
	addSeedNode(t, fx, azureSeedsAccount, "azure:appgw:internal",
		"Microsoft.Network/applicationGateways", map[string]any{
			"properties": map[string]any{
				"frontendIPConfigurations": []map[string]any{
					{
						"properties": map[string]any{
							// No publicIPAddress field.
						},
					},
				},
			},
		})

	scoped := fx.reader(azureSeedsAccount)
	seeds := enumerateSeeds(newTestCtx(t), scoped, "azure")
	assert.Empty(t, seeds)
}

// TestAzureSeedRules_FrontDoor verifies an Azure Front Door endpoint
// always fires (always public by design).
func TestAzureSeedRules_FrontDoor(t *testing.T) {
	fx := buildAzureSeedsFixture(t)
	addSeedNode(t, fx, azureSeedsAccount, "azure:afd:ep1",
		"Microsoft.Cdn/profiles/afdEndpoints", map[string]any{})

	scoped := fx.reader(azureSeedsAccount)
	seeds := enumerateSeeds(newTestCtx(t), scoped, "azure")

	require.Len(t, seeds, 1)
	assert.Equal(t, "azure:afd:ep1", seeds[0].NodeID)
	assert.InDelta(t, 0.9, seeds[0].EntryScore, 0.0001)
	assert.Contains(t, seeds[0].Reason, "Front Door")
}

// TestAzureSeedRules_MixedResources verifies azure filter only returns
// azure seeds when both azure and aws resources exist.
func TestAzureSeedRules_MixedResources(t *testing.T) {
	fx := buildAzureSeedsFixture(t)
	addSeedNode(t, fx, azureSeedsAccount, "azure:afd:mixed",
		"Microsoft.Cdn/profiles/afdEndpoints", map[string]any{})
	addSeedNode(t, fx, azureSeedsAccount, "arn:alb:mixed",
		"elbv2-loadbalancer", map[string]any{"Scheme": "internet-facing"})

	scoped := fx.reader(azureSeedsAccount)
	azureSeeds := enumerateSeeds(newTestCtx(t), scoped, "azure")
	require.Len(t, azureSeeds, 1)
	assert.Equal(t, "azure:afd:mixed", azureSeeds[0].NodeID)
}
