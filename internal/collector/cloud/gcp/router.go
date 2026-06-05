// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	compute "cloud.google.com/go/compute/apiv1"
	computepb "cloud.google.com/go/compute/apiv1/computepb"
	"google.golang.org/api/iterator"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// --- Wire structs (curated content envelope for routers) ---

// routerContent is the curated wire shape for gcp:compute:router.
// Field set frozen in the Phase 1 audit.
type routerContent struct {
	Name     string                 `json:"name,omitempty"`
	SelfLink string                 `json:"selfLink,omitempty"`
	Region   string                 `json:"region,omitempty"`
	Network  string                 `json:"network,omitempty"`
	Bgp      *routerContentBgp      `json:"bgp,omitempty"`
	Nats     []routerContentNat     `json:"nats,omitempty"`
	BgpPeers []routerContentBgpPeer `json:"bgpPeers,omitempty"`
}

type routerContentBgp struct {
	Asn                *uint32                          `json:"asn,omitempty"`
	AdvertiseMode      string                           `json:"advertiseMode,omitempty"`
	AdvertisedIpRanges []routerContentAdvertisedIPRange `json:"advertisedIpRanges,omitempty"`
}

type routerContentAdvertisedIPRange struct {
	Range string `json:"range,omitempty"`
}

type routerContentNat struct {
	Name string `json:"name,omitempty"`
}

type routerContentBgpPeer struct {
	Name          string  `json:"name,omitempty"`
	PeerIpAddress string  `json:"peerIpAddress,omitempty"`
	PeerAsn       *uint32 `json:"peerAsn,omitempty"`
}

// buildRouterContent projects a *computepb.Router into the curated wire shape.
func buildRouterContent(r *computepb.Router) routerContent {
	out := routerContent{
		Name:     r.GetName(),
		SelfLink: r.GetSelfLink(),
		Region:   r.GetRegion(),
		Network:  r.GetNetwork(),
	}
	out.Bgp = projectRouterBgp(r.GetBgp())
	for _, n := range r.GetNats() {
		if n == nil {
			continue
		}
		out.Nats = append(out.Nats, routerContentNat{Name: n.GetName()})
	}
	for _, p := range r.GetBgpPeers() {
		if p == nil {
			continue
		}
		peer := routerContentBgpPeer{
			Name:          p.GetName(),
			PeerIpAddress: p.GetPeerIpAddress(),
		}
		if p.PeerAsn != nil {
			a := *p.PeerAsn
			peer.PeerAsn = &a
		}
		out.BgpPeers = append(out.BgpPeers, peer)
	}
	return out
}

func projectRouterBgp(b *computepb.RouterBgp) *routerContentBgp {
	if b == nil {
		return nil
	}
	out := &routerContentBgp{
		AdvertiseMode: b.GetAdvertiseMode(),
	}
	if b.Asn != nil {
		a := *b.Asn
		out.Asn = &a
	}
	for _, r := range b.GetAdvertisedIpRanges() {
		if r == nil {
			continue
		}
		out.AdvertisedIpRanges = append(out.AdvertisedIpRanges, routerContentAdvertisedIPRange{
			Range: r.GetRange(),
		})
	}
	return out
}

// routerSubCollector collects Cloud Router resources across all regions.
type routerSubCollector struct {
	client    *compute.RoutersClient
	projectID string
}

func newRouterSubCollector(client *compute.RoutersClient, projectID string) *routerSubCollector {
	return &routerSubCollector{client: client, projectID: projectID}
}

func (c *routerSubCollector) Name() string { return "gcp-routers" }

