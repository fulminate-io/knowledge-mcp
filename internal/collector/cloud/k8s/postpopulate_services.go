// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
)

// resolveServiceCloudLBLinkage emits EdgeExposedBy edges from LoadBalancer
// Services to cross-graph proxies of the cloud LB (GCP forwardingRule or AWS
// ELB) that realizes the Service's external address.
//
// The flow:
//  1. Build the shared GCP + AWS indexes. If BOTH are empty, skip — there is
//     no cloud graph loaded, so nothing to link (silent no-op, matches the
//     existing cluster-linkage pattern for non-GKE kubeconfig flows).
//  2. Query every NodeCloudResource with resource_type=Service. An explicit
//     resource_type filter is enforced by the metadata predicate in the executor.
//  3. For each Service: only LoadBalancer type with at least one
//     Status.LoadBalancer.Ingress entry is processed. Warn logs at
//     resource_id + kind + namespace + age when a LoadBalancer Service has
//     no Ingress yet — Phase 4 (Ingress) mirrors this format.
//  4. Per Ingress entry: lowercase IP → GCP index first; lowercase Hostname
//     → AWS index as fallback. On match, upsert a cross-graph proxy and
//     append one EdgeExposedBy edge. Dedup by (serviceID, proxyID).
func resolveServiceCloudLBLinkage(ctx context.Context, gc postpopulate.GraphCaller, graphName string) error {
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

	services, err := postpopulate.BrowseAllNodes(ctx, gc, kgtypes.GraphCloud, graphName, k8sResourceQuery("Service"))
	if err != nil {
		return err
	}

	seen := make(map[string]struct{})
	proxies := newProxyAccumulator()
	var edges []knowledgev1.Edge
	for _, n := range services {
		svcEdges, err := buildServiceCloudLBEdges(n, gcpIndex, awsIndex, seen, proxies)
		if err != nil {
			return err
		}
		edges = append(edges, svcEdges...)
	}

	if len(edges) == 0 {
		return nil
	}
	if err := postpopulate.LinkNodesAndEdgesBatch(ctx, gc, kgtypes.GraphCloud, graphName, proxies.nodes(), edges); err != nil {
		slog.Debug("postPopulate: failed to create EXPOSED_BY edges",
			"count", len(edges), "err", err)
		return err
	}
	slog.Debug("postPopulate: created EXPOSED_BY edges from Services",
		"count", len(edges))
	return nil
}

// buildServiceCloudLBEdges decodes one Service's Content and emits
// EdgeExposedBy edges for any Ingress entries that match a cloud LB index.
// Returns nil when the Service is not LoadBalancer-typed or has malformed
// Content. Emits a Warn log (and zero edges) when a LoadBalancer-typed
// Service has an empty Status.LoadBalancer.Ingress.
func buildServiceCloudLBEdges(n *knowledgev1.Node, gcpIndex, awsIndex map[string][]cloudLBRef, seen map[string]struct{}, proxies *proxyAccumulator) ([]knowledgev1.Edge, error) {
	sc, ok := decodeServiceContent(n)
	if !ok {
		return nil, nil
	}
	if !strings.EqualFold(sc.Spec.Type, "LoadBalancer") {
		return nil, nil
	}
	if len(sc.Status.LoadBalancer.Ingress) == 0 {
		warnServiceStalStatus(n, sc)
		return nil, nil
	}

	var edges []knowledgev1.Edge
	for _, ing := range sc.Status.LoadBalancer.Ingress {
		ingEdges, err := resolveServiceIngress(n, ing, gcpIndex, awsIndex, seen, proxies)
		if err != nil {
			return nil, err
		}
		edges = append(edges, ingEdges...)
	}
	return edges, nil
}

// resolveServiceIngress resolves a single Status.LoadBalancer.Ingress entry
// via the shared resolveLBEntryByAddress helper (see
// postpopulate_cloud_lb_index.go). Returns an empty slice when nothing
// matches or when every candidate (service, proxy) pair has already been
// emitted in this resolver pass.
func resolveServiceIngress(svc *knowledgev1.Node, ing serviceIngressLite, gcpIndex, awsIndex map[string][]cloudLBRef, seen map[string]struct{}, proxies *proxyAccumulator) ([]knowledgev1.Edge, error) {
	return resolveLBEntryByAddress(svc, ing.IP, ing.Hostname, gcpIndex, awsIndex, seen, proxies)
}

