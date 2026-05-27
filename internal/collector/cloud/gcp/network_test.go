// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	computepb "cloud.google.com/go/compute/apiv1/computepb"
	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/proto"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestNetworksSubCollector_Name(t *testing.T) {
	c := &networksSubCollector{}
	assert.Equal(t, "gcp-networks", c.Name())
}

func TestSubnetsSubCollector_Name(t *testing.T) {
	c := &subnetsSubCollector{}
	assert.Equal(t, "gcp-subnets", c.Name())
}

func TestFirewallsSubCollector_Name(t *testing.T) {
	c := &firewallsSubCollector{}
	assert.Equal(t, "gcp-firewalls", c.Name())
}

func TestBoolStr(t *testing.T) {
	assert.Equal(t, "true", boolStr(true))
	assert.Equal(t, "false", boolStr(false))
}

func TestNetworkPeeredWithEdges(t *testing.T) {
	peerNetwork := "https://www.googleapis.com/compute/v1/projects/peer-proj/global/networks/peer-net"
	selfLink := "https://www.googleapis.com/compute/v1/projects/my-proj/global/networks/my-net"

	t.Run("emits PEERED_WITH for active peering", func(t *testing.T) {
		network := &computepb.Network{
			SelfLink: new(selfLink),
			Name:     new("my-net"),
			Peerings: []*computepb.NetworkPeering{
				{
					Name:    new("peer-1"),
					Network: new(peerNetwork),
					State:   new("ACTIVE"),
				},
			},
		}
		edges := networkPeeringEdges(selfLink, network)
		assert.Len(t, edges, 1)
		assert.Equal(t, selfLink, edges[0].SourceID)
		assert.Equal(t, peerNetwork, edges[0].TargetID)
		assert.Equal(t, kgtypes.EdgePeeredWith, edges[0].Relationship)
		assert.Equal(t, "peer-1", edges[0].Metadata["peering_name"])
	})

	t.Run("no edge for inactive peering", func(t *testing.T) {
		network := &computepb.Network{
			SelfLink: new(selfLink),
			Peerings: []*computepb.NetworkPeering{
				{
					Name:    new("peer-2"),
					Network: new(peerNetwork),
					State:   new("INACTIVE"),
				},
			},
		}
		edges := networkPeeringEdges(selfLink, network)
		assert.Empty(t, edges)
	})

	t.Run("no edge when no peerings", func(t *testing.T) {
		network := &computepb.Network{
			SelfLink: new(selfLink),
		}
		edges := networkPeeringEdges(selfLink, network)
		assert.Empty(t, edges)
	})
}

func TestIntStr(t *testing.T) {
	assert.Equal(t, "0", intStr(0))
	assert.Equal(t, "1000", intStr(1000))
	assert.Equal(t, "-1", intStr(-1))
}

func TestSubnetsSubCollector_FlowLogMetadata(t *testing.T) {
	t.Run("flow logs enabled with full log config", func(t *testing.T) {
		subnet := &computepb.Subnetwork{
			IpCidrRange:    new("10.0.0.0/24"),
			Purpose:        new("PRIVATE"),
			EnableFlowLogs: new(true),
			LogConfig: &computepb.SubnetworkLogConfig{
				Enable:              new(true),
				FlowSampling:        proto.Float32(0.5),
				AggregationInterval: new("INTERVAL_5_SEC"),
			},
		}
		meta := subnetMetadata(subnet)
		assert.Equal(t, "10.0.0.0/24", meta["ipCidrRange"])
		assert.Equal(t, "PRIVATE", meta["purpose"])
		assert.Equal(t, "true", meta["flowLogEnabled"])
		assert.Equal(t, "0.5", meta["flowLogSampleRate"])
		assert.Equal(t, "INTERVAL_5_SEC", meta["flowLogAggregationInterval"])
	})

	t.Run("flow logs disabled", func(t *testing.T) {
		subnet := &computepb.Subnetwork{
			IpCidrRange:    new("10.0.1.0/24"),
			Purpose:        new("PRIVATE"),
			EnableFlowLogs: new(false),
		}
		meta := subnetMetadata(subnet)
		assert.Equal(t, "false", meta["flowLogEnabled"])
		_, hasRate := meta["flowLogSampleRate"]
		assert.False(t, hasRate)
	})

	t.Run("log config nil", func(t *testing.T) {
		subnet := &computepb.Subnetwork{
			IpCidrRange: new("10.0.2.0/24"),
		}
		meta := subnetMetadata(subnet)
		assert.Equal(t, "false", meta["flowLogEnabled"])
	})
}
