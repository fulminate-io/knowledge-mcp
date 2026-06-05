// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	compute "cloud.google.com/go/compute/apiv1"
	computepb "cloud.google.com/go/compute/apiv1/computepb"
	"google.golang.org/api/iterator"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// --- Wire structs (curated content envelopes, no SDK leak) ---

// networkContent is the curated wire shape for gcp:compute:network. Field set
// frozen in Phase 1 audit (session ful-88-gcp-planning).
type networkContent struct {
	Name                  string                  `json:"name,omitempty"`
	SelfLink              string                  `json:"selfLink,omitempty"`
	AutoCreateSubnetworks *bool                   `json:"autoCreateSubnetworks,omitempty"`
	RoutingConfig         *networkRoutingConfig   `json:"routingConfig,omitempty"`
	Peerings              []networkPeeringContent `json:"peerings,omitempty"`
}

type networkRoutingConfig struct {
	RoutingMode string `json:"routingMode,omitempty"`
}

type networkPeeringContent struct {
	Name    string `json:"name,omitempty"`
	Network string `json:"network,omitempty"`
	State   string `json:"state,omitempty"`
}

// subnetworkContent is the curated wire shape for gcp:compute:subnetwork.
// Field set frozen in Phase 1 audit. The `Network` field is read by
// postpopulate_sharedvpc.go (Phase 3 reader convergence).
type subnetworkContent struct {
	Name           string                      `json:"name,omitempty"`
	SelfLink       string                      `json:"selfLink,omitempty"`
	Region         string                      `json:"region,omitempty"`
	Network        string                      `json:"network,omitempty"`
	IpCidrRange    string                      `json:"ipCidrRange,omitempty"`
	Purpose        string                      `json:"purpose,omitempty"`
	EnableFlowLogs *bool                       `json:"enableFlowLogs,omitempty"`
	LogConfig      *subnetworkLogConfigContent `json:"logConfig,omitempty"`
}

type subnetworkLogConfigContent struct {
	Enable              *bool    `json:"enable,omitempty"`
	FlowSampling        *float32 `json:"flowSampling,omitempty"`
	AggregationInterval string   `json:"aggregationInterval,omitempty"`
}

// firewallContent is the curated wire shape for gcp:compute:firewall.
// Convergence target for postpopulate_firewall.go reader (Phase 3).
// Pointer fields preserved where the reader checks for nil distinction.
// IPProtocol JSON tag uses the acronym uppercase form (NOT ipProtocol) —
// existing readers depend on this exact spelling.
type firewallContent struct {
	Name                  string                   `json:"name,omitempty"`
	SelfLink              string                   `json:"selfLink,omitempty"`
	Direction             *string                  `json:"direction,omitempty"`
	Disabled              *bool                    `json:"disabled,omitempty"`
	Network               *string                  `json:"network,omitempty"`
	Priority              int32                    `json:"priority,omitempty"`
	TargetTags            []string                 `json:"targetTags,omitempty"`
	TargetServiceAccounts []string                 `json:"targetServiceAccounts,omitempty"`
	SourceRanges          []string                 `json:"sourceRanges,omitempty"`
	SourceTags            []string                 `json:"sourceTags,omitempty"`
	SourceServiceAccounts []string                 `json:"sourceServiceAccounts,omitempty"`
	DestinationRanges     []string                 `json:"destinationRanges,omitempty"`
	Allowed               []firewallContentAllowed `json:"allowed,omitempty"`
}

type firewallContentAllowed struct {
	IPProtocol string   `json:"IPProtocol,omitempty"`
	Ports      []string `json:"ports,omitempty"`
}

// --- Projectors ---

// buildNetworkContent projects a *computepb.Network into the curated wire shape.
func buildNetworkContent(n *computepb.Network) networkContent {
	out := networkContent{
		Name:     n.GetName(),
		SelfLink: n.GetSelfLink(),
	}
	if n.AutoCreateSubnetworks != nil {
		b := *n.AutoCreateSubnetworks
		out.AutoCreateSubnetworks = &b
	}
	out.RoutingConfig = projectNetworkRoutingConfig(n.GetRoutingConfig())
	for _, p := range n.GetPeerings() {
		if p == nil {
			continue
		}
		out.Peerings = append(out.Peerings, networkPeeringContent{
			Name:    p.GetName(),
			Network: p.GetNetwork(),
			State:   p.GetState(),
		})
	}
	return out
}

