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

// postpopulate_netpol_anp.go is the post-populate seam for AdminNetworkPolicy
// (and BaselineAdminNetworkPolicy) reachability edges. It mirrors the regular
// NetworkPolicy resolver in postpopulate_netpol.go but stamps ANP-specific
// metadata on each emitted edge so the topology analyzer can apply ANP
// priority semantics at canReach time.
//
// COLLECTOR GAP NOTE
//
// The current k8s collector (sub_networkpolicies.go) does not list
// AdminNetworkPolicy resources — that API lives in sigs.k8s.io/network-policy-api
// which is not currently a project dependency. This file is the
// "analyzer-first, collector-follow-up" half of Phase 5.5: when a future
// subcollector populates nodes with resource_type="AdminNetworkPolicy" (or
// "BaselineAdminNetworkPolicy") and a JSON Content payload matching anpSpec,
// resolveANPReachabilityEdges automatically emits the ANP-tagged reachability
// edges. Until then this resolver is a no-op (the query returns zero nodes).
//
// EDGE METADATA SCHEMA
//
// Each emitted edge uses dedicated kgtypes.EdgeANPIngressFrom / kgtypes.EdgeANPEgressTo
// types so multiple ANP rules can co-exist with regular NetworkPolicy edges
// on the same (src, dst) pair without overwriting each other in the
// (FromID,Type,ToID)-keyed edgeMeta map. Edge.Method is set to
// methodAdminNetworkPolicy and Evidence carries an Evidence JSON payload
// extending portMetadata with three ANP-specific fields:
//
//	{
//	  "protocol":      "TCP" | "UDP" | "SCTP" | "",
//	  "port_from":     int,
//	  "port_to":       int,
//	  "is_anp":        true,
//	  "anp_priority":  int,           // lower number = higher priority
//	  "anp_action":    "Allow" | "Deny" | "Pass"
//	}
//
// The anpPortMetadata local struct owns the encoding so ports parsing stays in
// postpopulate_netpol_ports.go and the topology layer can decode the same
// shape via its own anonymous struct (per the layering rule).

// methodAdminNetworkPolicy is the Edge.Method discriminator stamped on every
// reachability edge produced by resolveANPReachabilityEdges. Distinct from
// methodNetworkPolicy so the topology analyzer can identify ANP edges by
// method alone if it ever needs the cheap path.
const methodAdminNetworkPolicy = "k8s-admin-networkpolicy"

// anpSpec is the partial JSON schema for an AdminNetworkPolicy spec captured
// from the K8s ANP API. The shape mirrors NetworkPolicy except (1) action is
// per-rule, (2) priority is at the spec level, and (3) "Subject" replaces
// "podSelector" with a more flexible namespaces/pods union — the resolver
// flattens both subject variants into a target pod set.
type anpSpec struct {
	Spec struct {
		Priority int        `json:"priority"`
		Subject  anpSubject `json:"subject"`
		Ingress  []anpRule  `json:"ingress,omitempty"`
		Egress   []anpRule  `json:"egress,omitempty"`
	} `json:"spec"`
}

// anpSubject is the ANP target selector. Exactly one of Namespaces or Pods is
// typically set. When Pods is set, the inner NamespaceSelector + PodSelector
// pair selects pods within matching namespaces (intersection semantics).
type anpSubject struct {
	Namespaces *metav1.LabelSelector `json:"namespaces,omitempty"`
	Pods       *anpPodSubject        `json:"pods,omitempty"`
}

// anpPodSubject is the inner Pods variant of an ANP subject — both selectors
// are required when Pods is set. NamespaceSelector picks the namespaces;
// PodSelector then filters pods within those namespaces.
type anpPodSubject struct {
	NamespaceSelector metav1.LabelSelector `json:"namespaceSelector"`
	PodSelector       metav1.LabelSelector `json:"podSelector"`
}

// anpRule models an ANP ingress/egress rule. Action is required and one of
// Allow/Deny/Pass. The peer shape mirrors NetworkPolicy peers but the K8s
// ANP type uses From/To with Namespaces/Pods variants — this resolver flattens
// both variants into the existing npPeer structure for code reuse.
type anpRule struct {
	Action string       `json:"action"`
	From   []anpPeer    `json:"from,omitempty"`
	To     []anpPeer    `json:"to,omitempty"`
	Ports  []npRulePort `json:"ports,omitempty"`
}

// anpPeer is the ANP peer schema. It accepts either a Namespaces selector or
// a Pods (NamespaceSelector + PodSelector) intersection — translated into the
// existing npPeer shape so resolvePeers can be reused unchanged.
type anpPeer struct {
	Namespaces *metav1.LabelSelector `json:"namespaces,omitempty"`
	Pods       *anpPodSubject        `json:"pods,omitempty"`
}

// toNPPeer converts an anpPeer into the existing npPeer shape so the regular
// resolvePeers helper from postpopulate_netpol_selectors.go can be reused.
// Returns the zero value when neither variant is set — resolvePeers treats
// that as a no-op peer.
func (p anpPeer) toNPPeer() npPeer {
	switch {
	case p.Pods != nil:
		// Both selectors are required by the ANP schema; resolvePeers reads
		// them via the intersection branch.
		ns := p.Pods.NamespaceSelector
		ps := p.Pods.PodSelector
		return npPeer{NamespaceSelector: &ns, PodSelector: &ps}
	case p.Namespaces != nil:
		return npPeer{NamespaceSelector: p.Namespaces}
	default:
		return npPeer{}
	}
}