// emitCloudLBEdge upserts the cross-graph proxy for ref and appends the
// EdgeExposedBy edge to out, returning the (possibly unchanged) slice.
// Dedupes (from, to) pairs via seen — two Ingress entries that resolve to
// the same LB collapse to a single edge (the duplicate leaves out
// unchanged). evidence captures which lookup key (ip=… / hostname=…)
// matched so operators can audit mis-linkages without re-running the
// resolver. The edge is a fresh knowledgev1.Edge literal at the append site so
// the embedded proto lock is never copied.
func emitCloudLBEdge(out []knowledgev1.Edge, from *knowledgev1.Node, ref cloudLBRef, provider, evidence string, seen map[string]struct{}, proxies *proxyAccumulator) ([]knowledgev1.Edge, error) {
	source := cloudLBProxySource(ref.NodeID, provider)
	proxyID, err := proxies.proxy(&knowledgev1.ProxyTarget{
		GraphType: string(kgtypes.GraphCloud),
		Name:      ref.GraphName,
		NodeId:    ref.NodeID,
	}, source)
	if err != nil {
		return out, err
	}
	key := from.Id + "→" + proxyID
	if _, dup := seen[key]; dup {
		return out, nil
	}
	seen[key] = struct{}{}
	return append(out, knowledgev1.Edge{
		FromId:   from.Id,
		ToId:     proxyID,
		Type:     string(kgtypes.EdgeExposedBy),
		Method:   provider,
		Evidence: evidence,
	}), nil
}

// cloudLBProxySource is the display-only source Node passed to
// BuildCrossGraphProxy. provider picks the resource_type stamp so proxy
// inspection surfaces "gcp:compute:forwardingRule" or the AWS ELB type
// without a follow-up lookup.
func cloudLBProxySource(nodeID, provider string) *knowledgev1.Node {
	name := lastSegmentAfterSlash(nodeID)
	rt := ""
	switch provider {
	case "gcp":
		rt = gcpForwardingRuleResourceType
	case "aws":
		// The AWS index may have matched either elbv2-loadbalancer or
		// elb-loadbalancer. The proxy just needs a reasonable default --
		// the authoritative resource_type lives on the real node in the
		// AWS cloud graph, and ProxyInfo/UI resolution will surface it.
		rt = "elbv2-loadbalancer"
	}
	src := &knowledgev1.Node{
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: name,
		Source:     "cloud",
		Summary:    rt + " " + name,
	}
	kgtypes.SetValue(src, "provider", provider)
	if rt != "" {
		kgtypes.SetValue(src, "resource_type", rt)
	}
	return src
}

// warnServiceStalStatus logs a single Warn for a LoadBalancer-typed Service
// whose Status.LoadBalancer.Ingress has not yet been populated by the cloud
// controller. Phase 4 (Ingress resolver) and Phase 5 (Gateway resolver)
// mirror this field set — parent plan mandates a uniform warn shape so
// operators can grep/aggregate "LB not realized yet" across
// Service/Ingress/Gateway.
//
// Fields: service (node ID) + kind + namespace + age. age is derived from
// metadata.creationTimestamp when present, "unknown" otherwise.
func warnServiceStalStatus(n *knowledgev1.Node, sc serviceContent) {
	slog.Warn("postPopulate: LoadBalancer Service has no Status.LoadBalancer.Ingress yet",
		"service", n.Id,
		"kind", "Service",
		"namespace", kgtypes.Value(n, "namespace"),
		"age", resourceAge(sc.Metadata.CreationTimestamp))
}

// resourceAge parses metadata.creationTimestamp and returns the age as a
// human-readable duration string. Returns "unknown" when the field is
// missing or unparseable. Not Service-specific — shared with the Ingress
// (Phase 4) and Gateway (Phase 5) resolvers for uniform warn formatting.
func resourceAge(creationTimestamp string) string {
	if creationTimestamp == "" {
		return "unknown"
	}
	t, err := time.Parse(time.RFC3339, creationTimestamp)
	if err != nil {
		return "unknown"
	}
	return time.Since(t).Truncate(time.Second).String()
}

// --- Content decoding ---------------------------------------------------

// serviceContent is the minimal shape we need from a Service's Content
// JSON. Keep tiny — the full corev1.Service shape is huge and we only need
// Spec.Type + Status.LoadBalancer.Ingress[] + metadata.creationTimestamp.
type serviceContent struct {
	Metadata struct {
		CreationTimestamp string `json:"creationTimestamp,omitempty"`
	} `json:"metadata"`
	Spec struct {
		Type string `json:"type,omitempty"`
	} `json:"spec"`
	Status struct {
		LoadBalancer struct {
			Ingress []serviceIngressLite `json:"ingress,omitempty"`
		} `json:"loadBalancer"`
	} `json:"status"`
}

// serviceIngressLite mirrors corev1.LoadBalancerIngress but only the two
// fields that can match a cloud LB: IP (GCP) and Hostname (AWS).
type serviceIngressLite struct {
	IP       string `json:"ip,omitempty"`
	Hostname string `json:"hostname,omitempty"`
}

// decodeServiceContent decodes the Service's Content JSON. Returns
// ok=false on empty or malformed Content — callers log-and-skip.
func decodeServiceContent(n *knowledgev1.Node) (serviceContent, bool) {
	var sc serviceContent
	if len(n.Content) == 0 {
		return sc, false
	}
	if err := json.Unmarshal([]byte(n.Content), &sc); err != nil {
		return sc, false
	}
	return sc, true
}
