// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	computepb "cloud.google.com/go/compute/apiv1/computepb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestNATEdges_WithNetwork(t *testing.T) {
	routerSelfLink := "https://www.googleapis.com/compute/v1/projects/p/regions/us-central1/routers/my-router"
	natID := routerSelfLink + "/nats/my-nat"
	router := &computepb.Router{
		Name:     new("my-router"),
		Network:  new("https://www.googleapis.com/compute/v1/projects/p/global/networks/my-vpc"),
		Region:   new("https://www.googleapis.com/compute/v1/projects/p/regions/us-central1"),
		SelfLink: new(routerSelfLink),
	}

	edges := natEdges(natID, routerSelfLink, router)
	assert.Len(t, edges, 2)

	// First edge: NAT -> parent router via ROUTES_VIA.
	assert.Equal(t, natID, edges[0].SourceID)
	assert.Equal(t, routerSelfLink, edges[0].TargetID)
	assert.Equal(t, kgtypes.EdgeRoutesVia, edges[0].Relationship)

	// Second edge: NAT -> VPC network via USES_NETWORK.
	assert.Equal(t, natID, edges[1].SourceID)
	assert.Equal(t, router.GetNetwork(), edges[1].TargetID)
	assert.Equal(t, kgtypes.EdgeUsesNetwork, edges[1].Relationship)
}

func TestNATEdges_NoNetwork(t *testing.T) {
	routerSelfLink := "https://www.googleapis.com/compute/v1/projects/p/regions/us-central1/routers/my-router"
	natID := routerSelfLink + "/nats/my-nat"
	router := &computepb.Router{
		Name:     new("my-router"),
		Region:   new("https://www.googleapis.com/compute/v1/projects/p/regions/us-central1"),
		SelfLink: new(routerSelfLink),
	}

	edges := natEdges(natID, routerSelfLink, router)
	assert.Len(t, edges, 1)

	// Only ROUTES_VIA to parent router — no network edge.
	assert.Equal(t, natID, edges[0].SourceID)
	assert.Equal(t, routerSelfLink, edges[0].TargetID)
	assert.Equal(t, kgtypes.EdgeRoutesVia, edges[0].Relationship)
}

func TestNATResourceSpec(t *testing.T) {
	routerSelfLink := "https://www.googleapis.com/compute/v1/projects/p/regions/us-central1/routers/my-router"
	natID := routerSelfLink + "/nats/my-nat"
	router := &computepb.Router{
		Name:   new("my-router"),
		Region: new("https://www.googleapis.com/compute/v1/projects/p/regions/us-central1"),
	}
	nat := &computepb.RouterNat{
		Name:                             new("my-nat"),
		NatIpAllocateOption:              new("AUTO_ONLY"),
		SourceSubnetworkIpRangesToNat:    new("ALL_SUBNETWORKS_ALL_IP_RANGES"),
		EnableDynamicPortAllocation:      new(true),
		EnableEndpointIndependentMapping: new(false),
		NatIps: []string{
			"https://www.googleapis.com/compute/v1/projects/p/regions/us-central1/addresses/nat-ip-1",
		},
	}

	content := []byte(`{"name":"my-nat"}`)
	spec := natResourceSpec(natID, "my-nat", router, nat, content)

	assert.Equal(t, natID, spec.ID)
	assert.Equal(t, "my-nat", spec.Name)
	assert.Equal(t, "gcp:compute:nat", spec.ResourceType)
	assert.Equal(t, "us-central1", spec.Region)
	assert.Equal(t, "my-router", spec.Metadata["router"])
	assert.Equal(t, "AUTO_ONLY", spec.Metadata["natIpAllocateOption"])
	assert.Equal(t, "ALL_SUBNETWORKS_ALL_IP_RANGES", spec.Metadata["sourceSubnetworkIpRangesToNat"])
	assert.Equal(t, "true", spec.Metadata["enableDynamicPortAllocation"])
	assert.Equal(t, "false", spec.Metadata["enableEndpointIndependentMapping"])
	assert.Contains(t, spec.Metadata["natIps"], "nat-ip-1")
}

func TestCollectNATConfigs_WithNats(t *testing.T) {
	routerSelfLink := "https://www.googleapis.com/compute/v1/projects/p/regions/us-central1/routers/my-router"
	router := &computepb.Router{
		Name:     new("my-router"),
		Network:  new("https://www.googleapis.com/compute/v1/projects/p/global/networks/my-vpc"),
		Region:   new("https://www.googleapis.com/compute/v1/projects/p/regions/us-central1"),
		SelfLink: new(routerSelfLink),
		Nats: []*computepb.RouterNat{
			{Name: new("nat-config-1")},
			{Name: new("nat-config-2")},
		},
	}

	var result cloud.SubCollectorResult
	require.NoError(t, collectRouterNATs(&result, router, routerSelfLink))

	assert.Len(t, result.Resources, 2)
	assert.Equal(t, routerSelfLink+"/nats/nat-config-1", result.Resources[0].ID)
	assert.Equal(t, routerSelfLink+"/nats/nat-config-2", result.Resources[1].ID)

	// Each NAT config produces 2 edges (ROUTES_VIA + USES_NETWORK).
	assert.Len(t, result.Edges, 4)
}

func TestCollectNATConfigs_NoNats(t *testing.T) {
	routerSelfLink := "https://www.googleapis.com/compute/v1/projects/p/regions/us-central1/routers/my-router"
	router := &computepb.Router{
		Name:     new("my-router"),
		SelfLink: new(routerSelfLink),
	}

	var result cloud.SubCollectorResult
	require.NoError(t, collectRouterNATs(&result, router, routerSelfLink))

	assert.Empty(t, result.Resources)
	assert.Empty(t, result.Edges)
}

func TestCollectNATConfigs_EmptyName(t *testing.T) {
	routerSelfLink := "https://www.googleapis.com/compute/v1/projects/p/regions/us-central1/routers/my-router"
	router := &computepb.Router{
		Name:     new("my-router"),
		SelfLink: new(routerSelfLink),
		Nats: []*computepb.RouterNat{
			{Name: new("")}, // empty name — should be skipped
			{Name: new("valid-nat")},
		},
	}

	var result cloud.SubCollectorResult
	require.NoError(t, collectRouterNATs(&result, router, routerSelfLink))

	// Only the valid-nat should produce resources.
	assert.Len(t, result.Resources, 1)
	assert.Equal(t, "valid-nat", result.Resources[0].Name)
}
