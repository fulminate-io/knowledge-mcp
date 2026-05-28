// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"encoding/json"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
)

// Phase 2 of the K8s → cloud-LB linkage plan. These helpers build sibling
// indexes off the loaded cloud graphs so the per-kind resolvers
// (postpopulate_services.go, postpopulate_ingress.go, postpopulate_gateway.go)
// can map a Service / Ingress / Gateway address to the GCP forwardingRule or
// AWS ELB that realizes it, and emit EdgeExposedBy edges to a cross-graph
// proxy of the matching node.
//
// The K8s package is not allowed to import cloud/aws or cloud/gcp, so the
// minimal Content decoder structs (one field each) are duplicated here. The
// alternative — exporting them — would couple cloud/k8s to two sibling
// domain packages just to read a single string field per node, which the
// store/-as-base-of-the-pyramid layering rule already nudges us away from.

// cloudLBRef carries the (graph, node) pair Phase 3/4/5 needs to call
// crossgraph.BuildCrossGraphProxy. The lookup key (lowercase IP for GCP,
// lowercase DNS name for AWS) maps to a SLICE of refs because a single
// address can legitimately back multiple cloud LB resources:
//   - GCP forwardingRules are keyed on (IP, port, protocol) — a GKE
//     Gateway normally has one forwardingRule per protocol sharing a
//     single VIP (one for HTTP:80, one for HTTPS:443). Collapsing to a
//     single ref per IP would drop all but one.
//   - AWS DNS is typically 1:1 per ELB but weighted Route 53 records or
//     alias chains can legitimately multi-map. Same data shape works for
//     both; the slice cost is negligible (bounded by N LBs per graph).
//
// Each ref in the slice gets its own EXPOSED_BY edge at resolution time.
type cloudLBRef struct {
	GraphName string
	NodeID    string
}

// gcpForwardingRuleResourceType is the resource_type stamped on every
// forwardingRule node by cloud/gcp/loadbalancer.go.
const gcpForwardingRuleResourceType = "gcp:compute:forwardingRule"

// awsELBResourceTypes enumerates the resource_type values an AWS ELB node may
// carry. cloud/aws/elbv2.go currently emits only "elbv2-loadbalancer" — the
// classic ELB collector is not yet implemented but its expected resource_type
// "elb-loadbalancer" is included here so this helper picks them up the moment
// they land without a follow-up edit.
var awsELBResourceTypes = []string{
	"elbv2-loadbalancer",
	"elb-loadbalancer",
}

// gcpForwardingRuleContent mirrors the single field this resolver reads from
// the GCP forwardingRule JSON. Kept tiny intentionally — see file header for
// the duplication rationale.
type gcpForwardingRuleContent struct {
	IPAddress string `json:"IPAddress"`
}

// awsELBContent mirrors elbContentDNS in cloud/aws/postpopulate_dns.go. Same
// shape, duplicated to avoid a cross-domain import.
type awsELBContent struct {
	DNSName string `json:"DNSName"`
}

// buildGCPForwardingRuleIndex scans every loaded GraphCloud graph for nodes
// of resource_type=gcp:compute:forwardingRule and returns a map from
// lowercase ipAddress to the (graph, nodeID) that owns it.
//
// Returns an empty map when no cloud graphs are loaded. Errors from the
// underlying graph queries propagate so the caller can decide whether to
// abort the whole resolver pass — typically callers will return the error to
// the postPopulate orchestrator.
func buildGCPForwardingRuleIndex(ctx context.Context, gc postpopulate.GraphCaller) (map[string][]cloudLBRef, error) {
	names, err := postpopulate.ListGraphNames(ctx, gc, kgtypes.GraphCloud)
	if err != nil {
		return nil, err
	}
	index := make(map[string][]cloudLBRef)
	for _, name := range names {
		nodes, err := queryCloudGraphByResourceType(ctx, gc, name, gcpForwardingRuleResourceType)
		if err != nil {
			return nil, err
		}
		mergeForwardingRuleNodes(index, name, nodes)
	}
	return index, nil
}

// buildAWSELBDNSIndex scans every loaded GraphCloud graph for nodes whose
// resource_type is one of awsELBResourceTypes and returns a map from
// lowercase DNSName to the (graph, nodeID) that owns it.
//
// Same loaded-graph + propagation semantics as buildGCPForwardingRuleIndex.
func buildAWSELBDNSIndex(ctx context.Context, gc postpopulate.GraphCaller) (map[string][]cloudLBRef, error) {
	names, err := postpopulate.ListGraphNames(ctx, gc, kgtypes.GraphCloud)
	if err != nil {
		return nil, err
	}
	index := make(map[string][]cloudLBRef)
	for _, name := range names {
		for _, rt := range awsELBResourceTypes {
			nodes, err := queryCloudGraphByResourceType(ctx, gc, name, rt)
			if err != nil {
				return nil, err
			}
			mergeELBDNSNodes(index, name, rt, nodes)
		}
	}
	return index, nil
}

// queryCloudGraphByResourceType is the shared "open this cloud graph and
// pull every node of a given resource_type" helper. Used by both index
// builders so the loaded-graph iteration stays one-liner-thin.
//
// Retrieve without WithCreate intentionally: we only want already-present
// graphs, never to materialize a fresh empty graph as a side effect.
func queryCloudGraphByResourceType(ctx context.Context, gc postpopulate.GraphCaller, graphName, resourceType string) ([]*knowledgev1.Node, error) {
	return postpopulate.BrowseNodes(ctx, gc, kgtypes.GraphCloud, graphName, map[string]any{
		"type":  string(kgtypes.NodeCloudResource),
		"meta":  map[string]string{"resource_type": resourceType},
		"limit": 0,
	})
}

