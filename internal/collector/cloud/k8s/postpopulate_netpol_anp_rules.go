// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"encoding/json"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// postpopulate_netpol_anp_rules.go owns the per-rule edge emission for
// AdminNetworkPolicy. Split out from postpopulate_netpol_anp.go so the main
// resolver file stays under the 300-line soft cap. The contract:
//
//   - anpPortMetadata is the JSON wire-format extending portMetadata with
//     three ANP-specific fields (is_anp / anp_priority / anp_action). The
//     topology layer mirrors this struct in k8s_reachability_anp.go.
//   - fromPortMetadata builds an anpPortMetadata from a base portMetadata
//     plus a priority and action. Used by the per-rule helpers so the
//     existing expandRulePorts function in postpopulate_netpol_ports.go can
//     be reused for port expansion / named-port resolution unchanged.
//   - anpIngressEdges / anpEgressEdges convert one ANP rule into directional
//     reachability edges, mirroring ingressEdgesForRule / egressEdgesForRule
//     but using the dedicated EdgeANPIngressFrom / EdgeANPEgressTo types so
//     ANP edges can co-exist with regular NetworkPolicy edges on the same
//     pod pair.
//   - buildANPEdge stamps Edge.Method = methodAdminNetworkPolicy and the
//     encoded ANP metadata payload onto each emitted edge.

// anpPortMetadata extends portMetadata with the three ANP-specific fields the
// topology analyzer reads to apply priority ordering. is_anp is the marker
// the analyzer uses to bucketize edges into the ANP allow/deny maps.
type anpPortMetadata struct {
	Protocol       string `json:"protocol,omitempty"`
	PortFrom       int    `json:"port_from,omitempty"`
	PortTo         int    `json:"port_to,omitempty"`
	NamedPort      string `json:"named_port,omitempty"`
	PortUnresolved bool   `json:"port_unresolved,omitempty"`
	IsANP          bool   `json:"is_anp,omitempty"`
	ANPPriority    int    `json:"anp_priority"`
	ANPAction      string `json:"anp_action,omitempty"`
}

// encode marshals the metadata into the compact JSON form stored in
// Edge.Evidence. ANP edges always carry IsANP=true even when port fields are
// zero so the topology analyzer can distinguish them from a fully-open
// NetworkPolicy edge (which encodes to the empty string).
func (p anpPortMetadata) encode() string {
	b, err := json.Marshal(p)
	if err != nil {
		return `{"is_anp":true,"encode_error":true}`
	}
	return string(b)
}

// fromPortMetadata builds an anpPortMetadata from a base portMetadata plus the
// ANP-specific priority and action. Used by the per-rule emission helpers so
// expandRulePorts can be reused for port expansion / named-port resolution.
func fromPortMetadata(pm portMetadata, priority int, action string) anpPortMetadata {
	return anpPortMetadata{
		Protocol:       pm.Protocol,
		PortFrom:       pm.PortFrom,
		PortTo:         pm.PortTo,
		NamedPort:      pm.NamedPort,
		PortUnresolved: pm.PortUnresolved,
		IsANP:          true,
		ANPPriority:    priority,
		ANPAction:      action,
	}
}

// anpIngressEdges emits one edge per (target, source, port) combination for a
// single ANP ingress rule. Mirrors ingressEdgesForRule in
// postpopulate_netpol_ports.go but uses the dedicated EdgeANPIngressFrom edge
// type so multiple ANP rules can co-exist with regular NetworkPolicy edges
// without overwriting each other in the (FromID,Type,ToID)-keyed edgeMeta
// map.
func anpIngressEdges(
	podIndex []podEntry,
	nsLabelIndex map[string]map[string]string,
	podPortIndex map[string][]podContainerPort,
	policyNS string,
	targets []podEntry,
	rule anpRule,
	priority int,
) ([]knowledgev1.Edge, error) {
	npPeers := make([]npPeer, 0, len(rule.From))
	for _, p := range rule.From {
		npPeers = append(npPeers, p.toNPPeer())
	}
	sources, err := resolvePeers(podIndex, nsLabelIndex, policyNS, npPeers)
	if err != nil {
		return nil, err
	}
	if len(sources) == 0 {
		return nil, nil
	}
	edges := make([]knowledgev1.Edge, 0, len(targets)*len(sources))
	for _, tgt := range targets {
		portMetas := expandRulePorts(rule.Ports, tgt, podPortIndex)
		for _, src := range sources {
			if tgt.id == src.id {
				continue
			}
			for _, pm := range portMetas {
				edges = append(edges, buildANPEdge(tgt.id, src.id, kgtypes.EdgeANPIngressFrom, pm, priority, rule.Action))
			}
		}
	}
	return edges, nil
}

// anpEgressEdges is the egress counterpart of anpIngressEdges. Emits one edge
// per (target, dest, port) combination, resolving named ports against the
// destination pod (matching K8s egress semantics). Uses EdgeANPEgressTo so
// the edge can co-exist with a regular EdgeAllowsEgressTo on the same pair.
func anpEgressEdges(
	podIndex []podEntry,
	nsLabelIndex map[string]map[string]string,
	podPortIndex map[string][]podContainerPort,
	policyNS string,
	targets []podEntry,
	rule anpRule,
	priority int,
) ([]knowledgev1.Edge, error) {
	npPeers := make([]npPeer, 0, len(rule.To))
	for _, p := range rule.To {
		npPeers = append(npPeers, p.toNPPeer())
	}
	dests, err := resolvePeers(podIndex, nsLabelIndex, policyNS, npPeers)
	if err != nil {
		return nil, err
	}
	if len(dests) == 0 {
		return nil, nil
	}
	edges := make([]knowledgev1.Edge, 0, len(targets)*len(dests))
	for _, tgt := range targets {
		for _, dst := range dests {
			if tgt.id == dst.id {
				continue
			}
			portMetas := expandRulePorts(rule.Ports, dst, podPortIndex)
			for _, pm := range portMetas {
				edges = append(edges, buildANPEdge(tgt.id, dst.id, kgtypes.EdgeANPEgressTo, pm, priority, rule.Action))
			}
		}
	}
	return edges, nil
}

// buildANPEdge stamps the ANP method + encoded metadata onto a directed
// reachability Edge. Mirrors buildReachabilityEdge but uses
// methodAdminNetworkPolicy and the anpPortMetadata encoder.
func buildANPEdge(from, to string, edgeType kgtypes.EdgeType, pm portMetadata, priority int, action string) knowledgev1.Edge {
	return knowledgev1.Edge{
		FromId:   from,
		ToId:     to,
		Type:     string(edgeType),
		Method:   methodAdminNetworkPolicy,
		Evidence: fromPortMetadata(pm, priority, action).encode(),
	}
}