func (c *routerSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var result cloud.SubCollectorResult
	seenProxies := map[string]bool{}

	it := c.client.AggregatedList(ctx, &computepb.AggregatedListRoutersRequest{
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

		for _, router := range pair.Value.GetRouters() {
			selfLink := router.GetSelfLink()
			if selfLink == "" {
				continue
			}

			content, err := json.Marshal(buildRouterContent(router))
			if err != nil {
				return result, fmt.Errorf("gcp routers: marshal router content: %w", err)
			}

			spec := routerResourceSpec(selfLink, router, content)
			result.Resources = append(result.Resources, spec)
			edges, proxies := routerEdges(c.projectID, selfLink, router, seenProxies)
			result.Edges = append(result.Edges, edges...)
			result.Resources = append(result.Resources, proxies...)

			// Cloud NAT lives as nested data on Router; collect inline so we
			// don't issue a second AggregatedListRouters round-trip.
			if err := collectRouterNATs(&result, router, selfLink); err != nil {
				return result, err
			}
		}
	}

	return result, nil
}

// routerResourceSpec builds a ResourceSpec for a Cloud Router.
func routerResourceSpec(selfLink string, router *computepb.Router, content []byte) cloud.ResourceSpec {
	meta := map[string]string{
		"region": extractLast(router.GetRegion()),
	}

	if bgp := router.GetBgp(); bgp != nil {
		meta["bgpAsn"] = fmt.Sprintf("%d", bgp.GetAsn())
		meta["advertiseMode"] = bgp.GetAdvertiseMode()

		ranges := bgp.GetAdvertisedIpRanges()
		if len(ranges) > 0 {
			parts := make([]string, 0, len(ranges))
			for _, r := range ranges {
				parts = append(parts, r.GetRange())
			}
			meta["advertisedIpRanges"] = strings.Join(parts, ",")
		}
	}

	return cloud.ResourceSpec{
		ID:           selfLink,
		Name:         router.GetName(),
		ResourceType: "gcp:compute:router",
		Region:       extractLast(router.GetRegion()),
		Content:      content,
		Metadata:     meta,
	}
}

// bgpPeerSentinelID returns the synthetic graph ID for an external BGP peer
// referenced only by IP. External peers (on-prem, third-party clouds) cannot
// be canonicalized to a managed GCP resource ID, so they get a sentinel node
// — same shape as gcpCIDRSentinelID for un-canonicalizable CIDR ranges.
func bgpPeerSentinelID(peerIP string) string {
	return "gcp:bgp-peer:" + peerIP
}

// routerEdges extracts edges from a Cloud Router and returns proxy resources
// for any synthetic edge targets:
//
//   - EdgeUsesNetwork from router → VPC network.
//   - EdgeContains from router → each Cloud NAT config. NAT IDs use
//     routerSelfLink + "/nats/" + natName (matching cloud/gcp/nat.go).
//   - EdgePeeredWith from router → each BGP peer (sentinel node), with peer
//     IP, ASN, and name in edge metadata. The peer IP is not a canonical
//     graph ID, so each unique peer also produces a proxy ResourceSpec so
//     graph traversal lands on a real node. seenProxies dedupes across
//     routers that share an external peer.
func routerEdges(
	_, selfLink string, router *computepb.Router, seenProxies map[string]bool,
) ([]cloud.EdgeSpec, []cloud.ResourceSpec) {
	var edges []cloud.EdgeSpec
	var proxies []cloud.ResourceSpec

	// Router -> VPC network.
	if network := router.GetNetwork(); network != "" {
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     selfLink,
			TargetID:     network,
			Relationship: kgtypes.EdgeUsesNetwork,
		})
	}

	// Router -> NAT configs (CONTAINS).
	for _, nat := range router.GetNats() {
		natName := nat.GetName()
		if natName == "" {
			continue
		}
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     selfLink,
			TargetID:     selfLink + "/nats/" + natName,
			Relationship: kgtypes.EdgeContains,
		})
	}

	// Router -> BGP peers (PEERED_WITH + sentinel proxy).
	for _, peer := range router.GetBgpPeers() {
		peerIP := peer.GetPeerIpAddress()
		if peerIP == "" {
			continue
		}
		peerID := bgpPeerSentinelID(peerIP)
		meta := map[string]string{"peer_ip": peerIP}
		if name := peer.GetName(); name != "" {
			meta["peer_name"] = name
		}
		if asn := peer.GetPeerAsn(); asn != 0 {
			meta["peer_asn"] = fmt.Sprintf("%d", asn)
		}
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     selfLink,
			TargetID:     peerID,
			Relationship: kgtypes.EdgePeeredWith,
			Metadata:     meta,
		})

		if seenProxies != nil && !seenProxies[peerID] {
			seenProxies[peerID] = true
			proxyMeta := map[string]string{
				"collected":        "false",
				"collected_reason": "external BGP peer (no GCP-managed counterpart)",
				"discovered_via":   "cloud router bgp peer",
				"peer_ip":          peerIP,
			}
			if asn := peer.GetPeerAsn(); asn != 0 {
				proxyMeta["peer_asn"] = fmt.Sprintf("%d", asn)
			}
			proxies = append(proxies, cloud.ResourceSpec{
				ID:           peerID,
				Name:         peerIP,
				ResourceType: "gcp:bgp:peer",
				Metadata:     proxyMeta,
			})
		}
	}

	return edges, proxies
}
