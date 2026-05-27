// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v6"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestDiskEdges(t *testing.T) {
	diskID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Compute/disks/my-disk"
	vmID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/my-vm"
	desID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Compute/diskEncryptionSets/my-des"

	t.Run("emits BOUND_TO and ENCRYPTS_WITH", func(t *testing.T) {
		disk := &armcompute.Disk{
			ID:        &diskID,
			ManagedBy: &vmID,
			Properties: &armcompute.DiskProperties{
				Encryption: &armcompute.Encryption{
					DiskEncryptionSetID: &desID,
				},
			},
		}
		edges := diskEdges(disk)
		require.Len(t, edges, 2)

		var hasBound, hasEncrypt bool
		for _, e := range edges {
			if e.Relationship == kgtypes.EdgeBoundTo {
				assert.Equal(t, diskID, e.SourceID)
				assert.Equal(t, vmID, e.TargetID)
				hasBound = true
			}
			if e.Relationship == kgtypes.EdgeEncryptsWith {
				assert.Equal(t, diskID, e.SourceID)
				assert.Equal(t, desID, e.TargetID)
				hasEncrypt = true
			}
		}
		assert.True(t, hasBound, "expected BOUND_TO edge")
		assert.True(t, hasEncrypt, "expected ENCRYPTS_WITH edge")
	})

	t.Run("no edges when unattached and no encryption", func(t *testing.T) {
		disk := &armcompute.Disk{
			ID:         &diskID,
			Properties: &armcompute.DiskProperties{},
		}
		edges := diskEdges(disk)
		assert.Empty(t, edges)
	})
}
