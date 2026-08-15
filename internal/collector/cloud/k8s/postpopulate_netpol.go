// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
)

// npSpec is a partial JSON schema for a NetworkPolicy spec. It captures just
// the fields needed for pod-to-pod reachability edge emission plus the ports
// clause so emitted edges can carry port/protocol metadata.
type npSpec struct {
	Spec struct {
		PodSelector metav1.LabelSelector `json:"podSelector"`
		PolicyTypes []string             `json:"policyTypes"`
		Ingress     []npRule             `json:"ingress"`
		Egress      []npRule             `json:"egress"`
	} `json:"spec"`
}

// npRule models an ingress or egress rule. The ports slice is parsed so
// emitted edges can include per-port metadata; an empty/missing ports slice
// means "all ports, all protocols" (fully open) and is encoded as a single
// empty-metadata edge per (target, peer) pair. The npRulePort type and all
// port-expansion helpers live in postpopulate_netpol_ports.go.
type npRule struct {
	From  []npPeer     `json:"from,omitempty"`
	To    []npPeer     `json:"to,omitempty"`
	Ports []npRulePort `json:"ports,omitempty"`
}

// npPeer models a NetworkPolicy peer. Exactly one of podSelector,
// namespaceSelector, or ipBlock is typically set, but both selectors may be
// combined (intersection semantics). ipBlock is captured for schema
// completeness but ignored during pod-to-pod edge emission — the analyzer
// handles ipBlock via JSON re-parse per the layering rule.
type npPeer struct {
	PodSelector       *metav1.LabelSelector `json:"podSelector,omitempty"`
	NamespaceSelector *metav1.LabelSelector `json:"namespaceSelector,omitempty"`
	IPBlock           *npIPBlock            `json:"ipBlock,omitempty"`
}

// npIPBlock is intentionally minimal — we only need to detect its presence so
// we can skip the peer for pod-to-pod reachability.
type npIPBlock struct {
	CIDR string `json:"cidr,omitempty"`
}

// resolveNetworkPolicyReachability converts a batch of NetworkPolicy nodes
// into a list of directional reachability edges between pods. Inputs:
//
//   - podIndex: every pod in the cluster with its namespace + labels
//   - nsLabelIndex: namespace → labels map (empty map for unlabeled namespaces)
//   - podPortIndex: pod ID → container port declarations (from
//     buildPodPortIndex) used to resolve named-port references
//   - policies: NetworkPolicy nodes whose Content field holds the raw JSON
//
// For every policy it:
//  1. Parses Content into an npSpec
//  2. Resolves spec.podSelector against pods in the policy's own namespace
//     to determine the target set
//  3. For each ingress rule, resolves every from[] peer to concrete source
//     pods and emits EdgeAllowsIngressFrom (target → source), one edge per
//     (target, source, port) combination
//  4. Symmetric for egress: emits EdgeAllowsEgressTo (source → target)
//
// Peer resolution semantics:
//   - podSelector only: pods in the policy's namespace matching the selector
//   - namespaceSelector only: pods in ANY namespace whose labels match,
//     matching every pod in each such namespace
//   - both: intersection (matching pods within matching namespaces)
//   - ipBlock: skipped (handled by the analyzer, not the collector)
//
// Policy-type defaults follow K8s semantics: if spec.policyTypes is unset it
// defaults to [Ingress] when ingress rules exist, or [Ingress, Egress] when
// egress rules exist.
//
// # Edge metadata schema
//
// Each emitted edge carries port/protocol metadata in Edge.Evidence as a
// compact JSON object. Edge.Method is set to methodNetworkPolicy so
// downstream consumers can recognize the schema. The JSON payload has four
// fields, all optional — an empty/missing field means "all ports / all
// protocols" (fully open):
//
//	{
//	  "protocol":        "TCP" | "UDP" | "SCTP" | "",   // empty = any
//	  "port_from":       int,                           // range start (0 = unset)
//	  "port_to":         int,                           // range end (equal to port_from for single ports)
//	  "named_port":      string,                        // original name, non-empty only when the rule used a name
//	  "port_unresolved": bool                           // true when the named port could not be resolved
//	}
//
// A rule with an empty ports[] slice emits edges with an empty Evidence
// string (no JSON), which is the canonical fully-open signal. Rules with
// numeric ports emit port_from==port_to (single port) or port_from<port_to
// (range). Rules with named ports are expanded per target pod: each target
// pod's container ports are searched for a matching name; a hit emits an
// edge with port_from==port_to==<resolved number>; a miss across every
// target pod emits an edge with port_unresolved=true (the named_port is
// preserved for diagnostics). Named ports that resolve to different numbers
// across pods yield separate edges per target pod.
func resolveNetworkPolicyReachability(
	podIndex []podEntry,
	nsLabelIndex map[string]map[string]string,
	podPortIndex map[string][]podContainerPort,
	policies []*knowledgev1.Node,
) ([]knowledgev1.Edge, error) {
	var edges []knowledgev1.Edge

	for _, policy := range policies {
		if len(policy.Content) == 0 {
			continue
		}

		var spec npSpec
		if err := json.Unmarshal([]byte(policy.Content), &spec); err != nil {
			// Malformed content is not fatal for other policies.
			slog.Debug("resolveNetworkPolicyReachability: unmarshal content failed",
				"policy", policy.Id, "err", err)
			continue
		}

		policyNS := kgtypes.Value(policy, "namespace")
		if policyNS == "" {
			continue
		}

		targetPodSel, err := labelSelectorFromLS(&spec.Spec.PodSelector)
		if err != nil {
			slog.Debug("resolveNetworkPolicyReachability: bad pod selector",
				"policy", policy.Id, "err", err)
			continue
		}

		targets := podsMatching(podIndex, policyNS, targetPodSel)
		if len(targets) == 0 {
			continue
		}

		ingressRules, egressRules := effectivePolicyTypes(spec)

		if ingressRules {
			for _, rule := range spec.Spec.Ingress {
				ruleEdges, err := ingressEdgesForRule(podIndex, nsLabelIndex, podPortIndex, policyNS, targets, rule)
				if err != nil {
					return nil, fmt.Errorf("policy %s ingress: %w", policy.Id, err)
				}
				edges = append(edges, ruleEdges...)
			}
		}

		if egressRules {
			for _, rule := range spec.Spec.Egress {
				ruleEdges, err := egressEdgesForRule(podIndex, nsLabelIndex, podPortIndex, policyNS, targets, rule)
				if err != nil {
					return nil, fmt.Errorf("policy %s egress: %w", policy.Id, err)
				}
				edges = append(edges, ruleEdges...)
			}
		}
	}

	return edges, nil
}

