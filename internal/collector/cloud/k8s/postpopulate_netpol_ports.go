// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"encoding/json"
	"strconv"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// methodNetworkPolicy is the Edge.Method discriminator stamped on every
// reachability edge produced by resolveNetworkPolicyReachability so
// downstream consumers know the Evidence payload uses the port-metadata
// schema documented on resolveNetworkPolicyReachability.
const methodNetworkPolicy = "k8s-networkpolicy"

// npRulePort mirrors the NetworkPolicyPort schema. The Port field is a
// union type in K8s (int or string for named ports) so it is decoded as
// json.RawMessage and normalized downstream by parseRulePort. EndPort is
// optional and non-zero only for numeric ranges.
type npRulePort struct {
	Protocol string          `json:"protocol,omitempty"`
	Port     json.RawMessage `json:"port,omitempty"`
	EndPort  int             `json:"endPort,omitempty"`
}

// ingressEdgesForRule resolves a single ingress rule's from[] peers into
// EdgeAllowsIngressFrom edges from every target pod to every resolved source
// pod, one edge per (target, source, port) combination. An empty ports slice
// emits a single fully-open edge per (target, source) pair.
func ingressEdgesForRule(
	podIndex []podEntry,
	nsLabelIndex map[string]map[string]string,
	podPortIndex map[string][]podContainerPort,
	policyNS string,
	targets []podEntry,
	rule npRule,
) ([]knowledgev1.Edge, error) {
	sources, err := resolvePeers(podIndex, nsLabelIndex, policyNS, rule.From)
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
				edges = append(edges, buildReachabilityEdge(tgt.id, src.id, kgtypes.EdgeAllowsIngressFrom, pm))
			}
		}
	}
	return edges, nil
}

// egressEdgesForRule is the egress counterpart of ingressEdgesForRule. It
// emits EdgeAllowsEgressTo edges from every target pod (the egress source)
// to every resolved destination pod, one edge per (target, dest, port)
// combination. Named ports are resolved against the DESTINATION pod's
// container ports because egress ports on the K8s spec name the port on the
// peer being connected to.
func egressEdgesForRule(
	podIndex []podEntry,
	nsLabelIndex map[string]map[string]string,
	podPortIndex map[string][]podContainerPort,
	policyNS string,
	targets []podEntry,
	rule npRule,
) ([]knowledgev1.Edge, error) {
	dests, err := resolvePeers(podIndex, nsLabelIndex, policyNS, rule.To)
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
			// For egress rules, named ports reference ports on the
			// destination pod (the service the client is egressing to).
			portMetas := expandRulePorts(rule.Ports, dst, podPortIndex)
			for _, pm := range portMetas {
				edges = append(edges, buildReachabilityEdge(tgt.id, dst.id, kgtypes.EdgeAllowsEgressTo, pm))
			}
		}
	}
	return edges, nil
}

// portMetadata is the decoded form of Edge.Evidence for NetworkPolicy
// reachability edges. All fields are optional — the zero value signals a
// fully-open edge (all ports, all protocols). When port_unresolved is true
// the named_port field preserves the original name for diagnostics.
type portMetadata struct {
	Protocol       string `json:"protocol,omitempty"`
	PortFrom       int    `json:"port_from,omitempty"`
	PortTo         int    `json:"port_to,omitempty"`
	NamedPort      string `json:"named_port,omitempty"`
	PortUnresolved bool   `json:"port_unresolved,omitempty"`
}

// isOpen reports whether the metadata represents the "all ports / all
// protocols" (fully open) signal. An all-zero value is the canonical
// fully-open marker.
func (p portMetadata) isOpen() bool {
	return p.Protocol == "" && p.PortFrom == 0 && p.PortTo == 0 &&
		p.NamedPort == "" && !p.PortUnresolved
}

// encode returns the compact JSON string that is stored in Edge.Evidence.
// A fully-open metadata value encodes to the empty string so the (by far
// most common) all-ports case does not pay the JSON allocation cost.
func (p portMetadata) encode() string {
	if p.isOpen() {
		return ""
	}
	b, err := json.Marshal(p)
	if err != nil {
		// Marshal of a plain struct with primitive fields cannot fail in
		// practice; fall back to a stable diagnostic so the edge is still
		// emitted with something non-empty.
		return `{"protocol":"","port_from":0,"port_to":0,"encode_error":true}`
	}
	return string(b)
}

