// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"encoding/json"
	"log/slog"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
)

// resolveGatewayCloudLBLinkage emits EdgeExposedBy edges from K8s Gateway
// (gateway.networking.k8s.io/v1) resources to cross-graph proxies of the
// cloud LB (GCP forwardingRule or AWS ELB) that realizes the Gateway's
// Status.Addresses[].
//
// Mirrors the Phase 3 Service / Phase 4 Ingress resolvers, with one
// structural difference in the Content shape:
//   - Service/Ingress: Status.LoadBalancer.Ingress[]{IP, Hostname} — both
//     fields may populate simultaneously, so the shared resolver consults
//     GCP IP first and falls back to AWS hostname.
//   - Gateway: Status.Addresses[]{Type, Value} — Type is a discriminator
//     ("IPAddress" | "Hostname" | implementation-specific). The value goes
//     into exactly one resolver input slot based on Type. Implementation-
//     specific types are silently skipped (the Gateway API spec allows
//     implementations to define their own Address types, and those are not
//     portable cloud LB identifiers).
//
// The flow:
//  1. Build the shared GCP + AWS indexes. If BOTH are empty, silent no-op —
//     no cloud graph loaded, nothing to link.
//  2. Query every NodeCloudResource with resource_type=Gateway. An explicit
//     resource_type filter is enforced by the metadata predicate in the executor.
//  3. Per Gateway: decode Content, walk Status.Addresses[]. Empty list →
//     Warn (controller has not populated addresses yet; same shape as the
//     Service/Ingress stale-status warnings so operators can grep uniformly).
//     One EdgeExposedBy per (gateway, proxy) pair, dedup'd by seen map.
func resolveGatewayCloudLBLinkage(ctx context.Context, gc postpopulate.GraphCaller, graphName string) error {
	gcpIndex, err := buildGCPForwardingRuleIndex(ctx, gc)
	if err != nil {
		return err
	}
	awsIndex, err := buildAWSELBDNSIndex(ctx, gc)
	if err != nil {
		return err
	}
	if len(gcpIndex) == 0 && len(awsIndex) == 0 {
		// No cloud graphs (or none carry matching LBs) — silent no-op.
		return nil
	}

	gateways, err := postpopulate.BrowseAllNodes(ctx, gc, kgtypes.GraphCloud, graphName, k8sResourceQuery("Gateway"))
	if err != nil {
		return err
	}

	seen := make(map[string]struct{})
	proxies := newProxyAccumulator()
	var edges []knowledgev1.Edge
	for _, n := range gateways {
		gwEdges, err := buildGatewayCloudLBEdges(n, gcpIndex, awsIndex, seen, proxies)
		if err != nil {
			return err
		}
		edges = append(edges, gwEdges...)
	}

	if len(edges) == 0 {
		return nil
	}
	if err := postpopulate.LinkNodesAndEdgesBatch(ctx, gc, kgtypes.GraphCloud, graphName, proxies.nodes(), edges); err != nil {
		slog.Debug("postPopulate: failed to create EXPOSED_BY edges",
			"count", len(edges), "err", err)
		return err
	}
	slog.Debug("postPopulate: created EXPOSED_BY edges from Gateways",
		"count", len(edges))
	return nil
}

// buildGatewayCloudLBEdges decodes one Gateway's Content and emits
// EdgeExposedBy edges for any Status.Addresses entries that match a cloud
// LB index. Returns nil when the Gateway has malformed Content. Emits a
// Warn log (and zero edges) when the Status.Addresses list is empty — the
// controller has not yet populated addresses.
//
// The Type discriminator splits each address into exactly one lookup:
// IPAddress → GCP IP index, Hostname → AWS DNS index. Implementation-
// specific types (e.g. "NamedAddress") are silently skipped — they do not
// correspond to a portable cloud LB identifier.
func buildGatewayCloudLBEdges(n *knowledgev1.Node, gcpIndex, awsIndex map[string][]cloudLBRef, seen map[string]struct{}, proxies *proxyAccumulator) ([]knowledgev1.Edge, error) {
	gwc, ok := decodeGatewayContent(n)
	if !ok {
		return nil, nil
	}
	if len(gwc.Status.Addresses) == 0 {
		warnGatewayStalStatus(n, gwc)
		return nil, nil
	}

	var edges []knowledgev1.Edge
	for _, addr := range gwc.Status.Addresses {
		addrEdges, err := resolveGatewayAddress(n, addr, gcpIndex, awsIndex, seen, proxies)
		if err != nil {
			return nil, err
		}
		edges = append(edges, addrEdges...)
	}
	return edges, nil
}

