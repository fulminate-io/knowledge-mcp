// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// resolveIngressCloudLBLinkage emits EdgeExposedBy edges from K8s Ingress
// resources to cross-graph proxies of the cloud LB (GCP forwardingRule or
// AWS ELB) that realizes the Ingress's Status.LoadBalancer.Ingress[].
//
// Mirrors the Phase 3 Service resolver shape, with two intentional
// differences:
//   - No Spec.Type check. Ingress has no equivalent: if Status.LoadBalancer
//     carries at least one entry, the controller already realized it.
//   - Uses the shared resolveLBEntryByAddress helper (factored during
//     Phase 4) so the per-entry resolution is literally one line. Phase 5
//     (Gateway) reuses the same helper with a different Content decoder.
//
// The flow:
//  1. Build the shared GCP + AWS indexes. If BOTH are empty, skip — no
//     cloud graph loaded, so nothing to link (silent no-op).
//  2. Query every NodeCloudResource with resource_type=Ingress. An explicit
//     resource_type filter is enforced by the metadata predicate in the executor.
//  3. Per Ingress: decode Content, walk Status.LoadBalancer.Ingress[].
//     Empty list → Warn (controller has not realized the LB yet). One
//     EdgeExposedBy per (ingress, proxy) pair, dedup'd by seen map.
func resolveIngressCloudLBLinkage(ctx context.Context, gc postpopulate.GraphCaller, graphName string) error {
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

	ingresses, err := postpopulate.BrowseNodes(ctx, gc, kgtypes.GraphCloud, graphName, k8sResourceQuery("Ingress"))
	if err != nil {
		return err
	}

	seen := make(map[string]struct{})
	proxies := newProxyAccumulator()
	var edges []knowledgev1.Edge
	for _, n := range ingresses {
		ingEdges, err := buildIngressCloudLBEdges(n, gcpIndex, awsIndex, seen, proxies)
		if err != nil {
			return err
		}
		edges = append(edges, ingEdges...)
	}

	if len(edges) == 0 {
		return nil
	}
	if err := postpopulate.LinkNodesAndEdgesBatch(ctx, gc, kgtypes.GraphCloud, graphName, proxies.nodes(), edges); err != nil {
		slog.Debug("postPopulate: failed to create EXPOSED_BY edges",
			"count", len(edges), "err", err)
		return err
	}
	slog.Debug("postPopulate: created EXPOSED_BY edges from Ingresses",
		"count", len(edges))
	return nil
}

// buildIngressCloudLBEdges decodes one Ingress's Content and emits
// EdgeExposedBy edges for any Status.LoadBalancer.Ingress entries that
// match a cloud LB index. Returns nil when the Ingress has malformed
// Content. Emits a Warn log (and zero edges) when the Status.LoadBalancer
// is empty — the controller has not yet populated the address.
func buildIngressCloudLBEdges(n *knowledgev1.Node, gcpIndex, awsIndex map[string][]cloudLBRef, seen map[string]struct{}, proxies *proxyAccumulator) ([]knowledgev1.Edge, error) {
	ic, ok := decodeIngressContent(n)
	if !ok {
		return nil, nil
	}
	if len(ic.Status.LoadBalancer.Ingress) == 0 {
		warnIngressStalStatus(n, ic)
		return nil, nil
	}

	var edges []knowledgev1.Edge
	for _, ing := range ic.Status.LoadBalancer.Ingress {
		ingEdges, err := resolveLBEntryByAddress(n, ing.IP, ing.Hostname, gcpIndex, awsIndex, seen, proxies)
		if err != nil {
			return nil, err
		}
		edges = append(edges, ingEdges...)
	}
	return edges, nil
}

// warnIngressStalStatus mirrors warnServiceStalStatus: same Warn field
// shape (resource, kind, namespace, age) so operators can grep/aggregate
// "LB not realized yet" across Service/Ingress/Gateway uniformly.
func warnIngressStalStatus(n *knowledgev1.Node, ic ingressContent) {
	slog.Warn("postPopulate: Ingress has no Status.LoadBalancer.Ingress yet",
		"ingress", n.Id,
		"kind", "Ingress",
		"namespace", kgtypes.Value(n, "namespace"),
		"age", resourceAge(ic.Metadata.CreationTimestamp))
}

// --- Content decoding ---------------------------------------------------

// ingressContent is the minimal shape we need from an Ingress's Content
// JSON. K8s Ingress uses the same IngressLoadBalancerStatus shape as a
// LoadBalancer Service — `.Status.LoadBalancer.Ingress[]{IP, Hostname}` —
// so the decoder shape mirrors serviceContent except there is no Spec.Type.
type ingressContent struct {
	Metadata struct {
		CreationTimestamp string `json:"creationTimestamp,omitempty"`
	} `json:"metadata"`
	Status struct {
		LoadBalancer struct {
			Ingress []ingressLBStatusLite `json:"ingress,omitempty"`
		} `json:"loadBalancer"`
	} `json:"status"`
}

// ingressLBStatusLite mirrors networkingv1.IngressLoadBalancerIngress but
// only the two fields that can match a cloud LB: IP (GCP) and Hostname
// (AWS). Separate type from serviceIngressLite to keep shape-kind greppable
// ("which resolver owns this Content type?").
type ingressLBStatusLite struct {
	IP       string `json:"ip,omitempty"`
	Hostname string `json:"hostname,omitempty"`
}

// decodeIngressContent decodes the Ingress's Content JSON. Returns
// ok=false on empty or malformed Content — callers log-and-skip.
func decodeIngressContent(n *knowledgev1.Node) (ingressContent, bool) {
	var ic ingressContent
	if len(n.Content) == 0 {
		return ic, false
	}
	if err := json.Unmarshal([]byte(n.Content), &ic); err != nil {
		return ic, false
	}
	return ic, true
}
