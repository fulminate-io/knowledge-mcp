// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v6"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

type firewallCollector struct {
	cred           azcore.TokenCredential
	subscriptionID string
}

func newFirewallCollector(cred azcore.TokenCredential, subID string) *firewallCollector {
	return &firewallCollector{cred: cred, subscriptionID: subID}
}

func (c *firewallCollector) Name() string { return "azure-firewalls" }

func (c *firewallCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	client, err := armnetwork.NewAzureFirewallsClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-firewalls: client: %w", err)
	}

	var result cloud.SubCollectorResult

	pager := client.NewListAllPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return result, fmt.Errorf("azure-firewalls: list: %w", err)
		}

		for _, fw := range page.Value {
			if fw.ID == nil || fw.Name == nil {
				continue
			}

			content, err := json.Marshal(fw)
			if err != nil {
				continue
			}

			result.Resources = append(result.Resources, firewallResourceSpec(fw, content))
			result.Edges = append(result.Edges, firewallEdges(fw)...)
		}
	}

	return result, nil
}

func firewallResourceSpec(fw *armnetwork.AzureFirewall, content []byte) cloud.ResourceSpec {
	spec := cloud.ResourceSpec{
		ID:           *fw.ID,
		Name:         *fw.Name,
		ResourceType: "Microsoft.Network/azureFirewalls",
		Content:      content,
		Metadata:     map[string]string{},
	}
	if fw.Location != nil {
		spec.Region = *fw.Location
	}
	if fw.Properties != nil && fw.Properties.SKU != nil && fw.Properties.SKU.Name != nil {
		spec.Metadata["skuName"] = string(*fw.Properties.SKU.Name)
	}
	if fw.Properties != nil && fw.Properties.ThreatIntelMode != nil {
		spec.Metadata["threatIntelMode"] = string(*fw.Properties.ThreatIntelMode)
	}
	return spec
}

func firewallEdges(fw *armnetwork.AzureFirewall) []cloud.EdgeSpec {
	if fw.Properties == nil {
		return nil
	}

	var edges []cloud.EdgeSpec
	seen := map[string]bool{}

	// Edges from IP configurations: USES_SUBNET (→ AzureFirewallSubnet)
	// and USES_NETWORK (→ parent VNet, derived from subnet ID).
	for _, ipCfg := range fw.Properties.IPConfigurations {
		if ipCfg.Properties == nil || ipCfg.Properties.Subnet == nil || ipCfg.Properties.Subnet.ID == nil {
			continue
		}
		subnetID := *ipCfg.Properties.Subnet.ID
		if !seen[subnetID] {
			seen[subnetID] = true
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     *fw.ID,
				TargetID:     subnetID,
				Relationship: kgtypes.EdgeUsesSubnet,
			})
		}
		if vnetID := vnetIDFromSubnet(subnetID); vnetID != "" && !seen[vnetID] {
			seen[vnetID] = true
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     *fw.ID,
				TargetID:     vnetID,
				Relationship: kgtypes.EdgeUsesNetwork,
			})
		}
	}

	// Management IP configuration also references a subnet.
	if mgmt := fw.Properties.ManagementIPConfiguration; mgmt != nil {
		if mgmt.Properties != nil && mgmt.Properties.Subnet != nil && mgmt.Properties.Subnet.ID != nil {
			subnetID := *mgmt.Properties.Subnet.ID
			if !seen[subnetID] {
				seen[subnetID] = true
				edges = append(edges, cloud.EdgeSpec{
					SourceID:     *fw.ID,
					TargetID:     subnetID,
					Relationship: kgtypes.EdgeUsesSubnet,
				})
			}
		}
	}

	// DNAT rule PROTECTS edges (best-effort: target may not resolve to a
	// graph node; per decision, only meaningful when it does).
	edges = append(edges, firewallDNATProtectsEdges(fw)...)

	return edges
}

// firewallDNATProtectsEdges emits PROTECTS from the firewall to each DNAT
// rule's translated target. Only emits for targets that look like Azure
// resource IDs (start with "/"). IP addresses and bare FQDNs are skipped
// since they cannot reliably match graph nodes.
func firewallDNATProtectsEdges(fw *armnetwork.AzureFirewall) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec
	for _, coll := range fw.Properties.NatRuleCollections {
		if coll.Properties == nil {
			continue
		}
		for _, rule := range coll.Properties.Rules {
			target := dnatRuleTarget(rule)
			if target == "" || !strings.HasPrefix(target, "/") {
				continue
			}
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     *fw.ID,
				TargetID:     target,
				Relationship: kgtypes.EdgeProtects,
			})
		}
	}
	return edges
}

// dnatRuleTarget returns the translated target of a DNAT rule, preferring
// TranslatedFqdn over TranslatedAddress.
func dnatRuleTarget(rule *armnetwork.AzureFirewallNatRule) string {
	if rule.TranslatedFqdn != nil && *rule.TranslatedFqdn != "" {
		return *rule.TranslatedFqdn
	}
	if rule.TranslatedAddress != nil && *rule.TranslatedAddress != "" {
		return *rule.TranslatedAddress
	}
	return ""
}

// vnetIDFromSubnet extracts the VNet resource ID from a subnet resource ID.
// Subnet IDs follow the pattern:
//
//	.../Microsoft.Network/virtualNetworks/{vnet}/subnets/{subnet}
func vnetIDFromSubnet(subnetID string) string {
	lower := strings.ToLower(subnetID)
	idx := strings.Index(lower, "/subnets/")
	if idx < 0 {
		return ""
	}
	return subnetID[:idx]
}
