// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v6"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

type appGatewayCollector struct {
	cred           azcore.TokenCredential
	subscriptionID string
}

func newAppGatewayCollector(cred azcore.TokenCredential, subID string) *appGatewayCollector {
	return &appGatewayCollector{cred: cred, subscriptionID: subID}
}

func (c *appGatewayCollector) Name() string { return "azure-appgateways" }

func (c *appGatewayCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	client, err := armnetwork.NewApplicationGatewaysClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-appgateways: client: %w", err)
	}

	var result cloud.SubCollectorResult

	pager := client.NewListAllPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return result, fmt.Errorf("azure-appgateways: list: %w", err)
		}

		for _, gw := range page.Value {
			if gw.ID == nil || gw.Name == nil {
				continue
			}

			content, err := json.Marshal(gw)
			if err != nil {
				continue
			}

			result.Resources = append(result.Resources, appGwResourceSpec(gw, content))
			result.Edges = append(result.Edges, appGwEdges(gw)...)
		}
	}

	return result, nil
}

func appGwResourceSpec(gw *armnetwork.ApplicationGateway, content []byte) cloud.ResourceSpec {
	spec := cloud.ResourceSpec{
		ID:           *gw.ID,
		Name:         *gw.Name,
		ResourceType: "Microsoft.Network/applicationGateways",
		Content:      content,
		Metadata:     map[string]string{},
	}
	if gw.Location != nil {
		spec.Region = *gw.Location
	}
	if gw.Properties != nil && gw.Properties.SKU != nil && gw.Properties.SKU.Name != nil {
		spec.Metadata["skuName"] = string(*gw.Properties.SKU.Name)
	}
	return spec
}

func appGwEdges(gw *armnetwork.ApplicationGateway) []cloud.EdgeSpec {
	if gw.Properties == nil {
		return nil
	}

	var edges []cloud.EdgeSpec

	// Edges: App Gateway → subnet (USES_SUBNET) via gateway IP configurations.
	for _, ipCfg := range gw.Properties.GatewayIPConfigurations {
		if ipCfg.Properties != nil && ipCfg.Properties.Subnet != nil && ipCfg.Properties.Subnet.ID != nil {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     *gw.ID,
				TargetID:     *ipCfg.Properties.Subnet.ID,
				Relationship: kgtypes.EdgeUsesSubnet,
			})
		}
	}

	// Edges: App Gateway → backend pool targets (TARGETS) via NIC IP configurations.
	edges = append(edges, appGwBackendEdges(gw)...)

	// Edge: WAF policy PROTECTS App Gateway.
	if gw.Properties.FirewallPolicy != nil && gw.Properties.FirewallPolicy.ID != nil {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     *gw.Properties.FirewallPolicy.ID,
			TargetID:     *gw.ID,
			Relationship: kgtypes.EdgeProtects,
		})
	}

	// Edges: App Gateway USES_CERT → Key Vault certificate/secret.
	edges = append(edges, appGwSSLCertEdges(gw)...)

	return edges
}

// appGwSSLCertEdges emits USES_CERT from App Gateway to each Key Vault
// certificate referenced by the gateway's SSL certificate configurations.
func appGwSSLCertEdges(gw *armnetwork.ApplicationGateway) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec
	for _, cert := range gw.Properties.SSLCertificates {
		if cert.Properties == nil || cert.Properties.KeyVaultSecretID == nil {
			continue
		}
		secretID := *cert.Properties.KeyVaultSecretID
		if secretID == "" {
			continue
		}
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     *gw.ID,
			TargetID:     secretID,
			Relationship: kgtypes.EdgeUsesCert,
		})
	}
	return edges
}

func appGwBackendEdges(gw *armnetwork.ApplicationGateway) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec
	for _, pool := range gw.Properties.BackendAddressPools {
		if pool.Properties == nil {
			continue
		}
		// BackendIPConfigurations are read-only refs to NIC IP configs (VMs/VMSS).
		for _, ipCfg := range pool.Properties.BackendIPConfigurations {
			if ipCfg.ID != nil {
				edges = append(edges, cloud.EdgeSpec{
					SourceID:     *gw.ID,
					TargetID:     *ipCfg.ID,
					Relationship: kgtypes.EdgeTargets,
				})
			}
		}
	}
	return edges
}