// anpPortMetadata, fromPortMetadata, anpIngressEdges, anpEgressEdges, and
// buildANPEdge live in postpopulate_netpol_anp_rules.go to keep this file
// under the 300-line soft cap.

// resolveANPReachability turns a batch of ANP nodes into directional
// reachability edges. Each edge is tagged with the policy's priority and the
// matching rule's action so the topology analyzer can apply priority ordering
// at canReach time. The shape mirrors resolveNetworkPolicyReachability — the
// per-rule helpers below own the rule loop while this function handles the
// per-policy parse + target resolution.
func resolveANPReachability(
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

		var spec anpSpec
		if err := json.Unmarshal([]byte(policy.Content), &spec); err != nil {
			slog.Debug("resolveANPReachability: unmarshal content failed",
				"policy", policy.Id, "err", err)
			continue
		}

		targets := resolveANPSubject(podIndex, nsLabelIndex, spec.Spec.Subject)
		if len(targets) == 0 {
			continue
		}

		// ANP rules apply cluster-wide; the policy's "namespace" is irrelevant
		// for peer resolution. We pass an empty policyNS so resolvePeers
		// requires every peer to specify its own namespaces selector. The K8s
		// ANP schema enforces this at the API level.
		const anpPolicyNS = ""

		for _, rule := range spec.Spec.Ingress {
			ruleEdges, err := anpIngressEdges(podIndex, nsLabelIndex, podPortIndex, anpPolicyNS, targets, rule, spec.Spec.Priority)
			if err != nil {
				return nil, fmt.Errorf("anp %s ingress: %w", policy.Id, err)
			}
			edges = append(edges, ruleEdges...)
		}

		for _, rule := range spec.Spec.Egress {
			ruleEdges, err := anpEgressEdges(podIndex, nsLabelIndex, podPortIndex, anpPolicyNS, targets, rule, spec.Spec.Priority)
			if err != nil {
				return nil, fmt.Errorf("anp %s egress: %w", policy.Id, err)
			}
			edges = append(edges, ruleEdges...)
		}
	}

	return edges, nil
}

// resolveANPSubject flattens an ANP subject into the matching pod set. The
// subject is either a Namespaces selector (every pod in matching namespaces)
// or a Pods (NamespaceSelector + PodSelector) intersection. An empty subject
// returns nil so the caller skips the policy.
func resolveANPSubject(
	podIndex []podEntry,
	nsLabelIndex map[string]map[string]string,
	subject anpSubject,
) []podEntry {
	switch {
	case subject.Pods != nil:
		nsSel, err := labelSelectorFromLS(&subject.Pods.NamespaceSelector)
		if err != nil {
			return nil
		}
		podSel, err := labelSelectorFromLS(&subject.Pods.PodSelector)
		if err != nil {
			return nil
		}
		nsNames := namespacesMatching(nsLabelIndex, nsSel)
		var out []podEntry
		for _, ns := range nsNames {
			out = append(out, podsMatching(podIndex, ns, podSel)...)
		}
		return out
	case subject.Namespaces != nil:
		nsSel, err := labelSelectorFromLS(subject.Namespaces)
		if err != nil {
			return nil
		}
		nsNames := namespacesMatching(nsLabelIndex, nsSel)
		nsSet := make(map[string]struct{}, len(nsNames))
		for _, ns := range nsNames {
			nsSet[ns] = struct{}{}
		}
		var out []podEntry
		for _, p := range podIndex {
			if _, ok := nsSet[p.namespace]; ok {
				out = append(out, p)
			}
		}
		return out
	default:
		return nil
	}
}

// resolveANPReachabilityEdges is the postPopulate wiring for
// resolveANPReachability. It queries every AdminNetworkPolicy and
// BaselineAdminNetworkPolicy node from the graph, resolves reachability edges,
// and writes them via db.LinkBatch. Returns nil + nil edges when no ANP nodes
// exist (the common case today — no collector populates them).
func resolveANPReachabilityEdges(
	ctx context.Context,
	gc postpopulate.GraphCaller,
	graphName string,
	podIndex []podEntry,
	nsLabelIndex map[string]map[string]string,
	podPortIndex map[string][]podContainerPort,
) error {
	var policies []*knowledgev1.Node
	for _, rt := range []string{"AdminNetworkPolicy", "BaselineAdminNetworkPolicy"} {
		nodes, err := postpopulate.BrowseNodes(ctx, gc, kgtypes.GraphCloud, graphName, k8sResourceQuery(rt))
		if err != nil {
			return err
		}
		policies = append(policies, nodes...)
	}
	if len(policies) == 0 {
		return nil
	}

	edges, err := resolveANPReachability(podIndex, nsLabelIndex, podPortIndex, policies)
	if err != nil {
		return err
	}
	if len(edges) == 0 {
		return nil
	}

	if err := postpopulate.LinkEdgesBatch(ctx, gc, kgtypes.GraphCloud, graphName, edges); err != nil {
		slog.Debug("postPopulate: failed to create ANP reachability edges",
			"count", len(edges), "err", err)
		return err
	}

	slog.Debug("postPopulate: created ANP reachability edges", "count", len(edges))
	return nil
}
