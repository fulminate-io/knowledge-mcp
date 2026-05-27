// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	computepb "cloud.google.com/go/compute/apiv1/computepb"
	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestExtractZone(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"projects/my-project/zones/us-central1-a", "us-central1-a"},
		{"us-central1-a", "us-central1-a"},
		{"", ""},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, extractZone(tt.input))
	}
}

func TestExtractLast(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"projects/p/zones/us-central1-a", "us-central1-a"},
		{"projects/p/machineTypes/e2-medium", "e2-medium"},
		{"simple", "simple"},
		{"", ""},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, extractLast(tt.input))
	}
}

func TestSaResourceName(t *testing.T) {
	got := saResourceName("my-project", "sa@my-project.iam.gserviceaccount.com")
	assert.Equal(t, "projects/my-project/serviceAccounts/sa@my-project.iam.gserviceaccount.com", got)
}

func TestComputeSubCollector_Name(t *testing.T) {
	c := &computeSubCollector{}
	assert.Equal(t, "gcp-compute-instances", c.Name())
}

func TestComputeEdges_BoundTo(t *testing.T) {
	diskSource := "https://www.googleapis.com/compute/v1/projects/p/zones/z/disks/my-disk"
	selfLink := "https://www.googleapis.com/compute/v1/projects/p/zones/z/instances/my-vm"

	t.Run("emits BOUND_TO for attached disks", func(t *testing.T) {
		inst := &computepb.Instance{
			Disks: []*computepb.AttachedDisk{
				{Source: new(diskSource)},
			},
		}
		edges := computeEdges("p", selfLink, inst)

		var found bool
		for _, e := range edges {
			if e.Relationship == kgtypes.EdgeBoundTo {
				assert.Equal(t, diskSource, e.SourceID)
				assert.Equal(t, selfLink, e.TargetID)
				found = true
			}
		}
		assert.True(t, found, "expected EdgeBoundTo edge")
	})

	t.Run("no BOUND_TO for empty disk source", func(t *testing.T) {
		inst := &computepb.Instance{
			Disks: []*computepb.AttachedDisk{
				{Source: new("")},
			},
		}
		edges := computeEdges("p", selfLink, inst)

		for _, e := range edges {
			assert.NotEqual(t, kgtypes.EdgeBoundTo, e.Relationship)
		}
	})

	t.Run("no BOUND_TO when no disks", func(t *testing.T) {
		inst := &computepb.Instance{}
		edges := computeEdges("p", selfLink, inst)

		for _, e := range edges {
			assert.NotEqual(t, kgtypes.EdgeBoundTo, e.Relationship)
		}
	})
}
