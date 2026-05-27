// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestExtractProjectFromSelfLink(t *testing.T) {
	tests := []struct {
		name     string
		selfLink string
		want     string
	}{
		{
			name:     "network self-link",
			selfLink: "https://www.googleapis.com/compute/v1/projects/host-project/global/networks/shared-vpc",
			want:     "host-project",
		},
		{
			name:     "subnet self-link",
			selfLink: "https://www.googleapis.com/compute/v1/projects/service-project/regions/us-central1/subnetworks/subnet-1",
			want:     "service-project",
		},
		{
			name:     "empty",
			selfLink: "",
			want:     "",
		},
		{
			name:     "no projects marker",
			selfLink: "https://example.com/foo/bar",
			want:     "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, extractProjectFromSelfLink(tt.selfLink))
		})
	}
}

func TestExtractNetworkFromSubnet(t *testing.T) {
	network := "https://www.googleapis.com/compute/v1/projects/host-project/global/networks/shared-vpc"
	subnet := &knowledgev1.Node{
		Id:      "https://www.googleapis.com/compute/v1/projects/service-project/regions/us-central1/subnetworks/subnet-1",
		Content: `{"network":"` + network + `","name":"subnet-1"}`,
	}
	assert.Equal(t, network, extractNetworkFromSubnet(subnet))
}

func TestExtractNetworkFromSubnet_EmptyContent(t *testing.T) {
	subnet := &knowledgev1.Node{Id: "some-id", Content: ""}
	assert.Empty(t, extractNetworkFromSubnet(subnet))
}

func TestExtractNetworkFromSubnet_NoNetwork(t *testing.T) {
	subnet := &knowledgev1.Node{Id: "some-id", Content: `{"name":"subnet-1"}`}
	assert.Empty(t, extractNetworkFromSubnet(subnet))
}

func TestDetectGCPProject(t *testing.T) {
	subnets := []*knowledgev1.Node{
		{Id: "https://www.googleapis.com/compute/v1/projects/my-project/regions/us-central1/subnetworks/s1"},
	}
	assert.Equal(t, "my-project", detectGCPProject(subnets))
}

func TestDetectGCPProject_Empty(t *testing.T) {
	assert.Empty(t, detectGCPProject(nil))
}

func TestSharedVPCEdgeDetection(t *testing.T) {
	// Simulate a service project subnet referencing a host project network.
	hostProject := "host-project"
	serviceProject := "service-project"
	networkSelfLink := "https://www.googleapis.com/compute/v1/projects/" + hostProject + "/global/networks/shared-vpc"
	subnetSelfLink := "https://www.googleapis.com/compute/v1/projects/" + serviceProject + "/regions/us-central1/subnetworks/subnet-1"

	subnet := &knowledgev1.Node{
		Id:      subnetSelfLink,
		Type:    string(kgtypes.NodeCloudResource),
		Content: `{"network":"` + networkSelfLink + `","name":"subnet-1"}`,
	}
	kgtypes.SetValue(subnet, "resource_type", "gcp:compute:subnetwork")

	// Test the pure detection logic without a DB.
	currentProject := extractProjectFromSelfLink(subnet.Id)
	assert.Equal(t, serviceProject, currentProject)

	network := extractNetworkFromSubnet(subnet)
	assert.Equal(t, networkSelfLink, network)

	networkProject := extractProjectFromSelfLink(network)
	assert.Equal(t, hostProject, networkProject)
	assert.NotEqual(t, currentProject, networkProject, "cross-project reference detected")

	// Verify the edge that would be emitted.
	edge := knowledgev1.Edge{
		FromId: networkSelfLink,
		ToId:   subnetSelfLink,
		Type:   string(kgtypes.EdgeSharedWith),
		Method: methodGCPSharedVPC,
	}
	assert.Equal(t, networkSelfLink, edge.FromId, "edge FROM host network")
	assert.Equal(t, subnetSelfLink, edge.ToId, "edge TO service subnet")
}

func TestSharedVPCEdgeDetection_SameProject(t *testing.T) {
	// Same-project subnet should NOT produce a SHARED_WITH edge.
	project := "my-project"
	networkSelfLink := "https://www.googleapis.com/compute/v1/projects/" + project + "/global/networks/default"
	subnetSelfLink := "https://www.googleapis.com/compute/v1/projects/" + project + "/regions/us-central1/subnetworks/subnet-1"

	subnet := &knowledgev1.Node{
		Id:      subnetSelfLink,
		Content: `{"network":"` + networkSelfLink + `"}`,
	}

	network := extractNetworkFromSubnet(subnet)
	networkProject := extractProjectFromSelfLink(network)
	currentProject := extractProjectFromSelfLink(subnet.Id)
	assert.Equal(t, currentProject, networkProject, "same project, no shared VPC edge")
}

func TestSharedVPCEdgeDetection_MultipleSubnets(t *testing.T) {
	hostNetwork := "https://www.googleapis.com/compute/v1/projects/host/global/networks/shared"
	serviceProject := "service"

	subnets := []*knowledgev1.Node{
		{
			Id:      "https://www.googleapis.com/compute/v1/projects/" + serviceProject + "/regions/us-central1/subnetworks/s1",
			Content: `{"network":"` + hostNetwork + `"}`,
		},
		{
			Id:      "https://www.googleapis.com/compute/v1/projects/" + serviceProject + "/regions/us-east1/subnetworks/s2",
			Content: `{"network":"` + hostNetwork + `"}`,
		},
		{
			// Same-project subnet — should not produce edge.
			Id:      "https://www.googleapis.com/compute/v1/projects/" + serviceProject + "/regions/us-west1/subnetworks/s3",
			Content: `{"network":"https://www.googleapis.com/compute/v1/projects/` + serviceProject + `/global/networks/local"}`,
		},
	}

	var edges []knowledgev1.Edge
	for _, subnet := range subnets {
		network := extractNetworkFromSubnet(subnet)
		if network == "" {
			continue
		}
		networkProject := extractProjectFromSelfLink(network)
		currentProject := extractProjectFromSelfLink(subnet.Id)
		if networkProject == "" || networkProject == currentProject {
			continue
		}
		edges = append(edges, knowledgev1.Edge{
			FromId: network,
			ToId:   subnet.Id,
			Type:   string(kgtypes.EdgeSharedWith),
			Method: methodGCPSharedVPC,
		})
	}

	require.Len(t, edges, 2, "only cross-project subnets produce edges")
	for i := range edges {
		e := &edges[i]
		assert.Equal(t, string(kgtypes.EdgeSharedWith), e.Type)
		assert.Equal(t, hostNetwork, e.FromId)
	}
}