// resolveGatewayAddress routes one Status.Addresses entry to the shared
// resolveLBEntryByAddress helper based on addr.Type. Silently returns
// (nil, nil) for implementation-specific types so they don't count as
// misses or log spam — the Gateway API spec explicitly allows custom
// Address types and we only know how to resolve the two standard ones.
func resolveGatewayAddress(n *knowledgev1.Node, addr gatewayAddressLite, gcpIndex, awsIndex map[string][]cloudLBRef, seen map[string]struct{}, proxies *proxyAccumulator) ([]knowledgev1.Edge, error) {
	switch addr.Type {
	case "IPAddress":
		return resolveLBEntryByAddress(n, addr.Value, "", gcpIndex, awsIndex, seen, proxies)
	case "Hostname":
		return resolveLBEntryByAddress(n, "", addr.Value, gcpIndex, awsIndex, seen, proxies)
	default:
		// implementation-specific types (per Gateway API spec) — skip.
		return nil, nil
	}
}

// warnGatewayStalStatus mirrors warnServiceStalStatus / warnIngressStalStatus:
// same Warn field shape (resource, kind, namespace, age) so operators can
// grep/aggregate "LB not realized yet" uniformly across
// Service/Ingress/Gateway.
func warnGatewayStalStatus(n *knowledgev1.Node, gc gatewayContent) {
	slog.Warn("postPopulate: Gateway has no Status.Addresses yet",
		"gateway", n.Id,
		"kind", "Gateway",
		"namespace", kgtypes.Value(n, "namespace"),
		"age", resourceAge(gc.Metadata.CreationTimestamp))
}

// --- Content decoding ---------------------------------------------------

// gatewayContent is the minimal shape we need from a Gateway's Content
// JSON. Gateway API v1 defines Status.Addresses[] as
// []GatewayStatusAddress{Type *AddressType, Value string}. Type is a
// pointer-to-string in the typed API but JSON-encodes as a plain string
// field; decoding to `string` here is safe because we only ever read it
// as an exact-match discriminator.
//
// Keep tiny — the full Gateway spec is huge and we only need
// Status.Addresses[] + metadata.creationTimestamp for the stale-status
// warning.
type gatewayContent struct {
	Metadata struct {
		CreationTimestamp string `json:"creationTimestamp,omitempty"`
	} `json:"metadata"`
	Status struct {
		Addresses []gatewayAddressLite `json:"addresses,omitempty"`
	} `json:"status"`
}

// gatewayAddressLite mirrors gatewayv1.GatewayStatusAddress but only the
// two fields the resolver consults. Separate type from serviceIngressLite
// / ingressLBStatusLite to keep shape-kind greppable ("which resolver
// owns this Content type?").
type gatewayAddressLite struct {
	Type  string `json:"type,omitempty"`
	Value string `json:"value,omitempty"`
}

// decodeGatewayContent decodes the Gateway's Content JSON. Returns
// ok=false on empty or malformed Content — callers log-and-skip.
func decodeGatewayContent(n *knowledgev1.Node) (gatewayContent, bool) {
	var gc gatewayContent
	if len(n.Content) == 0 {
		return gc, false
	}
	if err := json.Unmarshal([]byte(n.Content), &gc); err != nil {
		return gc, false
	}
	return gc, true
}
