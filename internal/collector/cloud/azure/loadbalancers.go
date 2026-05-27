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

type lbCollector struct {
	cred           azcore.TokenCredential
	subscriptionID string
}

func newLBCollector(cred azcore.TokenCredential, subID string) *lbCollector {
	return &lbCollector{cred: cred, subscriptionID: subID}
}

func (c *lbCollector) Name() string { return "azure-loadbalancers" }

func (c *lbCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	client, err := armnetwork.NewLoadBalancersClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-loadbalancers: client: %w", err)
	}

	var result cloud.SubCollectorResult

	pager := client.NewListAllPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return result, fmt.Errorf("azure-loadbalancers: list: %w", err)
		}

		for _, lb := range page.Value {
			if lb.ID == nil || lb.Name == nil {
				continue
			}

			content, err := json.Marshal(lb)
			if err != nil {
				continue
			}

			result.Resources = append(result.Resources, lbResourceSpec(lb, content))
			result.Edges = append(result.Edges, lbEdges(lb)...)
		}
	}

	return result, nil
}

func lbResourceSpec(lb *armnetwork.LoadBalancer, content []byte) cloud.ResourceSpec {
	spec := cloud.ResourceSpec{
		ID:           *lb.ID,
		Name:         *lb.Name,
		ResourceType: "Microsoft.Network/loadBalancers",
		Content:      content,
		Metadata:     map[string]string{},
	}
	if lb.Location != nil {
		spec.Region = *lb.Location
	}
	if lb.SKU != nil && lb.SKU.Name != nil {
		spec.Metadata["skuName"] = string(*lb.SKU.Name)
	}
	return spec
}

func lbEdges(lb *armnetwork.LoadBalancer) []cloud.EdgeSpec {
	if lb.Properties == nil {
		return nil
	}

	var edges []cloud.EdgeSpec

	// Edges: LB frontend → subnet (USES_SUBNET)
	for _, fip := range lb.Properties.FrontendIPConfigurations {
		if fip.Properties != nil && fip.Properties.Subnet != nil && fip.Properties.Subnet.ID != nil {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     *lb.ID,
				TargetID:     *fip.Properties.Subnet.ID,
				Relationship: kgtypes.EdgeUsesSubnet,
			})
		}
	}

	// Edges: LB → backend pool targets (TARGETS)
	for _, pool := range lb.Properties.BackendAddressPools {
		if pool.Properties == nil {
			continue
		}
		poolName := ""
		if pool.Name != nil {
			poolName = *pool.Name
		}
		for _, addr := range pool.Properties.LoadBalancerBackendAddresses {
			if addr.Properties == nil {
				continue
			}
			meta := map[string]string{}
			if poolName != "" {
				meta["pool_name"] = poolName
			}
			// Target the NIC IP config if available, otherwise the VNet.
			if addr.Properties.NetworkInterfaceIPConfiguration != nil && addr.Properties.NetworkInterfaceIPConfiguration.ID != nil {
				edges = append(edges, cloud.EdgeSpec{
					SourceID:     *lb.ID,
					TargetID:     *addr.Properties.NetworkInterfaceIPConfiguration.ID,
					Relationship: kgtypes.EdgeTargets,
					Metadata:     meta,
				})
			} else if addr.Properties.VirtualNetwork != nil && addr.Properties.VirtualNetwork.ID != nil {
				edges = append(edges, cloud.EdgeSpec{
					SourceID:     *lb.ID,
					TargetID:     *addr.Properties.VirtualNetwork.ID,
					Relationship: kgtypes.EdgeTargets,
					Metadata:     meta,
				})
			}
		}
		// Also capture subnet references from backend addresses.
		for _, addr := range pool.Properties.LoadBalancerBackendAddresses {
			if addr.Properties != nil && addr.Properties.Subnet != nil && addr.Properties.Subnet.ID != nil {
				edges = append(edges, cloud.EdgeSpec{
					SourceID:     *lb.ID,
					TargetID:     *addr.Properties.Subnet.ID,
					Relationship: kgtypes.EdgeUsesSubnet,
				})
			}
		}
	}

	return edges
}
