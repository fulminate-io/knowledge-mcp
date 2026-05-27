// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func buildFirewallNode(t *testing.T, id string, spec firewallContent) *knowledgev1.Node { //nolint:unparam // id varies in future tests
	t.Helper()
	content, err := json.Marshal(spec)
	require.NoError(t, err)
	n := &knowledgev1.Node{
		Id:         id,
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: id,
		Content:    string(content),
	}
	kgtypes.SetValue(n, "resource_type", "gcp:compute:firewall")
	return n
}

func buildInstanceNode(t *testing.T, id string, tags []string, saEmails []string, network string) *knowledgev1.Node {
	t.Helper()
	spec := instanceSpec{
		NetworkInterfaces: []instanceNIC{{Network: network}},
	}
	if len(tags) > 0 {
		spec.Tags = &instanceTags{Items: tags}
	}
	for _, email := range saEmails {
		spec.ServiceAccounts = append(spec.ServiceAccounts, instanceSA{Email: email})
	}
	content, err := json.Marshal(spec)
	require.NoError(t, err)
	n := &knowledgev1.Node{
		Id:         id,
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: id,
		Content:    string(content),
	}
	kgtypes.SetValue(n, "resource_type", "gcp:compute:instance")
	return n
}

func collectFWEdgeKeys(edges []knowledgev1.Edge) map[string]*knowledgev1.Edge {
	out := make(map[string]*knowledgev1.Edge, len(edges))
	for i := range edges {
		e := &edges[i]
		key := e.FromId + "->" + e.ToId + ":" + e.Type
		out[key] = e
	}
	return out
}

func TestFirewall_IngressCIDR(t *testing.T) {
	network := "https://www.googleapis.com/compute/v1/projects/p/global/networks/vpc-1"
	fw := buildFirewallNode(t, "fw-1", firewallContent{
		Direction:    new("INGRESS"),
		Network:      &network,
		TargetTags:   []string{"web"},
		SourceRanges: []string{"0.0.0.0/0"},
		Allowed:      []firewallContentAllowed{{IPProtocol: "tcp", Ports: []string{"443"}}},
	})
	inst := buildInstanceNode(t, "inst-1", []string{"web"}, nil, network)

	idx := buildInstanceIndex([]*knowledgev1.Node{inst})
	edges, cidrs := buildFirewallEdges([]*knowledgev1.Node{fw}, idx)

	require.Len(t, edges, 1)
	require.Len(t, cidrs, 1)
	assert.Equal(t, "0.0.0.0/0", cidrs["gcp:cidr:0.0.0.0/0"])

	e := &edges[0]
	assert.Equal(t, string(kgtypes.EdgeAllowsIngressFrom), e.Type)
	assert.Equal(t, "inst-1", e.FromId)
	assert.Equal(t, "gcp:cidr:0.0.0.0/0", e.ToId)
	assert.Equal(t, methodGCPFirewall, e.Method)
	assert.Contains(t, e.Evidence, `"protocol":"tcp"`)
}

func TestFirewall_EgressCIDR(t *testing.T) {
	network := "https://www.googleapis.com/compute/v1/projects/p/global/networks/vpc-1"
	fw := buildFirewallNode(t, "fw-1", firewallContent{
		Direction:         new("EGRESS"),
		Network:           &network,
		TargetTags:        []string{"web"},
		DestinationRanges: []string{"10.0.0.0/8"},
		Allowed:           []firewallContentAllowed{{IPProtocol: "tcp", Ports: []string{"80"}}},
	})
	inst := buildInstanceNode(t, "inst-1", []string{"web"}, nil, network)

	idx := buildInstanceIndex([]*knowledgev1.Node{inst})
	edges, cidrs := buildFirewallEdges([]*knowledgev1.Node{fw}, idx)

	require.Len(t, edges, 1)
	assert.Equal(t, "10.0.0.0/8", cidrs["gcp:cidr:10.0.0.0/8"])

	e := &edges[0]
	assert.Equal(t, string(kgtypes.EdgeAllowsEgressTo), e.Type)
	assert.Equal(t, "inst-1", e.FromId)
	assert.Equal(t, "gcp:cidr:10.0.0.0/8", e.ToId)
	assert.Contains(t, e.Evidence, `"egress":true`)
}

func TestFirewall_SourceTags_InstanceToInstance(t *testing.T) {
	network := "https://www.googleapis.com/compute/v1/projects/p/global/networks/vpc-1"
	fw := buildFirewallNode(t, "fw-1", firewallContent{
		Direction:  new("INGRESS"),
		Network:    &network,
		TargetTags: []string{"web"},
		SourceTags: []string{"backend"},
		Allowed:    []firewallContentAllowed{{IPProtocol: "tcp", Ports: []string{"8080"}}},
	})
	web := buildInstanceNode(t, "inst-web", []string{"web"}, nil, network)
	backend := buildInstanceNode(t, "inst-backend", []string{"backend"}, nil, network)

	idx := buildInstanceIndex([]*knowledgev1.Node{web, backend})
	edges, cidrs := buildFirewallEdges([]*knowledgev1.Node{fw}, idx)

	assert.Empty(t, cidrs, "no CIDR sentinels for tag-based rules")
	require.Len(t, edges, 1)

	e := &edges[0]
	assert.Equal(t, string(kgtypes.EdgeAllowsIngressFrom), e.Type)
	assert.Equal(t, "inst-web", e.FromId)
	assert.Equal(t, "inst-backend", e.ToId)
}

