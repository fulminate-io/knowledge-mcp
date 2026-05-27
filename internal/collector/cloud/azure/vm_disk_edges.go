// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v6"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// storageProfileDiskEdges extracts BOUND_TO and ENCRYPTS_WITH edges from a VM
// or VMSS storage profile. For each managed disk (OS + data), it emits:
//   - BOUND_TO from disk resource ID to the parent (VM/VMSS)
//   - ENCRYPTS_WITH from parent to the disk encryption set (if configured)
func storageProfileDiskEdges(parentID string, sp *armcompute.StorageProfile) []cloud.EdgeSpec {
	if sp == nil {
		return nil
	}
	var edges []cloud.EdgeSpec
	edges = append(edges, osDiskEdges(parentID, sp.OSDisk)...)
	for _, dd := range sp.DataDisks {
		edges = append(edges, dataDiskEdges(parentID, dd)...)
	}
	return edges
}

// osDiskEdges emits BOUND_TO and ENCRYPTS_WITH for a VM/VMSS OS disk.
func osDiskEdges(parentID string, osDisk *armcompute.OSDisk) []cloud.EdgeSpec {
	if osDisk == nil || osDisk.ManagedDisk == nil {
		return nil
	}
	return managedDiskEdges(parentID, osDisk.ManagedDisk)
}

// dataDiskEdges emits BOUND_TO and ENCRYPTS_WITH for a VM/VMSS data disk.
func dataDiskEdges(parentID string, dd *armcompute.DataDisk) []cloud.EdgeSpec {
	if dd == nil || dd.ManagedDisk == nil {
		return nil
	}
	return managedDiskEdges(parentID, dd.ManagedDisk)
}

// managedDiskEdges emits edges for a single managed disk reference.
func managedDiskEdges(parentID string, md *armcompute.ManagedDiskParameters) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec

	// Disk BOUND_TO parent VM/VMSS.
	if md.ID != nil && *md.ID != "" {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     *md.ID,
			TargetID:     parentID,
			Relationship: kgtypes.EdgeBoundTo,
		})
	}

	// Parent ENCRYPTS_WITH disk encryption set.
	if md.DiskEncryptionSet != nil && md.DiskEncryptionSet.ID != nil && *md.DiskEncryptionSet.ID != "" {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     parentID,
			TargetID:     *md.DiskEncryptionSet.ID,
			Relationship: kgtypes.EdgeEncryptsWith,
		})
	}

	return edges
}

// vmssStorageProfileDiskEdges extracts disk edges from a VMSS
// VirtualMachineProfile storage profile. VMSS profiles use the same OSDisk and
// DataDisk types as VMs but at the template level, so disk IDs are typically
// absent (they're templates, not instances). We still extract encryption set
// references which are present at the template level.
func vmssStorageProfileDiskEdges(parentID string, sp *armcompute.VirtualMachineScaleSetStorageProfile) []cloud.EdgeSpec {
	if sp == nil {
		return nil
	}
	var edges []cloud.EdgeSpec

	// OS disk encryption set.
	if sp.OSDisk != nil && sp.OSDisk.ManagedDisk != nil {
		md := sp.OSDisk.ManagedDisk
		if md.DiskEncryptionSet != nil && md.DiskEncryptionSet.ID != nil && *md.DiskEncryptionSet.ID != "" {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     parentID,
				TargetID:     *md.DiskEncryptionSet.ID,
				Relationship: kgtypes.EdgeEncryptsWith,
			})
		}
	}

	// Data disk encryption sets.
	for _, dd := range sp.DataDisks {
		if dd == nil || dd.ManagedDisk == nil {
			continue
		}
		md := dd.ManagedDisk
		if md.DiskEncryptionSet != nil && md.DiskEncryptionSet.ID != nil && *md.DiskEncryptionSet.ID != "" {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     parentID,
				TargetID:     *md.DiskEncryptionSet.ID,
				Relationship: kgtypes.EdgeEncryptsWith,
			})
		}
	}

	return edges
}
