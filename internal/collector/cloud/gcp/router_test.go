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

func TestRouterSubCollector_Name(t *testing.T) {
	c := &routerSubCollector{}
	assert.Equal(t, "gcp-routers", c.Name())
}

func TestRouterEdges_WithNetwork(t *testing.T) {
	selfLink := "https://www.googleapis.com/compute/v1/projects/p/regions/us-central1/routers/my-router"
	router := &computepb.Router{
		Name:     new("my-router"),
		Network:  new("https://www.googleapis.com/compute/v1/projects/p/global/networks/my-vpc"),
		Region:   new("https://www.googleapis.com/compute/v1/projects/p/regions/us-central1"),
		SelfLink: new(selfLink),
	}

	edges, proxies := routerEdges("p", selfLink, router, map[string]bool{})
	assert.Len(t, edges, 1)
	assert.Empty(t, proxies)
	assert.Equal(t, selfLink, edges[0].SourceID)
	assert.Equal(t, router.GetNetwork(), edges[0].TargetID)
	assert.Equal(t, kgtypes.EdgeUsesNetwork, edges[0].Relationship)
}

func TestRouterEdges_NoNetwork(t *testing.T) {
	selfLink := "https://www.googleapis.com/compute/v1/projects/p/regions/us-central1/routers/my-router"
	router := &computepb.Router{
		Name:     new("my-router"),
		Region:   new("https://www.googleapis.com/compute/v1/projects/p/regions/us-central1"),
		SelfLink: new(selfLink),
	}

	edges, proxies := routerEdges("p", selfLink, router, map[string]bool{})
	assert.Empty(t, edges)
	assert.Empty(t, proxies)
}

func TestRouterResourceSpec(t *testing.T) {
	selfLink := "https://www.googleapis.com/compute/v1/projects/p/regions/us-central1/routers/my-router"
	router := &computepb.Router{
		Name:     new("my-router"),
		Network:  new("https://www.googleapis.com/compute/v1/projects/p/global/networks/my-vpc"),
		Region:   new("https://www.googleapis.com/compute/v1/projects/p/regions/us-central1"),
		SelfLink: new(selfLink),
		Bgp: &computepb.RouterBgp{
			Asn:           proto.Uint32(65001),
			AdvertiseMode: new("DEFAULT"),
			AdvertisedIpRanges: []*computepb.RouterAdvertisedIpRange{
				{Range: new("10.0.0.0/24")},
				{Range: new("10.1.0.0/24")},
			},
		},
	}

	content := []byte(`{"name":"my-router"}`)
	spec := routerResourceSpec(selfLink, router, content)

	assert.Equal(t, selfLink, spec.ID)
	assert.Equal(t, "my-router", spec.Name)
	assert.Equal(t, "gcp:compute:router", spec.ResourceType)
	assert.Equal(t, "us-central1", spec.Region)
	assert.Equal(t, "us-central1", spec.Metadata["region"])
	assert.Equal(t, "65001", spec.Metadata["bgpAsn"])
	assert.Equal(t, "DEFAULT", spec.Metadata["advertiseMode"])
	assert.Equal(t, "10.0.0.0/24,10.1.0.0/24", spec.Metadata["advertisedIpRanges"])
}

