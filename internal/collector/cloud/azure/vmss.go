// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v6"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

type vmssCollector struct {
	cred           azcore.TokenCredential
	subscriptionID string
}

func newVMSSCollector(cred azcore.TokenCredential, subID string) *vmssCollector {
	return &vmssCollector{cred: cred, subscriptionID: subID}
}

func (c *vmssCollector) Name() string { return "azure-vmss" }

func (c *vmssCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	client, err := armcompute.NewVirtualMachineScaleSetsClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-vmss: client: %w", err)
	}

	var result cloud.SubCollectorResult

	pager := client.NewListAllPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return result, fmt.Errorf("azure-vmss: list: %w", err)
		}

		for _, vmss := range page.Value {
			if vmss.ID == nil || vmss.Name == nil {
				continue
			}

			content, err := json.Marshal(vmss)
			if err != nil {
				continue
			}

			result.Resources = append(result.Resources, vmssResourceSpec(vmss, content))
			result.Edges = append(result.Edges, vmssEdges(vmss)...)
		}
	}

	return result, nil
}

func vmssResourceSpec(vmss *armcompute.VirtualMachineScaleSet, content []byte) cloud.ResourceSpec {
	spec := cloud.ResourceSpec{
		ID:           *vmss.ID,
		Name:         *vmss.Name,
		ResourceType: "Microsoft.Compute/virtualMachineScaleSets",
		Content:      content,
		Metadata:     map[string]string{},
	}
	if vmss.Location != nil {
		spec.Region = *vmss.Location
	}
	if vmss.SKU != nil && vmss.SKU.Name != nil {
		spec.Metadata["skuName"] = *vmss.SKU.Name
	}
	if vmss.SKU != nil && vmss.SKU.Capacity != nil {
		spec.Metadata["capacity"] = fmt.Sprintf("%d", *vmss.SKU.Capacity)
	}
	if vmss.Properties != nil && vmss.Properties.ProvisioningState != nil {
		spec.Metadata["provisioningState"] = *vmss.Properties.ProvisioningState
	}
	return spec
}

func vmssEdges(vmss *armcompute.VirtualMachineScaleSet) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec

	// Edges: VMSS → managed identity (ASSUMES_ROLE)
	if vmss.Identity != nil && vmss.Identity.UserAssignedIdentities != nil {
		for identityID := range vmss.Identity.UserAssignedIdentities {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     *vmss.ID,
				TargetID:     identityID,
				Relationship: kgtypes.EdgeAssumesRole,
				Metadata:     map[string]string{"role_source": "managed_identity"},
			})
		}
	}

	// Edges: VMSS → subnet (USES_SUBNET) via network profile + disk encryption.
	if vmss.Properties != nil && vmss.Properties.VirtualMachineProfile != nil {
		vmp := vmss.Properties.VirtualMachineProfile
		edges = append(edges, vmssNetworkEdges(*vmss.ID, vmp.NetworkProfile)...)
		edges = append(edges, vmssStorageProfileDiskEdges(*vmss.ID, vmp.StorageProfile)...)
	}

	return edges
}

// vmssNetworkEdges extracts subnet edges from VMSS network profile.
func vmssNetworkEdges(vmssID string, np *armcompute.VirtualMachineScaleSetNetworkProfile) []cloud.EdgeSpec {
	if np == nil {
		return nil
	}
	var edges []cloud.EdgeSpec
	for _, nicConfig := range np.NetworkInterfaceConfigurations {
		if nicConfig.Properties == nil {
			continue
		}
		for _, ipConfig := range nicConfig.Properties.IPConfigurations {
			if ipConfig.Properties != nil && ipConfig.Properties.Subnet != nil && ipConfig.Properties.Subnet.ID != nil {
				edges = append(edges, cloud.EdgeSpec{
					SourceID:     vmssID,
					TargetID:     *ipConfig.Properties.Subnet.ID,
					Relationship: kgtypes.EdgeUsesSubnet,
				})
			}
		}
	}
	return edges
}
