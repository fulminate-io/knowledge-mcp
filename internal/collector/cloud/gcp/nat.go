// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"encoding/json"
	"fmt"
	"strings"

	computepb "cloud.google.com/go/compute/apiv1/computepb"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// --- Wire structs (FUL-88: curated content envelope for cloud nat) ---

// routerNatContent is the curated wire shape for gcp:compute:nat.
// Field set frozen in Phase 1 audit (session ful-88-gcp-planning).
// (No SelfLink — RouterNat has no self-link; the GCP API treats them
// as embedded sub-resources of Router.)
type routerNatContent struct {
	Name                             string   `json:"name,omitempty"`
	NatIpAllocateOption              string   `json:"natIpAllocateOption,omitempty"`
	SourceSubnetworkIpRangesToNat    string   `json:"sourceSubnetworkIpRangesToNat,omitempty"`
	EnableDynamicPortAllocation      *bool    `json:"enableDynamicPortAllocation,omitempty"`
	EnableEndpointIndependentMapping *bool    `json:"enableEndpointIndependentMapping,omitempty"`
	NatIps                           []string `json:"natIps,omitempty"`
}

// buildRouterNatContent projects a *computepb.RouterNat into the curated wire shape.
func buildRouterNatContent(n *computepb.RouterNat) routerNatContent {
	out := routerNatContent{
		Name:                          n.GetName(),
		NatIpAllocateOption:           n.GetNatIpAllocateOption(),
		SourceSubnetworkIpRangesToNat: n.GetSourceSubnetworkIpRangesToNat(),
		NatIps:                        n.GetNatIps(),
	}
	if n.EnableDynamicPortAllocation != nil {
		b := *n.EnableDynamicPortAllocation
		out.EnableDynamicPortAllocation = &b
	}
	if n.EnableEndpointIndependentMapping != nil {
		b := *n.EnableEndpointIndependentMapping
		out.EnableEndpointIndependentMapping = &b
	}
	return out
}

// collectRouterNATs extracts NAT configurations nested inside a Router proto
// and appends them to result. Cloud NAT is not a separately-listable
// resource family on the GCP API — the configs live as repeated fields on
// Router. Called from routerSubCollector.Collect so we don't issue a second
// AggregatedListRouters round-trip the way a separate sub-collector would.
func collectRouterNATs(
	result *cloud.SubCollectorResult,
	router *computepb.Router,
	routerSelfLink string,
) error {
	for _, nat := range router.GetNats() {
		natName := nat.GetName()
		if natName == "" {
			continue
		}

		// NAT configs don't have their own self-link; compose a unique ID.
		natID := routerSelfLink + "/nats/" + natName

		content, err := json.Marshal(buildRouterNatContent(nat))
		if err != nil {
			return fmt.Errorf("gcp cloud nat: marshal nat content: %w", err)
		}

		spec := natResourceSpec(natID, natName, router, nat, content)
		result.Resources = append(result.Resources, spec)
		result.Edges = append(result.Edges, natEdges(natID, routerSelfLink, router)...)
	}
	return nil
}

// natResourceSpec builds a ResourceSpec for a Cloud NAT configuration.
func natResourceSpec(
	natID, natName string,
	router *computepb.Router,
	nat *computepb.RouterNat,
	content []byte,
) cloud.ResourceSpec {
	meta := map[string]string{
		"router":                           router.GetName(),
		"natIpAllocateOption":              nat.GetNatIpAllocateOption(),
		"sourceSubnetworkIpRangesToNat":    nat.GetSourceSubnetworkIpRangesToNat(),
		"enableDynamicPortAllocation":      boolStr(nat.GetEnableDynamicPortAllocation()),
		"enableEndpointIndependentMapping": boolStr(nat.GetEnableEndpointIndependentMapping()),
	}

	if ips := nat.GetNatIps(); len(ips) > 0 {
		meta["natIps"] = strings.Join(ips, ",")
	}

	return cloud.ResourceSpec{
		ID:           natID,
		Name:         natName,
		ResourceType: "gcp:compute:nat",
		Region:       extractLast(router.GetRegion()),
		Content:      content,
		Metadata:     meta,
	}
}

// natEdges extracts edges from a Cloud NAT config: ROUTES_VIA to the parent
// router and USES_NETWORK to the router's VPC network.
func natEdges(natID, routerSelfLink string, router *computepb.Router) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec

	// NAT -> parent router.
	edges = append(edges, cloud.EdgeSpec{
		SourceID:     natID,
		TargetID:     routerSelfLink,
		Relationship: kgtypes.EdgeRoutesVia,
	})

	// NAT -> VPC network (inherited from the router).
	if network := router.GetNetwork(); network != "" {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     natID,
			TargetID:     network,
			Relationship: kgtypes.EdgeUsesNetwork,
		})
	}

	return edges
}
