// SPDX-License-Identifier: Apache-2.0

package exposure

import "encoding/json"

// k8s_reachability_ports.go holds the per-edge port/protocol decoding,
// range-intersection matching, and canReach predicate for the K8s
// reachability index. Keeping the port helpers in their own file lets
// k8s_reachability_index.go focus on graph walking while preserving the
// topology package's soft 300-line file cap.
//
// topology/ must not import cloud/k8s/ (layering rule), so the JSON schema
// for Edge.Evidence is duplicated here via edgePortMetadata. The field tags
// must match cloud/k8s's portMetadata encoder exactly or range intersection
// silently breaks.

// portRange is the decoded per-edge reachability metadata: a protocol filter
// plus a numeric port range. An empty Protocol means "any protocol"; a zero
// PortFrom AND zero PortTo means "all ports". Single-port rules have
// PortFrom == PortTo.
type portRange struct {
	Protocol string
	PortFrom int
	PortTo   int
}

// edgePortMetadata is a local JSON schema mirroring cloud/k8s's portMetadata
// encoder. Used to decode Edge.Evidence into a portRange without importing
// cloud/.
type edgePortMetadata struct {
	Protocol       string `json:"protocol,omitempty"`
	PortFrom       int    `json:"port_from,omitempty"`
	PortTo         int    `json:"port_to,omitempty"`
	NamedPort      string `json:"named_port,omitempty"`
	PortUnresolved bool   `json:"port_unresolved,omitempty"`
}

// parseEdgePortRange decodes an edge's Evidence field into a portRange. An
// empty string is the canonical fully-open signal (the collector emits no
// JSON when the rule has no ports[] clause) and yields a zero-value portRange
// that matches any query. Unresolved named ports (port_unresolved=true) fall
// through to a protocol-only filter so the classifier still surfaces the
// underlying reachability rather than silently dropping the edge.
func parseEdgePortRange(evidence string) portRange {
	if evidence == "" {
		return portRange{}
	}
	var meta edgePortMetadata
	if err := json.Unmarshal([]byte(evidence), &meta); err != nil {
		return portRange{}
	}
	if meta.PortUnresolved {
		return portRange{Protocol: meta.Protocol}
	}
	return portRange{
		Protocol: meta.Protocol,
		PortFrom: meta.PortFrom,
		PortTo:   meta.PortTo,
	}
}

// canReach reports whether src may initiate a connection to dst on the given
// (protocol, port). Applies K8s NetworkPolicy both-directions semantics: the
// call returns true only when src's egress rules permit reaching dst AND
// dst's ingress rules permit being reached from src. A pod that is NOT
// selected by any restricting policy on that direction is default-allow.
//
// Unknown src or dst pods return false (callers should pre-filter to pods
// that are present in the index). A skipped index returns false — callers
// should check index.skipped before issuing reachability queries.
//
// Port/protocol matching uses range intersection: a query (TCP, 80) matches
// an edge storing (TCP, 80, 100). See rangeCovers for the exact rules.
func (idx *reachabilityIndex) canReach(src, dst, protocol string, port int) bool {
	if idx == nil || idx.skipped {
		return false
	}
	srcPod, ok := idx.pods[src]
	if !ok {
		return false
	}
	dstPod, ok := idx.pods[dst]
	if !ok {
		return false
	}
	return egressAllows(srcPod, dst, protocol, port) && ingressAllows(dstPod, src, protocol, port)
}

// egressAllows reports whether srcPod permits egress to dst for the given
// (protocol, port). The dispatch order is:
//
//  1. AdminNetworkPolicy priority walk (egressDispatchANP). An explicit ANP
//     Allow returns true; an explicit ANP Deny returns false; an ANP Pass or
//     no-match falls through to step 2.
//  2. Regular NetworkPolicy: a non-egress-restricted pod is default-allow,
//     otherwise the AllowedEgressTo list for the destination must contain at
//     least one portRange covering the query.
func egressAllows(srcPod *podInfo, dst, protocol string, port int) bool {
	allowed, denied, fellThrough := egressDispatchANP(srcPod, dst, protocol, port)
	if !fellThrough {
		if allowed {
			return true
		}
		if denied {
			return false
		}
	}
	if !srcPod.EgressRestricted {
		return true
	}
	for _, r := range srcPod.AllowedEgressTo[dst] {
		if rangeCovers(r, protocol, port) {
			return true
		}
	}
	return false
}

// ingressAllows reports whether dstPod permits ingress from src for the
// given (protocol, port). Mirrors egressAllows on the ingress side: ANP
// priority dispatch runs first, then NetworkPolicy default-deny / allow.
func ingressAllows(dstPod *podInfo, src, protocol string, port int) bool {
	allowed, denied, fellThrough := ingressDispatchANP(dstPod, src, protocol, port)
	if !fellThrough {
		if allowed {
			return true
		}
		if denied {
			return false
		}
	}
	if !dstPod.IngressRestricted {
		return true
	}
	for _, r := range dstPod.AllowedIngressFrom[src] {
		if rangeCovers(r, protocol, port) {
			return true
		}
	}
	return false
}

// rangeCovers reports whether the stored (protocol, port range) portRange
// covers the query (protocol, port). Rules:
//
//   - Protocol: an empty stored Protocol matches any query protocol ("any").
//     An explicit stored Protocol must equal the query Protocol exactly
//     (case-sensitive; the collector canonicalizes to upper-case already).
//   - Port range: PortFrom == 0 AND PortTo == 0 means "all ports" — covers
//     any query port including 0. Otherwise the query port must satisfy
//     PortFrom <= port <= PortTo (inclusive on both ends). A stored range
//     with PortFrom > 0 and PortTo == 0 is tolerated as a single-port entry
//     (PortFrom == PortTo).
func rangeCovers(r portRange, protocol string, port int) bool {
	if r.Protocol != "" && r.Protocol != protocol {
		return false
	}
	if r.PortFrom == 0 && r.PortTo == 0 {
		return true
	}
	lo, hi := r.PortFrom, r.PortTo
	if hi == 0 {
		hi = lo
	}
	return port >= lo && port <= hi
}
