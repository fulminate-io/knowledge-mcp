// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v6"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestStorageProfileDiskEdges(t *testing.T) {
	vmID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/my-vm"
	diskID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Compute/disks/my-disk"
	desID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Compute/diskEncryptionSets/my-des"

	t.Run("emits BOUND_TO and ENCRYPTS_WITH for OS disk", func(t *testing.T) {
		sp := &armcompute.StorageProfile{
			OSDisk: &armcompute.OSDisk{
				ManagedDisk: &armcompute.ManagedDiskParameters{
					ID:                &diskID,
					DiskEncryptionSet: &armcompute.DiskEncryptionSetParameters{ID: &desID},
				},
			},
		}
		edges := storageProfileDiskEdges(vmID, sp)
		require.Len(t, edges, 2)

		var hasBound, hasEncrypt bool
		for _, e := range edges {
			if e.Relationship == kgtypes.EdgeBoundTo {
				assert.Equal(t, diskID, e.SourceID)
				assert.Equal(t, vmID, e.TargetID)
				hasBound = true
			}
			if e.Relationship == kgtypes.EdgeEncryptsWith {
				assert.Equal(t, vmID, e.SourceID)
				assert.Equal(t, desID, e.TargetID)
				hasEncrypt = true
			}
		}
		assert.True(t, hasBound, "expected BOUND_TO edge")
		assert.True(t, hasEncrypt, "expected ENCRYPTS_WITH edge")
	})

	t.Run("emits BOUND_TO for data disk", func(t *testing.T) {
		dataDiskID := diskID + "-data"
		sp := &armcompute.StorageProfile{
			DataDisks: []*armcompute.DataDisk{{
				ManagedDisk: &armcompute.ManagedDiskParameters{ID: &dataDiskID},
			}},
		}
		edges := storageProfileDiskEdges(vmID, sp)
		require.Len(t, edges, 1)
		assert.Equal(t, kgtypes.EdgeBoundTo, edges[0].Relationship)
		assert.Equal(t, dataDiskID, edges[0].SourceID)
	})

	t.Run("nil storage profile returns nil", func(t *testing.T) {
		edges := storageProfileDiskEdges(vmID, nil)
		assert.Nil(t, edges)
	})
}

func TestVMSSStorageProfileDiskEdges(t *testing.T) {
	vmssID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Compute/virtualMachineScaleSets/my-vmss"
	desID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Compute/diskEncryptionSets/my-des"

	t.Run("emits ENCRYPTS_WITH for VMSS OS disk", func(t *testing.T) {
		sp := &armcompute.VirtualMachineScaleSetStorageProfile{
			OSDisk: &armcompute.VirtualMachineScaleSetOSDisk{
				ManagedDisk: &armcompute.VirtualMachineScaleSetManagedDiskParameters{
					DiskEncryptionSet: &armcompute.DiskEncryptionSetParameters{ID: &desID},
				},
			},
		}
		edges := vmssStorageProfileDiskEdges(vmssID, sp)
		require.Len(t, edges, 1)
		assert.Equal(t, kgtypes.EdgeEncryptsWith, edges[0].Relationship)
		assert.Equal(t, vmssID, edges[0].SourceID)
		assert.Equal(t, desID, edges[0].TargetID)
	})

	t.Run("nil storage profile returns nil", func(t *testing.T) {
		edges := vmssStorageProfileDiskEdges(vmssID, nil)
		assert.Nil(t, edges)
	})
}