func TestFirewall_NoTargets_AllVPCInstances(t *testing.T) {
	network := "https://www.googleapis.com/compute/v1/projects/p/global/networks/vpc-1"
	fw := buildFirewallNode(t, "fw-1", firewallContent{
		Direction:    new("INGRESS"),
		Network:      &network,
		SourceRanges: []string{"0.0.0.0/0"},
		Allowed:      []firewallContentAllowed{{IPProtocol: "tcp", Ports: []string{"22"}}},
		// No TargetTags or TargetServiceAccounts -> matches ALL in VPC.
	})
	inst1 := buildInstanceNode(t, "inst-1", nil, nil, network)
	inst2 := buildInstanceNode(t, "inst-2", nil, nil, network)
	other := buildInstanceNode(t, "inst-other", nil, nil, "other-network")

	idx := buildInstanceIndex([]*knowledgev1.Node{inst1, inst2, other})
	edges, _ := buildFirewallEdges([]*knowledgev1.Node{fw}, idx)

	// Should match inst-1 and inst-2 (same VPC), but not inst-other.
	require.Len(t, edges, 2)
	byKey := collectFWEdgeKeys(edges)
	assert.Contains(t, byKey, "inst-1->gcp:cidr:0.0.0.0/0:ALLOWS_INGRESS_FROM")
	assert.Contains(t, byKey, "inst-2->gcp:cidr:0.0.0.0/0:ALLOWS_INGRESS_FROM")
}

func TestFirewall_DisabledSkipped(t *testing.T) {
	network := "https://www.googleapis.com/compute/v1/projects/p/global/networks/vpc-1"
	fw := buildFirewallNode(t, "fw-1", firewallContent{
		Direction:    new("INGRESS"),
		Disabled:     new(true),
		Network:      &network,
		SourceRanges: []string{"0.0.0.0/0"},
		Allowed:      []firewallContentAllowed{{IPProtocol: "tcp"}},
	})
	inst := buildInstanceNode(t, "inst-1", nil, nil, network)

	idx := buildInstanceIndex([]*knowledgev1.Node{inst})
	edges, cidrs := buildFirewallEdges([]*knowledgev1.Node{fw}, idx)

	assert.Empty(t, edges, "disabled firewall should be skipped")
	assert.Empty(t, cidrs)
}

func TestFirewall_DenyOnlySkipped(t *testing.T) {
	network := "https://www.googleapis.com/compute/v1/projects/p/global/networks/vpc-1"
	// Firewall with no Allowed rules (deny-only) should be skipped.
	fw := buildFirewallNode(t, "fw-1", firewallContent{
		Direction:    new("INGRESS"),
		Network:      &network,
		SourceRanges: []string{"0.0.0.0/0"},
		// No Allowed rules
	})
	inst := buildInstanceNode(t, "inst-1", nil, nil, network)

	idx := buildInstanceIndex([]*knowledgev1.Node{inst})
	edges, cidrs := buildFirewallEdges([]*knowledgev1.Node{fw}, idx)

	assert.Empty(t, edges, "deny-only firewall should be skipped")
	assert.Empty(t, cidrs)
}

func TestFirewall_ServiceAccountTargeting(t *testing.T) {
	network := "https://www.googleapis.com/compute/v1/projects/p/global/networks/vpc-1"
	fw := buildFirewallNode(t, "fw-1", firewallContent{
		Direction:             new("INGRESS"),
		Network:               &network,
		TargetServiceAccounts: []string{"web@project.iam.gserviceaccount.com"},
		SourceRanges:          []string{"10.0.0.0/8"},
		Allowed:               []firewallContentAllowed{{IPProtocol: "tcp", Ports: []string{"443"}}},
	})
	inst := buildInstanceNode(t, "inst-1", nil,
		[]string{"web@project.iam.gserviceaccount.com"}, network)
	other := buildInstanceNode(t, "inst-2", nil,
		[]string{"other@project.iam.gserviceaccount.com"}, network)

	idx := buildInstanceIndex([]*knowledgev1.Node{inst, other})
	edges, _ := buildFirewallEdges([]*knowledgev1.Node{fw}, idx)

	require.Len(t, edges, 1, "only inst-1 matches the target SA")
	assert.Equal(t, "inst-1", edges[0].FromId)
}

func TestFirewall_EmptyContent(t *testing.T) {
	n := &knowledgev1.Node{
		Id:   "fw-empty",
		Type: string(kgtypes.NodeCloudResource),
	}
	kgtypes.SetValue(n, "resource_type", "gcp:compute:firewall")
	edges, cidrs := buildFirewallEdges([]*knowledgev1.Node{n}, instanceIndex{
		byTag:     make(map[string][]instanceRef),
		bySA:      make(map[string][]instanceRef),
		byNetwork: make(map[string][]instanceRef),
	})
	assert.Empty(t, edges)
	assert.Empty(t, cidrs)
}

func TestFirewall_MalformedContent(t *testing.T) {
	n := &knowledgev1.Node{
		Id:      "fw-bad",
		Type:    string(kgtypes.NodeCloudResource),
		Content: "not json",
	}
	kgtypes.SetValue(n, "resource_type", "gcp:compute:firewall")
	edges, cidrs := buildFirewallEdges([]*knowledgev1.Node{n}, instanceIndex{
		byTag:     make(map[string][]instanceRef),
		bySA:      make(map[string][]instanceRef),
		byNetwork: make(map[string][]instanceRef),
	})
	assert.Empty(t, edges)
	assert.Empty(t, cidrs)
}