// mergeForwardingRuleNodes folds a slice of forwardingRule nodes into the
// shared index. Each node contributes at most one entry: keyed on its
// lowercase ipAddress, with the (graph, ID) pair for proxy creation.
//
// Extraction order: prefer the parsed Metadata["ipAddress"] (set by
// cloud/gcp/loadbalancer.go's subcollector) over re-parsing Content. Falls
// back to JSON unmarshaling Content if the metadata is absent — keeps the
// helper resilient to graphs collected by older code paths that didn't
// populate the convenience metadata field.
func mergeForwardingRuleNodes(index map[string][]cloudLBRef, graphName string, nodes []*knowledgev1.Node) {
	for _, n := range nodes {
		ip := forwardingRuleIPFromNode(n)
		if ip == "" {
			continue
		}
		key := strings.ToLower(ip)
		index[key] = append(index[key], cloudLBRef{GraphName: graphName, NodeID: n.Id})
	}
}

// mergeELBDNSNodes folds AWS ELB nodes into the shared DNS index. One
// lowercase DNSName key per node, value is the (graph, ID = ARN) pair.
//
// resourceType is the exact resource_type value the caller queried for
// (one of awsELBResourceTypes). The store now applies Q.Meta predicates
// in the executor so the input slice is already filtered by resource_type
// — but we still keep the parameter for documentation of which type the
// merge call belongs to (helps callers reason about which DNS rows landed
// from which subcollector).
func mergeELBDNSNodes(index map[string][]cloudLBRef, graphName, resourceType string, nodes []*knowledgev1.Node) {
	_ = resourceType
	for _, n := range nodes {
		dns := elbDNSFromNode(n)
		if dns == "" {
			continue
		}
		key := strings.ToLower(dns)
		index[key] = append(index[key], cloudLBRef{GraphName: graphName, NodeID: n.Id})
	}
}

// forwardingRuleIPFromNode reads the IP address off a single
// gcp:compute:forwardingRule node. Metadata-first (subcollector convenience
// path), Content-fallback (older graphs, or graphs collected without
// metadata enrichment).
func forwardingRuleIPFromNode(n *knowledgev1.Node) string {
	if ip := kgtypes.Value(n, "ipAddress"); ip != "" {
		return ip
	}
	if n.Content == "" {
		return ""
	}
	var c gcpForwardingRuleContent
	if err := json.Unmarshal([]byte(n.Content), &c); err != nil {
		return ""
	}
	return c.IPAddress
}

// elbDNSFromNode reads the DNSName off a single AWS ELB / ELBv2 node. The
// AWS subcollector emits DNS only inside Content, so this helper does not
// have a metadata fast-path — Content unmarshal is the only source.
func elbDNSFromNode(n *knowledgev1.Node) string {
	if n.Content == "" {
		return ""
	}
	var c awsELBContent
	if err := json.Unmarshal([]byte(n.Content), &c); err != nil {
		return ""
	}
	return c.DNSName
}

// resolveLBEntryByAddress is the per-address resolution primitive shared by
// the Service (Phase 3), Ingress (Phase 4), and Gateway (Phase 5) resolvers.
// Given an IP and/or a Hostname, it consults the GCP IP index first and the
// AWS DNS index as a fallback, upserts one cross-graph proxy per matching
// ref, and returns an EdgeExposedBy edge from `from` to each proxy.
//
// Returns an empty slice when neither address resolves. A single address can
// back multiple LBs (GCP forwardingRules keyed on IP+port+protocol — one per
// protocol on a shared VIP is standard for GKE Gateway); the caller receives
// one edge per match. Per-proxy dedup via the seen map prevents re-emission
// across callers that pass the same from+address multiple times.
//
// GCP-first ordering preserved from Phase 3 — when the same Ingress entry
// carries both IP and Hostname, the IP lookup wins and AWS isn't consulted.
func resolveLBEntryByAddress(from *knowledgev1.Node, ip, hostname string, gcpIndex, awsIndex map[string][]cloudLBRef, seen map[string]struct{}, proxies *proxyAccumulator) ([]knowledgev1.Edge, error) {
	if ip = strings.ToLower(strings.TrimSpace(ip)); ip != "" {
		if refs, ok := gcpIndex[ip]; ok {
			return emitCloudLBEdges(from, refs, "gcp", "ip="+ip, seen, proxies)
		}
	}
	if hostname = strings.ToLower(strings.TrimSpace(hostname)); hostname != "" {
		if refs, ok := awsIndex[hostname]; ok {
			return emitCloudLBEdges(from, refs, "aws", "hostname="+hostname, seen, proxies)
		}
	}
	return nil, nil
}

// emitCloudLBEdges loops emitCloudLBEdge over a slice of refs so the Service
// /Ingress/Gateway resolvers stay agnostic to the one-or-many shape.
func emitCloudLBEdges(from *knowledgev1.Node, refs []cloudLBRef, provider, evidence string, seen map[string]struct{}, proxies *proxyAccumulator) ([]knowledgev1.Edge, error) {
	out := make([]knowledgev1.Edge, 0, len(refs))
	for _, ref := range refs {
		next, err := emitCloudLBEdge(out, from, ref, provider, evidence, seen, proxies)
		if err != nil {
			return nil, err
		}
		out = next
	}
	return out, nil
}
