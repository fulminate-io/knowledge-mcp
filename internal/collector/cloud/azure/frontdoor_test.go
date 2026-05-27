// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/cdn/armcdn/v2"
	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestFrontDoorCollector_Name(t *testing.T) {
	c := &frontDoorCollector{}
	assert.Equal(t, "azure-frontdoor", c.Name())
}

func TestIsFrontDoorProfile(t *testing.T) {
	tests := []struct {
		name string
		sku  *armcdn.SKU
		want bool
	}{
		{
			"nil SKU",
			nil,
			false,
		},
		{
			"nil SKU name",
			&armcdn.SKU{Name: nil},
			false,
		},
		{
			"Standard Azure Front Door",
			&armcdn.SKU{Name: skuName(armcdn.SKUNameStandardAzureFrontDoor)},
			true,
		},
		{
			"Premium Azure Front Door",
			&armcdn.SKU{Name: skuName(armcdn.SKUNamePremiumAzureFrontDoor)},
			true,
		},
		{
			"Standard Microsoft CDN (not Front Door)",
			&armcdn.SKU{Name: skuName(armcdn.SKUNameStandardMicrosoft)},
			false,
		},
		{
			"Standard Verizon CDN (not Front Door)",
			&armcdn.SKU{Name: skuName(armcdn.SKUNameStandardVerizon)},
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile := &armcdn.Profile{SKU: tt.sku}
			assert.Equal(t, tt.want, isFrontDoorProfile(profile))
		})
	}
}

func TestFdProfileResourceSpec(t *testing.T) {
	skuN := armcdn.SKUNamePremiumAzureFrontDoor
	profile := &armcdn.Profile{
		ID:       new("/subscriptions/sub-1/resourceGroups/rg/providers/Microsoft.Cdn/profiles/fd-1"),
		Name:     new("fd-1"),
		Location: new("global"),
		SKU:      &armcdn.SKU{Name: &skuN},
	}
	spec := fdProfileResourceSpec(profile, []byte("{}"))
	assert.Equal(t, *profile.ID, spec.ID)
	assert.Equal(t, "fd-1", spec.Name)
	assert.Equal(t, "Microsoft.Cdn/profiles", spec.ResourceType)
	assert.Equal(t, "global", spec.Region)
	assert.Equal(t, string(armcdn.SKUNamePremiumAzureFrontDoor), spec.Metadata["skuName"])
}

// skuName returns a pointer to a SKUName value.
//
//go:fix inline
func skuName(n armcdn.SKUName) *armcdn.SKUName { return new(n) }

func TestFDSecurityPolicyProtectsEdges(t *testing.T) {
	profileID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Cdn/profiles/fd-1"
	wafID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Network/FrontDoorWebApplicationFirewallPolicies/waf-1"
	profile := &armcdn.Profile{ID: &profileID}

	t.Run("emits PROTECTS when WAF policy set", func(t *testing.T) {
		spType := armcdn.SecurityPolicyTypeWebApplicationFirewall
		sp := &armcdn.SecurityPolicy{
			Properties: &armcdn.SecurityPolicyProperties{
				Parameters: &armcdn.SecurityPolicyWebApplicationFirewallParameters{
					Type:      &spType,
					WafPolicy: &armcdn.ResourceReference{ID: &wafID},
				},
			},
		}
		edges := fdSecurityPolicyProtectsEdges(sp, profile)
		assert.Len(t, edges, 1)
		assert.Equal(t, kgtypes.EdgeProtects, edges[0].Relationship)
		assert.Equal(t, wafID, edges[0].SourceID)
		assert.Equal(t, profileID, edges[0].TargetID)
	})

	t.Run("no edge when no WAF parameters", func(t *testing.T) {
		sp := &armcdn.SecurityPolicy{
			Properties: &armcdn.SecurityPolicyProperties{},
		}
		edges := fdSecurityPolicyProtectsEdges(sp, profile)
		assert.Empty(t, edges)
	})
}

func TestFDCustomDomainCertEdge(t *testing.T) {
	profileID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Cdn/profiles/fd-1"
	secretID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Cdn/profiles/fd-1/secrets/cert-1"
	profile := &armcdn.Profile{ID: &profileID}

	t.Run("emits USES_CERT when TLS secret set", func(t *testing.T) {
		domain := &armcdn.AFDDomain{
			Properties: &armcdn.AFDDomainProperties{
				TLSSettings: &armcdn.AFDDomainHTTPSParameters{
					Secret: &armcdn.ResourceReference{ID: &secretID},
				},
			},
		}
		edge := fdCustomDomainCertEdge(domain, profile)
		assert.NotNil(t, edge)
		assert.Equal(t, kgtypes.EdgeUsesCert, edge.Relationship)
		assert.Equal(t, profileID, edge.SourceID)
		assert.Equal(t, secretID, edge.TargetID)
	})

	t.Run("no edge when no TLS settings", func(t *testing.T) {
		domain := &armcdn.AFDDomain{
			Properties: &armcdn.AFDDomainProperties{},
		}
		edge := fdCustomDomainCertEdge(domain, profile)
		assert.Nil(t, edge)
	})
}
