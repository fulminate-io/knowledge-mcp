// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	computepb "cloud.google.com/go/compute/apiv1/computepb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestDiskSubCollector_Name(t *testing.T) {
	c := &diskSubCollector{}
	assert.Equal(t, "gcp-compute-disks", c.Name())
}

func TestDiskResourceSpec_ZonalAndRegional(t *testing.T) {
	t.Run("zonal disk extracts zone", func(t *testing.T) {
		disk := &computepb.Disk{
			Name:   new("my-disk"),
			Zone:   new("https://www.googleapis.com/compute/v1/projects/p/zones/us-central1-a"),
			Status: new("READY"),
			Type:   new("projects/p/zones/us-central1-a/diskTypes/pd-ssd"),
			SizeGb: proto.Int64(100),
		}
		spec := diskResourceSpec(disk, "self-link", []byte(`{}`))
		assert.Equal(t, "gcp:compute:disk", spec.ResourceType)
		assert.Equal(t, "us-central1-a", spec.Region)
		assert.Equal(t, "my-disk", spec.Name)
		assert.Equal(t, "READY", spec.Metadata["status"])
		assert.Equal(t, "pd-ssd", spec.Metadata["type"])
		assert.Equal(t, "100", spec.Metadata["sizeGb"])
	})

	t.Run("regional disk falls back to region field", func(t *testing.T) {
		disk := &computepb.Disk{
			Name:   new("my-disk"),
			Region: new("projects/p/regions/us-central1"),
			SizeGb: proto.Int64(50),
		}
		spec := diskResourceSpec(disk, "self-link", []byte(`{}`))
		assert.Equal(t, "us-central1", spec.Region)
	})
}

func TestDiskEdges_CMEK(t *testing.T) {
	selfLink := "https://www.googleapis.com/compute/v1/projects/p/zones/z/disks/my-disk"
	kmsKey := "projects/p/locations/us-central1/keyRings/r/cryptoKeys/k"

	t.Run("emits ENCRYPTS_WITH when CMEK configured", func(t *testing.T) {
		disk := &computepb.Disk{
			DiskEncryptionKey: &computepb.CustomerEncryptionKey{
				KmsKeyName: new(kmsKey),
			},
		}
		edges := diskEdges(selfLink, disk)
		assert.Len(t, edges, 1)
		assert.Equal(t, selfLink, edges[0].SourceID)
		assert.Equal(t, kmsKey, edges[0].TargetID)
		assert.Equal(t, kgtypes.EdgeEncryptsWith, edges[0].Relationship)
		assert.Equal(t, "disk", edges[0].Metadata["encryption_scope"])
	})

	t.Run("no edges when no encryption key set", func(t *testing.T) {
		disk := &computepb.Disk{}
		assert.Empty(t, diskEdges(selfLink, disk))
	})

	t.Run("no edges when encryption key empty", func(t *testing.T) {
		disk := &computepb.Disk{
			DiskEncryptionKey: &computepb.CustomerEncryptionKey{
				KmsKeyName: new(""),
			},
		}
		assert.Empty(t, diskEdges(selfLink, disk))
	})
}

func TestDiskEdges_BoundTo(t *testing.T) {
	selfLink := "https://www.googleapis.com/compute/v1/projects/p/zones/z/disks/d"

	t.Run("multi-attach disk emits two BOUND_TO edges", func(t *testing.T) {
		disk := &computepb.Disk{
			Users: []string{
				"https://www.googleapis.com/compute/v1/projects/p/zones/z/instances/vm-1",
				"https://www.googleapis.com/compute/v1/projects/p/zones/z/instances/vm-2",
			},
		}
		edges := diskEdges(selfLink, disk)
		require.Len(t, edges, 2)
		assert.Equal(t, kgtypes.EdgeBoundTo, edges[0].Relationship)
		assert.Equal(t, disk.Users[0], edges[0].TargetID)
		assert.Equal(t, kgtypes.EdgeBoundTo, edges[1].Relationship)
		assert.Equal(t, disk.Users[1], edges[1].TargetID)
	})

	t.Run("unattached disk emits no BOUND_TO edges", func(t *testing.T) {
		disk := &computepb.Disk{}
		edges := diskEdges(selfLink, disk)
		assert.Empty(t, edges)
	})
}

func TestDiskEdges_SourceSnapshot(t *testing.T) {
	selfLink := "https://www.googleapis.com/compute/v1/projects/p/zones/z/disks/d"
	snapLink := "https://www.googleapis.com/compute/v1/projects/p/global/snapshots/snap-1"

	disk := &computepb.Disk{
		SourceSnapshot: new(snapLink),
	}
	edges := diskEdges(selfLink, disk)
	require.Len(t, edges, 1)
	assert.Equal(t, kgtypes.EdgeFromSnapshot, edges[0].Relationship)
	assert.Equal(t, snapLink, edges[0].TargetID)
}

func TestDiskEdges_SourceImage(t *testing.T) {
	selfLink := "https://www.googleapis.com/compute/v1/projects/p/zones/z/disks/d"
	imgLink := "https://www.googleapis.com/compute/v1/projects/p/global/images/img-1"

	disk := &computepb.Disk{
		SourceImage: new(imgLink),
	}
	edges := diskEdges(selfLink, disk)
	require.Len(t, edges, 1)
	assert.Equal(t, kgtypes.EdgeFromImage, edges[0].Relationship)
	assert.Equal(t, imgLink, edges[0].TargetID)
}

func TestDiskEdges_BothSnapshotAndImage(t *testing.T) {
	selfLink := "https://www.googleapis.com/compute/v1/projects/p/zones/z/disks/d"
	disk := &computepb.Disk{
		SourceSnapshot: new("projects/p/global/snapshots/snap-1"),
		SourceImage:    new("projects/p/global/images/img-1"),
	}
	edges := diskEdges(selfLink, disk)
	require.Len(t, edges, 2)
	assert.Equal(t, kgtypes.EdgeFromSnapshot, edges[0].Relationship)
	assert.Equal(t, kgtypes.EdgeFromImage, edges[1].Relationship)
}

func TestDiskEdges_AllEdgeTypes(t *testing.T) {
	selfLink := "https://www.googleapis.com/compute/v1/projects/p/zones/z/disks/d"
	kmsKey := "projects/p/locations/us-central1/keyRings/r/cryptoKeys/k"
	disk := &computepb.Disk{
		DiskEncryptionKey: &computepb.CustomerEncryptionKey{
			KmsKeyName: new(kmsKey),
		},
		Users:          []string{"projects/p/zones/z/instances/vm-1"},
		SourceSnapshot: new("projects/p/global/snapshots/snap-1"),
		SourceImage:    new("projects/p/global/images/img-1"),
	}
	edges := diskEdges(selfLink, disk)
	// Expected: ENCRYPTS_WITH + BOUND_TO + FROM_SNAPSHOT + FROM_IMAGE = 4
	require.Len(t, edges, 4)
	assert.Equal(t, kgtypes.EdgeEncryptsWith, edges[0].Relationship)
	assert.Equal(t, kgtypes.EdgeBoundTo, edges[1].Relationship)
	assert.Equal(t, kgtypes.EdgeFromSnapshot, edges[2].Relationship)
	assert.Equal(t, kgtypes.EdgeFromImage, edges[3].Relationship)
}
