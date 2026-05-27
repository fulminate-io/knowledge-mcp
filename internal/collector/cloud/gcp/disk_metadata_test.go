// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	computepb "cloud.google.com/go/compute/apiv1/computepb"
	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/proto"
)

func TestDiskMetadata_StatusTypeSizeZone(t *testing.T) {
	disk := &computepb.Disk{
		Status: new("READY"),
		Type:   new("projects/p/zones/us-central1-a/diskTypes/pd-ssd"),
		SizeGb: proto.Int64(100),
		Zone:   new("projects/p/zones/us-central1-a"),
	}
	m := diskMetadata(disk)
	assert.Equal(t, "READY", m["status"])
	assert.Equal(t, "pd-ssd", m["type"])
	assert.Equal(t, "100", m["sizeGb"])
	assert.Equal(t, "us-central1-a", m["zone"])
	_, hasRegion := m["region"]
	assert.False(t, hasRegion, "zonal disk must not emit region key")
}

func TestDiskMetadata_RegionalDisk(t *testing.T) {
	// Regional disks have Region set and Zone empty — the helper must
	// emit "region" and not "zone" so search can distinguish them.
	disk := &computepb.Disk{
		Region: new("projects/p/regions/us-central1"),
		Type:   new("projects/p/regions/us-central1/diskTypes/pd-standard"),
		SizeGb: proto.Int64(50),
		Status: new("READY"),
	}
	m := diskMetadata(disk)
	assert.Equal(t, "us-central1", m["region"])
	_, hasZone := m["zone"]
	assert.False(t, hasZone, "regional disk must not emit zone key")
	assert.Equal(t, "pd-standard", m["type"])
}

func TestDiskMetadata_Labels(t *testing.T) {
	t.Run("populated labels become label/<k>=<v>", func(t *testing.T) {
		disk := &computepb.Disk{
			Labels: map[string]string{"env": "prod", "team": "data"},
		}
		m := diskMetadata(disk)
		assert.Equal(t, "prod", m["label/env"])
		assert.Equal(t, "data", m["label/team"])
	})

	t.Run("nil labels emit no label/ keys", func(t *testing.T) {
		disk := &computepb.Disk{}
		m := diskMetadata(disk)
		for k := range m {
			assert.NotContains(t, k, "label/", "unexpected label/ key %q", k)
		}
	})
}

func TestDiskMetadata_CreationTime(t *testing.T) {
	t.Run("populated CreationTimestamp becomes creation_time", func(t *testing.T) {
		disk := &computepb.Disk{
			CreationTimestamp: new("2025-01-15T10:30:00.000-07:00"),
		}
		m := diskMetadata(disk)
		assert.Equal(t, "2025-01-15T10:30:00.000-07:00", m["creation_time"])
	})

	t.Run("empty CreationTimestamp emits no creation_time key", func(t *testing.T) {
		disk := &computepb.Disk{}
		m := diskMetadata(disk)
		_, ok := m["creation_time"]
		assert.False(t, ok)
	})
}

func TestDiskMetadata_EmptyValuesSkipped(t *testing.T) {
	t.Run("zero SizeGb is skipped", func(t *testing.T) {
		// A disk with SizeGb unset (nil *int64) should not produce
		// sizeGb="0" — that would pollute search results with a noisy
		// placeholder. GetSizeGb() returns 0 for nil, which we skip.
		disk := &computepb.Disk{Status: new("READY")}
		m := diskMetadata(disk)
		_, ok := m["sizeGb"]
		assert.False(t, ok, "zero sizeGb must be skipped")
	})

	t.Run("empty status is skipped", func(t *testing.T) {
		disk := &computepb.Disk{SizeGb: proto.Int64(10)}
		m := diskMetadata(disk)
		_, ok := m["status"]
		assert.False(t, ok)
	})

	t.Run("empty type is skipped", func(t *testing.T) {
		disk := &computepb.Disk{SizeGb: proto.Int64(10)}
		m := diskMetadata(disk)
		_, ok := m["type"]
		assert.False(t, ok)
	})
}

func TestDiskMetadata_NilSafety(t *testing.T) {
	// Empty Disk proto — every getter returns the zero value. Helper
	// must not panic and must return an empty (but non-nil) map.
	disk := &computepb.Disk{}
	m := diskMetadata(disk)
	assert.NotNil(t, m)
	assert.Empty(t, m, "empty disk must produce empty metadata map")
}
