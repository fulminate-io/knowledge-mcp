// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"encoding/json"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestVpcPeeringCollector_Name(t *testing.T) {
	c := &vpcPeeringCollector{}
	assert.Equal(t, "vpc-peering", c.Name())
}

func TestVpcPeeringLocalEdges_ActiveSameAccount(t *testing.T) {
	conn := ec2types.VpcPeeringConnection{
		Status: &ec2types.VpcPeeringConnectionStateReason{Code: ec2types.VpcPeeringConnectionStateReasonCode("active")},
		RequesterVpcInfo: &ec2types.VpcPeeringConnectionVpcInfo{
			VpcId:   awssdk.String("vpc-req"),
			OwnerId: awssdk.String("111111111111"),
		},
		AccepterVpcInfo: &ec2types.VpcPeeringConnectionVpcInfo{
			VpcId:   awssdk.String("vpc-acc"),
			OwnerId: awssdk.String("111111111111"),
		},
	}
	peeringARN := "arn:aws:ec2:us-east-1:111111111111:vpc-peering-connection/pcx-1"
	edges := vpcPeeringLocalEdges("us-east-1", "111111111111", peeringARN, conn)

	require.Len(t, edges, 2)
	for _, e := range edges {
		assert.Equal(t, peeringARN, e.SourceID)
		assert.Equal(t, kgtypes.EdgeUsesNetwork, e.Relationship)
	}
	assert.Equal(t, "arn:aws:ec2:us-east-1:111111111111:vpc/vpc-req", edges[0].TargetID)
	assert.Equal(t, "requester", edges[0].Metadata["role"])
	assert.Equal(t, "arn:aws:ec2:us-east-1:111111111111:vpc/vpc-acc", edges[1].TargetID)
	assert.Equal(t, "accepter", edges[1].Metadata["role"])
}

func TestVpcPeeringLocalEdges_PendingStatusSkipped(t *testing.T) {
	conn := ec2types.VpcPeeringConnection{
		Status: &ec2types.VpcPeeringConnectionStateReason{Code: ec2types.VpcPeeringConnectionStateReasonCode("pending-acceptance")},
		RequesterVpcInfo: &ec2types.VpcPeeringConnectionVpcInfo{
			VpcId: awssdk.String("vpc-req"),
		},
		AccepterVpcInfo: &ec2types.VpcPeeringConnectionVpcInfo{
			VpcId: awssdk.String("vpc-acc"),
		},
	}
	edges := vpcPeeringLocalEdges("us-east-1", "111111111111", "pcx-arn", conn)
	assert.Empty(t, edges, "pending peerings should not emit local edges")
}

func TestVpcPeeringLocalEdges_CrossAccountUsesPeerOwner(t *testing.T) {
	conn := ec2types.VpcPeeringConnection{
		Status: &ec2types.VpcPeeringConnectionStateReason{Code: ec2types.VpcPeeringConnectionStateReasonCode("active")},
		RequesterVpcInfo: &ec2types.VpcPeeringConnectionVpcInfo{
			VpcId:   awssdk.String("vpc-req"),
			OwnerId: awssdk.String("999999999999"),
		},
		AccepterVpcInfo: &ec2types.VpcPeeringConnectionVpcInfo{
			VpcId:   awssdk.String("vpc-acc"),
			OwnerId: awssdk.String("111111111111"),
		},
	}
	edges := vpcPeeringLocalEdges("us-east-1", "111111111111", "pcx-arn", conn)
	require.Len(t, edges, 2)
	assert.Equal(t, "arn:aws:ec2:us-east-1:999999999999:vpc/vpc-req", edges[0].TargetID,
		"cross-account VPC ARN should embed peer's OwnerId")
}