func TestRouterEdges_WithNATsAndBGPPeers(t *testing.T) {
	selfLink := "https://www.googleapis.com/compute/v1/projects/p/regions/us-central1/routers/my-router"
	router := &computepb.Router{
		Name:     new("my-router"),
		Network:  new("https://www.googleapis.com/compute/v1/projects/p/global/networks/my-vpc"),
		Region:   new("https://www.googleapis.com/compute/v1/projects/p/regions/us-central1"),
		SelfLink: new(selfLink),
		Nats: []*computepb.RouterNat{
			{Name: new("my-nat")},
		},
		BgpPeers: []*computepb.RouterBgpPeer{
			{
				Name:          new("peer-1"),
				PeerIpAddress: new("169.254.0.1"),
				PeerAsn:       proto.Uint32(64512),
			},
		},
	}
	edges, proxies := routerEdges("p", selfLink, router, map[string]bool{})
	// USES_NETWORK + CONTAINS(nat) + PEERED_WITH(bgp) = 3
	require.Len(t, edges, 3)
	assert.Equal(t, kgtypes.EdgeUsesNetwork, edges[0].Relationship)

	assert.Equal(t, kgtypes.EdgeContains, edges[1].Relationship)
	assert.Equal(t, selfLink+"/nats/my-nat", edges[1].TargetID)

	assert.Equal(t, kgtypes.EdgePeeredWith, edges[2].Relationship)
	assert.Equal(t, "gcp:bgp-peer:169.254.0.1", edges[2].TargetID)
	assert.Equal(t, "169.254.0.1", edges[2].Metadata["peer_ip"])
	assert.Equal(t, "64512", edges[2].Metadata["peer_asn"])
	assert.Equal(t, "peer-1", edges[2].Metadata["peer_name"])

	// Proxy resource lands so the dangling edge target resolves.
	require.Len(t, proxies, 1)
	assert.Equal(t, "gcp:bgp-peer:169.254.0.1", proxies[0].ID)
	assert.Equal(t, "169.254.0.1", proxies[0].Name)
	assert.Equal(t, "gcp:bgp:peer", proxies[0].ResourceType)
	assert.Equal(t, "false", proxies[0].Metadata["collected"])
	assert.Equal(t, "64512", proxies[0].Metadata["peer_asn"])
}

func TestRouterEdges_BGPPeerSeenProxiesDedupes(t *testing.T) {
	// Two routers in the same collect pass that share an external BGP peer
	// emit two PEERED_WITH edges but only one proxy node.
	selfLinkA := "https://www.googleapis.com/compute/v1/projects/p/regions/us-central1/routers/router-a"
	selfLinkB := "https://www.googleapis.com/compute/v1/projects/p/regions/us-central1/routers/router-b"
	mkRouter := func(name, link string) *computepb.Router {
		return &computepb.Router{
			Name:     new(name),
			Network:  new("https://www.googleapis.com/compute/v1/projects/p/global/networks/vpc"),
			SelfLink: new(link),
			BgpPeers: []*computepb.RouterBgpPeer{
				{PeerIpAddress: new("169.254.0.99"), PeerAsn: proto.Uint32(64600)},
			},
		}
	}
	seen := map[string]bool{}
	edgesA, proxiesA := routerEdges("p", selfLinkA, mkRouter("a", selfLinkA), seen)
	edgesB, proxiesB := routerEdges("p", selfLinkB, mkRouter("b", selfLinkB), seen)

	require.Len(t, edgesA, 2) // USES_NETWORK + PEERED_WITH
	require.Len(t, edgesB, 2)
	require.Len(t, proxiesA, 1, "first router emits the proxy")
	assert.Empty(t, proxiesB, "shared seenProxies suppresses duplicate proxy")
}

func TestRouterEdges_NoBgpPeers(t *testing.T) {
	selfLink := "https://www.googleapis.com/compute/v1/projects/p/regions/us-central1/routers/r"
	router := &computepb.Router{
		Name:     new("r"),
		Network:  new("https://www.googleapis.com/compute/v1/projects/p/global/networks/vpc"),
		SelfLink: new(selfLink),
	}
	edges, proxies := routerEdges("p", selfLink, router, map[string]bool{})
	require.Len(t, edges, 1)
	assert.Empty(t, proxies)
	assert.Equal(t, kgtypes.EdgeUsesNetwork, edges[0].Relationship)
}

func TestRouterResourceSpec_NoBgp(t *testing.T) {
	selfLink := "https://www.googleapis.com/compute/v1/projects/p/regions/us-central1/routers/simple-router"
	router := &computepb.Router{
		Name:     new("simple-router"),
		Region:   new("https://www.googleapis.com/compute/v1/projects/p/regions/us-central1"),
		SelfLink: new(selfLink),
	}

	content := []byte(`{"name":"simple-router"}`)
	spec := routerResourceSpec(selfLink, router, content)

	assert.Equal(t, selfLink, spec.ID)
	assert.Equal(t, "simple-router", spec.Name)
	assert.Equal(t, "gcp:compute:router", spec.ResourceType)
	assert.Equal(t, "us-central1", spec.Region)
	// BGP fields should not be present.
	_, hasBgpAsn := spec.Metadata["bgpAsn"]
	assert.False(t, hasBgpAsn)
}
