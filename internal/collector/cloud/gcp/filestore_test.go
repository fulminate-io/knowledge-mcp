// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	"cloud.google.com/go/filestore/apiv1/filestorepb"
	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestFilestoreSubCollector_Name(t *testing.T) {
	c := &filestoreSubCollector{}
	assert.Equal(t, "gcp-filestore", c.Name())
}

func TestFilestoreInstanceMetadata(t *testing.T) {
	inst := &filestorepb.Instance{
		Tier:  filestorepb.Instance_BASIC_SSD,
		State: filestorepb.Instance_READY,
		FileShares: []*filestorepb.FileShareConfig{
			{Name: "share1", CapacityGb: 1024},
		},
		Protocol: filestorepb.Instance_NFS_V3,
	}
	meta := filestoreInstanceMetadata(inst)
	assert.Equal(t, "BASIC_SSD", meta["tier"])
	assert.Equal(t, "READY", meta["state"])
	assert.Equal(t, "share1", meta["fileShareName"])
	assert.Equal(t, "1024", meta["capacityGb"])
	assert.Equal(t, "NFS_V3", meta["protocol"])
}

func TestFilestoreInstanceEdges_NetworkAndCMEK(t *testing.T) {
	instanceID := "projects/my-project/locations/us-central1/instances/my-fs"
	kmsKey := "projects/my-project/locations/us-central1/keyRings/r/cryptoKeys/k"

	inst := &filestorepb.Instance{
		Networks: []*filestorepb.NetworkConfig{
			{Network: "default"},
		},
		KmsKeyName: kmsKey,
	}
	edges := filestoreInstanceEdges(instanceID, inst)
	assert.Len(t, edges, 2)

	var netEdge, kmsEdge *struct {
		Relationship kgtypes.EdgeType
		SourceID     string
		TargetID     string
	}
	for i := range edges {
		e := edges[i]
		if e.Relationship == kgtypes.EdgeUsesNetwork {
			netEdge = &struct {
				Relationship kgtypes.EdgeType
				SourceID     string
				TargetID     string
			}{e.Relationship, e.SourceID, e.TargetID}
		}
		if e.Relationship == kgtypes.EdgeEncryptsWith {
			kmsEdge = &struct {
				Relationship kgtypes.EdgeType
				SourceID     string
				TargetID     string
			}{e.Relationship, e.SourceID, e.TargetID}
		}
	}

	assert.NotNil(t, netEdge, "expected USES_NETWORK edge")
	assert.Equal(t, instanceID, netEdge.SourceID)
	assert.Equal(t, "projects/my-project/global/networks/default", netEdge.TargetID)

	assert.NotNil(t, kmsEdge, "expected ENCRYPTS_WITH edge")
	assert.Equal(t, instanceID, kmsEdge.SourceID)
	assert.Equal(t, kmsKey, kmsEdge.TargetID)
}

func TestFilestoreInstanceEdges_FullyQualifiedNetwork(t *testing.T) {
	instanceID := "projects/my-project/locations/us-central1/instances/my-fs"
	fullNetwork := "projects/other-project/global/networks/shared"

	inst := &filestorepb.Instance{
		Networks: []*filestorepb.NetworkConfig{
			{Network: fullNetwork},
		},
	}
	edges := filestoreInstanceEdges(instanceID, inst)
	assert.Len(t, edges, 1)
	assert.Equal(t, kgtypes.EdgeUsesNetwork, edges[0].Relationship)
	assert.Equal(t, fullNetwork, edges[0].TargetID)
}

func TestFilestoreInstanceEdges_NoNetworkOrKMS(t *testing.T) {
	inst := &filestorepb.Instance{}
	assert.Empty(t, filestoreInstanceEdges("any", inst))
}

func TestFilestoreLocationFromName(t *testing.T) {
	assert.Equal(t, "us-central1",
		filestoreLocationFromName("projects/p/locations/us-central1/instances/i"))
	assert.Empty(t, filestoreLocationFromName("no-location"))
}