// TestPeeringEdgesForNode_CrossAccountMetadata verifies that the
// bidirectional EdgePeeredWith edges emitted by peeringEdgesForNode carry
// metadata (requester_owner, accepter_owner, CIDRs, status_code) in the
// Evidence field.
func TestPeeringEdgesForNode_CrossAccountMetadata(t *testing.T) {
	specJSON := `{
		"VpcPeeringConnectionId": "pcx-123",
		"Status": {"Code": "active"},
		"RequesterVpcInfo": {
			"VpcId": "vpc-req",
			"OwnerId": "111111111111",
			"CidrBlock": "10.0.0.0/16"
		},
		"AccepterVpcInfo": {
			"VpcId": "vpc-acc",
			"OwnerId": "111111111111",
			"CidrBlock": "10.1.0.0/16"
		}
	}`

	node := buildPeeringNode(t, "pcx-123", specJSON)

	// For same-account test we don't need cross-account resolution (the
	// account-node-ID sets are only consulted for cross-account peerings).
	edges, idx := peeringEdgesForNode(node, nil)
	require.Len(t, edges, 4, "expected 2 EdgePeeredWith + 2 EdgeRoutesToPeer")

	// First two: bidirectional EdgePeeredWith.
	assert.Equal(t, string(kgtypes.EdgePeeredWith), edges[0].Type)
	assert.Equal(t, string(kgtypes.EdgePeeredWith), edges[1].Type)

	// Next two: EdgeRoutesToPeer — VPC → peer CIDR.
	assert.Equal(t, string(kgtypes.EdgeRoutesToPeer), edges[2].Type)
	reqVPCARN := edges[0].FromId
	accVPCARN := edges[0].ToId
	assert.Equal(t, reqVPCARN, edges[2].FromId)
	assert.Equal(t, "10.1.0.0/16", edges[2].ToId, "requester VPC routes to accepter CIDR")
	assert.Equal(t, string(kgtypes.EdgeRoutesToPeer), edges[3].Type)
	assert.Equal(t, accVPCARN, edges[3].FromId)
	assert.Equal(t, "10.0.0.0/16", edges[3].ToId, "accepter VPC routes to requester CIDR")

	// Verify metadata is present on PeeredWith edges.
	for i := range edges[:2] {
		e := &edges[i]
		assert.NotEmpty(t, e.Evidence, "EdgePeeredWith must carry evidence metadata")
		var meta map[string]string
		require.NoError(t, json.Unmarshal([]byte(e.Evidence), &meta))
		assert.Equal(t, "111111111111", meta["requester_owner"])
		assert.Equal(t, "111111111111", meta["accepter_owner"])
		assert.Equal(t, "10.0.0.0/16", meta["requester_cidr"])
		assert.Equal(t, "10.1.0.0/16", meta["accepter_cidr"])
		assert.Equal(t, "active", meta["status_code"])
	}

	// Verify peer index.
	assert.True(t, idx["vpc-req"]["vpc-acc"])
	assert.True(t, idx["vpc-acc"]["vpc-req"])
}

// TestPeeringMetadata_NilFields verifies the helper doesn't panic on nil
// fields and returns empty string when nothing is populated.
func TestPeeringMetadata_NilFields(t *testing.T) {
	spec := vpcPeeringSpec{}
	assert.Empty(t, peeringMetadata(spec))
}

func TestPeeringMetadata_PartialFields(t *testing.T) {
	owner := "111111111111"
	spec := vpcPeeringSpec{
		RequesterVpcInfo: &vpcPeeringVpcInfo{OwnerId: &owner},
		Status:           &vpcPeeringStatus{Code: new("active")},
	}
	raw := peeringMetadata(spec)
	assert.NotEmpty(t, raw)

	var meta map[string]string
	require.NoError(t, json.Unmarshal([]byte(raw), &meta))
	assert.Equal(t, "111111111111", meta["requester_owner"])
	assert.Equal(t, "active", meta["status_code"])
	_, hasAccepter := meta["accepter_owner"]
	assert.False(t, hasAccepter)
}
