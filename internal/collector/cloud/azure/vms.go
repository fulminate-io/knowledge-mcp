// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v6"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v6"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

type vmCollector struct {
	cred           azcore.TokenCredential
	subscriptionID string
}

func newVMCollector(cred azcore.TokenCredential, subID string) *vmCollector {
	return &vmCollector{cred: cred, subscriptionID: subID}
}

func (c *vmCollector) Name() string { return "azure-vms" }

func (c *vmCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	computeClient, err := armcompute.NewVirtualMachinesClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-vms: compute client: %w", err)
	}

	nicClient, err := armnetwork.NewInterfacesClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-vms: nic client: %w", err)
	}

	// Bulk-fetch all NICs and build a map from NIC resource ID → subnet IDs.
	nicSubnets, err := buildNICSubnetMap(ctx, nicClient)
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("azure-vms: nic map: %w", err)
	}

	var result cloud.SubCollectorResult

	pager := computeClient.NewListAllPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return result, fmt.Errorf("azure-vms: list: %w", err)
		}

		for _, vm := range page.Value {
			if vm.ID == nil || vm.Name == nil {
				continue
			}

			content, err := json.Marshal(vm)
			if err != nil {
				continue
			}

			result.Resources = append(result.Resources, vmResourceSpec(vm, content))
			result.Edges = append(result.Edges, vmEdges(vm, nicSubnets)...)
		}
	}

	return result, nil
}

// buildNICSubnetMap fetches all NICs in the subscription and returns a map
// from lowercase NIC resource ID to the list of subnet IDs referenced by
// that NIC's IP configurations.
func buildNICSubnetMap(ctx context.Context, client *armnetwork.InterfacesClient) (map[string][]string, error) {
	nicSubnets := make(map[string][]string)
	pager := client.NewListAllPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nicSubnets, fmt.Errorf("list nics: %w", err)
		}
		for _, nic := range page.Value {
			if nic.ID == nil || nic.Properties == nil {
				continue
			}
			key := strings.ToLower(*nic.ID)
			for _, ipCfg := range nic.Properties.IPConfigurations {
				if ipCfg.Properties != nil && ipCfg.Properties.Subnet != nil && ipCfg.Properties.Subnet.ID != nil {
					nicSubnets[key] = append(nicSubnets[key], *ipCfg.Properties.Subnet.ID)
				}
			}
		}
	}
	return nicSubnets, nil
}

func vmResourceSpec(vm *armcompute.VirtualMachine, content []byte) cloud.ResourceSpec {
	spec := cloud.ResourceSpec{
		ID:           *vm.ID,
		Name:         *vm.Name,
		ResourceType: "Microsoft.Compute/virtualMachines",
		Content:      content,
		Metadata:     map[string]string{},
	}
	if vm.Location != nil {
		spec.Region = *vm.Location
	}
	vmPropertiesMetadata(vm.Properties, spec.Metadata)
	return spec
}

func vmPropertiesMetadata(p *armcompute.VirtualMachineProperties, meta map[string]string) {
	if p == nil {
		return
	}
	if p.HardwareProfile != nil && p.HardwareProfile.VMSize != nil {
		meta["vmSize"] = string(*p.HardwareProfile.VMSize)
	}
	if p.StorageProfile != nil && p.StorageProfile.OSDisk != nil && p.StorageProfile.OSDisk.OSType != nil {
		meta["osType"] = string(*p.StorageProfile.OSDisk.OSType)
	}
	if p.ProvisioningState != nil {
		meta["provisioningState"] = *p.ProvisioningState
	}
}

func vmEdges(vm *armcompute.VirtualMachine, nicSubnets map[string][]string) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec

	// Edges: VM → managed identity (ASSUMES_ROLE)
	if vm.Identity != nil && vm.Identity.UserAssignedIdentities != nil {
		for identityID := range vm.Identity.UserAssignedIdentities {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     *vm.ID,
				TargetID:     identityID,
				Relationship: kgtypes.EdgeAssumesRole,
				Metadata:     map[string]string{"role_source": "managed_identity"},
			})
		}
	}

	// Edges: VM → subnet (USES_SUBNET) via NIC resolution.
	// VMs reference external NIC resources; we resolve each NIC to its
	// actual subnet(s) using the pre-built NIC→subnet map.
	if vm.Properties != nil && vm.Properties.NetworkProfile != nil {
		for _, nic := range vm.Properties.NetworkProfile.NetworkInterfaces {
			if nic.ID == nil {
				continue
			}
			subnetIDs := nicSubnets[strings.ToLower(*nic.ID)]
			for _, subnetID := range subnetIDs {
				edges = append(edges, cloud.EdgeSpec{
					SourceID:     *vm.ID,
					TargetID:     subnetID,
					Relationship: kgtypes.EdgeUsesSubnet,
				})
			}
		}
	}

	// Edges: disk BOUND_TO VM + VM ENCRYPTS_WITH disk encryption set.
	if vm.Properties != nil {
		edges = append(edges, storageProfileDiskEdges(*vm.ID, vm.Properties.StorageProfile)...)
	}

	return edges
}