func projectNetworkRoutingConfig(rc *computepb.NetworkRoutingConfig) *networkRoutingConfig {
	if rc == nil {
		return nil
	}
	mode := rc.GetRoutingMode()
	if mode == "" {
		return nil
	}
	return &networkRoutingConfig{RoutingMode: mode}
}

// buildSubnetworkContent projects a *computepb.Subnetwork into the curated wire shape.
func buildSubnetworkContent(s *computepb.Subnetwork) subnetworkContent {
	out := subnetworkContent{
		Name:        s.GetName(),
		SelfLink:    s.GetSelfLink(),
		Region:      s.GetRegion(),
		Network:     s.GetNetwork(),
		IpCidrRange: s.GetIpCidrRange(),
		Purpose:     s.GetPurpose(),
	}
	if s.EnableFlowLogs != nil {
		b := *s.EnableFlowLogs
		out.EnableFlowLogs = &b
	}
	out.LogConfig = projectSubnetworkLogConfig(s.GetLogConfig())
	return out
}

func projectSubnetworkLogConfig(lc *computepb.SubnetworkLogConfig) *subnetworkLogConfigContent {
	if lc == nil {
		return nil
	}
	out := &subnetworkLogConfigContent{
		AggregationInterval: lc.GetAggregationInterval(),
	}
	if lc.Enable != nil {
		b := *lc.Enable
		out.Enable = &b
	}
	if lc.FlowSampling != nil {
		f := *lc.FlowSampling
		out.FlowSampling = &f
	}
	return out
}

// buildFirewallContent projects a *computepb.Firewall into the curated wire shape.
func buildFirewallContent(f *computepb.Firewall) firewallContent {
	out := firewallContent{
		Name:                  f.GetName(),
		SelfLink:              f.GetSelfLink(),
		Priority:              f.GetPriority(),
		TargetTags:            f.GetTargetTags(),
		TargetServiceAccounts: f.GetTargetServiceAccounts(),
		SourceRanges:          f.GetSourceRanges(),
		SourceTags:            f.GetSourceTags(),
		SourceServiceAccounts: f.GetSourceServiceAccounts(),
		DestinationRanges:     f.GetDestinationRanges(),
	}
	if f.Direction != nil {
		s := *f.Direction
		out.Direction = &s
	}
	if f.Disabled != nil {
		b := *f.Disabled
		out.Disabled = &b
	}
	if f.Network != nil {
		s := *f.Network
		out.Network = &s
	}
	for _, a := range f.GetAllowed() {
		if a == nil {
			continue
		}
		out.Allowed = append(out.Allowed, firewallContentAllowed{
			IPProtocol: a.GetIPProtocol(),
			Ports:      a.GetPorts(),
		})
	}
	return out
}

// networksSubCollector collects VPC networks.
type networksSubCollector struct {
	client    *compute.NetworksClient
	projectID string
}

func newNetworksSubCollector(client *compute.NetworksClient, projectID string) *networksSubCollector {
	return &networksSubCollector{client: client, projectID: projectID}
}

func (c *networksSubCollector) Name() string { return "gcp-networks" }

func (c *networksSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var result cloud.SubCollectorResult

	it := c.client.List(ctx, &computepb.ListNetworksRequest{
		Project: c.projectID,
	})

	for {
		network, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return result, err
		}

		selfLink := network.GetSelfLink()
		if selfLink == "" {
			continue
		}

		content, err := json.Marshal(buildNetworkContent(network))
		if err != nil {
			return result, fmt.Errorf("gcp networks: marshal network content: %w", err)
		}

		spec := cloud.ResourceSpec{
			ID:           selfLink,
			Name:         network.GetName(),
			ResourceType: "gcp:compute:network",
			Content:      content,
			Metadata: map[string]string{
				"autoCreateSubnetworks": boolStr(network.GetAutoCreateSubnetworks()),
				"routingMode":           network.GetRoutingConfig().GetRoutingMode(),
			},
		}
		result.Resources = append(result.Resources, spec)
		result.Edges = append(result.Edges, networkPeeringEdges(selfLink, network)...)
	}

	return result, nil
}

// networkPeeringEdges extracts PEERED_WITH edges from active VPC peerings.
func networkPeeringEdges(selfLink string, network *computepb.Network) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec
	for _, peering := range network.GetPeerings() {
		if peering.GetState() != "ACTIVE" {
			continue
		}
		peerNetwork := peering.GetNetwork()
		if peerNetwork == "" {
			continue
		}
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     selfLink,
			TargetID:     peerNetwork,
			Relationship: kgtypes.EdgePeeredWith,
			Metadata: map[string]string{
				"peering_name": peering.GetName(),
			},
		})
	}
	return edges
}