// buildReachabilityEdge stamps the NetworkPolicy method + encoded port
// metadata onto a directed reachability Edge. Callers assemble the edge
// type (ingress/egress) and endpoints; this helper owns the metadata
// contract so the two edge-emission loops cannot drift apart.
func buildReachabilityEdge(from, to string, edgeType kgtypes.EdgeType, pm portMetadata) knowledgev1.Edge {
	return knowledgev1.Edge{
		FromId:   from,
		ToId:     to,
		Type:     string(edgeType),
		Method:   methodNetworkPolicy,
		Evidence: pm.encode(),
	}
}

// expandRulePorts turns a rule's ports[] slice into one portMetadata per
// edge-to-emit for a given target pod. Empty/nil ports[] means "all ports"
// — it returns a single zero-value portMetadata so callers emit exactly
// one fully-open edge per (target, peer) pair. Named ports are resolved
// against the given target pod's container ports (ingress semantics); the
// egress helper instead passes the destination pod because K8s egress
// named ports name a port on the pod being connected to.
func expandRulePorts(ports []npRulePort, targetPod podEntry, podPortIndex map[string][]podContainerPort) []portMetadata {
	if len(ports) == 0 {
		return []portMetadata{{}}
	}
	out := make([]portMetadata, 0, len(ports))
	for _, rp := range ports {
		protocol := canonicalProtocol(rp.Protocol)
		numericPort, namedPort, haveAny := parseRulePort(rp.Port)
		if !haveAny {
			// Entry with only a protocol filter (no port field). Emit a
			// protocol-scoped edge with no numeric range.
			out = append(out, portMetadata{Protocol: protocol})
			continue
		}
		if namedPort != "" {
			resolved, ok := resolveNamedPort(podPortIndex, targetPod.id, namedPort, protocol)
			if !ok {
				out = append(out, portMetadata{
					Protocol:       protocol,
					NamedPort:      namedPort,
					PortUnresolved: true,
				})
				continue
			}
			out = append(out, portMetadata{
				Protocol: protocol,
				PortFrom: resolved,
				PortTo:   resolved,
			})
			continue
		}
		portTo := numericPort
		if rp.EndPort > 0 && rp.EndPort >= numericPort {
			portTo = rp.EndPort
		}
		out = append(out, portMetadata{
			Protocol: protocol,
			PortFrom: numericPort,
			PortTo:   portTo,
		})
	}
	return out
}

// parseRulePort decodes the union-typed `port` field from a
// NetworkPolicyPort entry. Returns (numericPort, namedPort, ok) where ok
// is false when the field is absent or malformed. Exactly one of
// numericPort / namedPort is non-zero on success.
func parseRulePort(raw json.RawMessage) (int, string, bool) {
	if len(raw) == 0 {
		return 0, "", false
	}
	// Try numeric first.
	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		return n, "", true
	}
	// Fall back to string (named port). K8s allows integer-as-string too;
	// handle that as a numeric resolution so the collector doesn't leak a
	// named-port miss for what is really a number.
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return 0, "", false
	}
	if s == "" {
		return 0, "", false
	}
	if v, err := strconv.Atoi(s); err == nil {
		return v, "", true
	}
	return 0, s, true
}

// canonicalProtocol normalizes the protocol string. Kubernetes defaults
// protocol to TCP when the field is absent; we preserve the empty string
// as "any protocol" so the topology analyzer can distinguish an explicit
// TCP-only rule from a fully-open rule. Explicit values are upper-cased
// to match the K8s enum.
func canonicalProtocol(p string) string {
	switch p {
	case "", "tcp", "TCP":
		if p == "" {
			return ""
		}
		return "TCP"
	case "udp", "UDP":
		return "UDP"
	case "sctp", "SCTP":
		return "SCTP"
	default:
		return p
	}
}

// resolveNamedPort looks up (name, protocol) in the podPortIndex entry for
// a given pod. Returns the numeric containerPort and true on a hit. A miss
// returns (0, false) so the caller can emit a port_unresolved edge.
// Protocol matching treats an empty rule protocol as "any": a container
// port declared without an explicit protocol is considered TCP.
func resolveNamedPort(podPortIndex map[string][]podContainerPort, podID, name, ruleProtocol string) (int, bool) {
	if name == "" {
		return 0, false
	}
	ports, ok := podPortIndex[podID]
	if !ok {
		return 0, false
	}
	want := ruleProtocol
	for _, cp := range ports {
		if cp.name != name {
			continue
		}
		have := cp.protocol
		if have == "" {
			have = "TCP"
		}
		if want == "" || want == have {
			return cp.port, true
		}
	}
	return 0, false
}
