// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v6"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestNSGRuleSpecs(t *testing.T) {
	nsgID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Network/networkSecurityGroups/mynsg"
	rule1Name := "AllowHTTP"
	rule2Name := "DenyAll"
	defaultName := "AllowVnetInBound"
	accessAllow := armnetwork.SecurityRuleAccessAllow
	accessDeny := armnetwork.SecurityRuleAccessDeny
	dirIn := armnetwork.SecurityRuleDirectionInbound
	dirOut := armnetwork.SecurityRuleDirectionOutbound
	protoTCP := armnetwork.SecurityRuleProtocolTCP
	protoAll := armnetwork.SecurityRuleProtocolAsterisk
	var p80 int32 = 100
	var p200 int32 = 200
	var p65000 int32 = 65000
	srcPrefix := "*"
	dstPrefix := "10.0.0.0/24"
	srcPort := "*"
	dstPort := "80"

	t.Run("two user rules + one default", func(t *testing.T) {
		props := &armnetwork.SecurityGroupPropertiesFormat{
			SecurityRules: []*armnetwork.SecurityRule{
				{
					Name: &rule1Name,
					Properties: &armnetwork.SecurityRulePropertiesFormat{
						Access:                   &accessAllow,
						Direction:                &dirIn,
						Protocol:                 &protoTCP,
						Priority:                 &p80,
						SourceAddressPrefix:      &srcPrefix,
						DestinationAddressPrefix: &dstPrefix,
						SourcePortRange:          &srcPort,
						DestinationPortRange:     &dstPort,
					},
				},
				{
					Name: &rule2Name,
					Properties: &armnetwork.SecurityRulePropertiesFormat{
						Access:    &accessDeny,
						Direction: &dirOut,
						Protocol:  &protoAll,
						Priority:  &p200,
					},
				},
			},
			DefaultSecurityRules: []*armnetwork.SecurityRule{{
				Name: &defaultName,
				Properties: &armnetwork.SecurityRulePropertiesFormat{
					Access:    &accessAllow,
					Direction: &dirIn,
					Protocol:  &protoAll,
					Priority:  &p65000,
				},
			}},
		}

		resources, edges := nsgRuleSpecs(nsgID, props)

		require.Len(t, resources, 3, "expected 2 user rules + 1 default")
		require.Len(t, edges, 3)

		// Verify first user rule
		assert.Equal(t, nsgID+"/securityRules/AllowHTTP", resources[0].ID)
		assert.Equal(t, "azure:nsg:rule", resources[0].ResourceType)
		assert.Equal(t, "Allow", resources[0].Metadata["access"])
		assert.Equal(t, "Inbound", resources[0].Metadata["direction"])
		assert.Equal(t, "Tcp", resources[0].Metadata["protocol"])
		assert.Equal(t, "100", resources[0].Metadata["priority"])
		assert.Equal(t, "*", resources[0].Metadata["source_address_prefix"])
		assert.Equal(t, "10.0.0.0/24", resources[0].Metadata["destination_address_prefix"])
		assert.Equal(t, "80", resources[0].Metadata["destination_port_range"])
		assert.Equal(t, "false", resources[0].Metadata["is_default"])

		// Verify second user rule
		assert.Equal(t, nsgID+"/securityRules/DenyAll", resources[1].ID)
		assert.Equal(t, "Deny", resources[1].Metadata["access"])

		// Verify default rule
		assert.Equal(t, nsgID+"/securityRules/AllowVnetInBound", resources[2].ID)
		assert.Equal(t, "true", resources[2].Metadata["is_default"])

		// All edges are EdgeContains
		for _, e := range edges {
			assert.Equal(t, kgtypes.EdgeContains, e.Relationship)
			assert.Equal(t, nsgID, e.SourceID)
		}
		assert.Equal(t, resources[0].ID, edges[0].TargetID)
		assert.Equal(t, resources[1].ID, edges[1].TargetID)
		assert.Equal(t, resources[2].ID, edges[2].TargetID)
	})

	t.Run("no rules", func(t *testing.T) {
		props := &armnetwork.SecurityGroupPropertiesFormat{}
		resources, edges := nsgRuleSpecs(nsgID, props)
		assert.Empty(t, resources)
		assert.Empty(t, edges)
	})

	t.Run("nil properties", func(t *testing.T) {
		resources, edges := nsgRuleSpecs(nsgID, nil)
		assert.Nil(t, resources)
		assert.Nil(t, edges)
	})

	t.Run("rule with nil name is skipped", func(t *testing.T) {
		props := &armnetwork.SecurityGroupPropertiesFormat{
			SecurityRules: []*armnetwork.SecurityRule{
				{Properties: &armnetwork.SecurityRulePropertiesFormat{}},
			},
		}
		resources, edges := nsgRuleSpecs(nsgID, props)
		assert.Empty(t, resources)
		assert.Empty(t, edges)
	})
}