// subnetsSubCollector collects VPC subnetworks across all regions.
type subnetsSubCollector struct {
	client    *compute.SubnetworksClient
	projectID string
}

func newSubnetsSubCollector(client *compute.SubnetworksClient, projectID string) *subnetsSubCollector {
	return &subnetsSubCollector{client: client, projectID: projectID}
}

func (c *subnetsSubCollector) Name() string { return "gcp-subnets" }

func (c *subnetsSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var result cloud.SubCollectorResult

	it := c.client.AggregatedList(ctx, &computepb.AggregatedListSubnetworksRequest{
		Project: c.projectID,
	})

	for {
		pair, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return result, err
		}

		for _, subnet := range pair.Value.GetSubnetworks() {
			selfLink := subnet.GetSelfLink()
			if selfLink == "" {
				continue
			}

			content, err := json.Marshal(buildSubnetworkContent(subnet))
			if err != nil {
				return result, fmt.Errorf("gcp subnets: marshal subnetwork content: %w", err)
			}

			spec := cloud.ResourceSpec{
				ID:           selfLink,
				Name:         subnet.GetName(),
				ResourceType: "gcp:compute:subnetwork",
				Region:       extractLast(subnet.GetRegion()),
				Content:      content,
				Metadata:     subnetMetadata(subnet),
			}
			result.Resources = append(result.Resources, spec)

			// Subnet -> parent VPC network.
			if network := subnet.GetNetwork(); network != "" {
				result.Edges = append(result.Edges, cloud.EdgeSpec{
					SourceID:     selfLink,
					TargetID:     network,
					Relationship: kgtypes.EdgeUsesNetwork,
				})
			}
		}
	}

	return result, nil
}

// firewallsSubCollector collects VPC firewall rules.
type firewallsSubCollector struct {
	client    *compute.FirewallsClient
	projectID string
}

func newFirewallsSubCollector(client *compute.FirewallsClient, projectID string) *firewallsSubCollector {
	return &firewallsSubCollector{client: client, projectID: projectID}
}

func (c *firewallsSubCollector) Name() string { return "gcp-firewalls" }

func (c *firewallsSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var result cloud.SubCollectorResult

	it := c.client.List(ctx, &computepb.ListFirewallsRequest{
		Project: c.projectID,
	})

	for {
		fw, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return result, err
		}

		selfLink := fw.GetSelfLink()
		if selfLink == "" {
			continue
		}

		content, err := json.Marshal(buildFirewallContent(fw))
		if err != nil {
			return result, fmt.Errorf("gcp firewalls: marshal firewall content: %w", err)
		}

		spec := cloud.ResourceSpec{
			ID:           selfLink,
			Name:         fw.GetName(),
			ResourceType: "gcp:compute:firewall",
			Content:      content,
			Metadata: map[string]string{
				"direction": fw.GetDirection(),
				"priority":  intStr(fw.GetPriority()),
				"disabled":  boolStr(fw.GetDisabled()),
			},
		}
		result.Resources = append(result.Resources, spec)

		// Firewall -> VPC network.
		if network := fw.GetNetwork(); network != "" {
			result.Edges = append(result.Edges, cloud.EdgeSpec{
				SourceID:     selfLink,
				TargetID:     network,
				Relationship: kgtypes.EdgeUsesNetwork,
			})
		}
	}

	return result, nil
}

// subnetMetadata builds searchable metadata for a Subnetwork, including
// VPC Flow Log configuration (flowLogEnabled, flowLogSampleRate,
// flowLogAggregationInterval) when available.
func subnetMetadata(subnet *computepb.Subnetwork) map[string]string {
	meta := map[string]string{
		"ipCidrRange":    subnet.GetIpCidrRange(),
		"purpose":        subnet.GetPurpose(),
		"flowLogEnabled": boolStr(subnet.GetEnableFlowLogs()),
	}
	if lc := subnet.GetLogConfig(); lc != nil {
		if lc.GetEnable() {
			meta["flowLogEnabled"] = "true"
		}
		if rate := lc.GetFlowSampling(); rate > 0 {
			meta["flowLogSampleRate"] = strconv.FormatFloat(float64(rate), 'f', -1, 32)
		}
		if ai := lc.GetAggregationInterval(); ai != "" {
			meta["flowLogAggregationInterval"] = ai
		}
	}
	return meta
}

// boolStr converts a bool to "true" or "false".
func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// intStr converts an int32 to its string representation.
func intStr(i int32) string {
	return strconv.FormatInt(int64(i), 10)
}