// effectivePolicyTypes returns whether ingress and egress rules should be
// evaluated for a given policy spec. If policyTypes is explicitly set, it is
// authoritative. Otherwise K8s implicit defaults apply: ingress is always
// active; egress is active only when spec.egress is non-empty.
func effectivePolicyTypes(spec npSpec) (ingress, egress bool) {
	if len(spec.Spec.PolicyTypes) > 0 {
		for _, pt := range spec.Spec.PolicyTypes {
			switch pt {
			case "Ingress":
				ingress = true
			case "Egress":
				egress = true
			}
		}
		return ingress, egress
	}
	// Implicit default per K8s docs.
	return true, len(spec.Spec.Egress) > 0
}

// resolveNetworkPolicyReachabilityEdges is the postPopulate wiring for
// resolveNetworkPolicyReachability. It queries every NetworkPolicy node from
// the graph, resolves reachability edges, and writes them via db.LinkBatch
// so each edge's Method/Evidence port-metadata survives the write.
func resolveNetworkPolicyReachabilityEdges(
	ctx context.Context,
	gc postpopulate.GraphCaller,
	graphName string,
	podIndex []podEntry,
	nsLabelIndex map[string]map[string]string,
	podPortIndex map[string][]podContainerPort,
) error {
	policies, err := postpopulate.BrowseAllNodes(ctx, gc, kgtypes.GraphCloud, graphName, k8sResourceQuery("NetworkPolicy"))
	if err != nil {
		return err
	}

	edges, err := resolveNetworkPolicyReachability(podIndex, nsLabelIndex, podPortIndex, policies)
	if err != nil {
		return err
	}
	if len(edges) == 0 {
		return nil
	}

	if err := postpopulate.LinkEdgesBatch(ctx, gc, kgtypes.GraphCloud, graphName, edges); err != nil {
		slog.Debug("postPopulate: failed to create reachability edges",
			"count", len(edges), "err", err)
		return err
	}

	var ingressCount, egressCount int
	for i := range edges {
		switch kgtypes.EdgeType(edges[i].Type) {
		case kgtypes.EdgeAllowsIngressFrom:
			ingressCount++
		case kgtypes.EdgeAllowsEgressTo:
			egressCount++
		}
	}

	if ingressCount+egressCount > 0 {
		slog.Debug("postPopulate: created reachability edges",
			"allows_ingress_from", ingressCount,
			"allows_egress_to", egressCount)
	}
	return nil
}
