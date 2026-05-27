// SPDX-License-Identifier: Apache-2.0

package exposure

// aws_sg_reachability_reach.go holds the per-layer reachability filter
// functions (SG ingress/egress, NACL, cross-VPC) for the AWS SG
// reachability index. Keeping reachability semantics in their own file lets
// aws_sg_reachability_index.go focus on graph walking.
//
// A packet from src to dst on (protocol, port) is reachable iff ALL of:
//
//  1. The src's SG egress rules contain an entry that permits dst's SG
//     (or a CIDR covering dst's address). AWS egress is default-allow
//     when a SG carries no explicit egress rules.
//  2. The dst's SG ingress rules contain an entry that permits src's SG
//     (or a CIDR covering src's address). Ingress is default-deny — if
//     the dst has no ingress rules the packet is dropped.
//  3. The src subnet's NACL egress and dst subnet's NACL ingress both
//     permit the packet. A subnet with zero NACL rules in a given
//     direction is default-allow (AWS provisions every new NACL with an
//     allow-all rule; a subnet with no NACL edges in the index either has
//     no NACL association or the default open NACL).
//  4. When src.VPC != dst.VPC, the two VPCs must be connected by an
//     EdgePeeredWith, EdgeRoutesVia, or EdgeExposedVia edge. Same-VPC
//     traffic skips this check.
//
// The matrix emitter (buildSGMatrixEntries) calls the individual layer
// functions directly. The streaming classifiers (world-open,
// transitive-chain, wide-CIDR) are edge-driven and bypass these entirely.

// egressSGAllows reports whether src's SG egress rules permit reaching
// dst. Default-allow when src has zero egress rules (SG with its default
// allow-all egress). Otherwise at least one entry must match dst's SGs or
// a CIDR covering dst.
//
// Hot path: walks the pre-built egressBySGPeer index for each dst SG and
// the egressCIDRIndex for the CIDR fallback. The pre-indexes are built by
// populateSGAttachments so this function does ZERO map iteration — only
// O(|dst.SGs|) bucket lookups plus a small port-sorted slice scan per
// matching bucket.
func egressSGAllows(srcInfo, dstInfo *resourceInfo, protocol string, port int) bool {
	if !srcInfo.hasEgressRules {
		return true
	}
	for _, dstSG := range dstInfo.SGs {
		bucket := srcInfo.egressBySGPeer[dstSG]
		if bucket == nil {
			continue
		}
		if bucket.covers(protocol, port) {
			return true
		}
	}
	// CIDR fallback — any CIDR entry conservatively matches every dst
	// (classifiers pick out true world-open separately).
	return srcInfo.egressCIDRIndex.covers(protocol, port)
}

// ingressSGAllows reports whether dst's SG ingress rules permit being
// reached from src. Ingress is default-deny: zero ingress rules means no
// match. At least one entry must match src's SGs or a CIDR covering src.
//
// Hot path mirrors egressSGAllows: per-srcSG bucket lookups against
// dstInfo.ingressBySGPeer plus a single dstInfo.ingressCIDRIndex.covers
// call. No map iteration.
func ingressSGAllows(srcInfo, dstInfo *resourceInfo, protocol string, port int) bool {
	if !dstInfo.hasIngressRules {
		return false
	}
	for _, srcSG := range srcInfo.SGs {
		bucket := dstInfo.ingressBySGPeer[srcSG]
		if bucket == nil {
			continue
		}
		if bucket.covers(protocol, port) {
			return true
		}
	}
	return dstInfo.ingressCIDRIndex.covers(protocol, port)
}

// naclLayerAllows reports whether both the src subnet's NACL egress rules
// and the dst subnet's NACL ingress rules permit the packet. Subnets with
// no NACL rules on a direction are default-allow (the index simply has no
// entries because no ALLOW edges exist — matches AWS's default NACL which
// allows everything).
func (idx *sgReachabilityIndex) naclLayerAllows(srcInfo, dstInfo *resourceInfo, protocol string, port int) bool {
	if srcInfo.Subnet != "" {
		if !naclAllows(idx.naclEgress[srcInfo.Subnet], protocol, port) {
			return false
		}
	}
	if dstInfo.Subnet != "" {
		if !naclAllows(idx.naclIngress[dstInfo.Subnet], protocol, port) {
			return false
		}
	}
	return true
}

// naclAllows reports whether the given NACL allow list contains at least
// one entry covering the query (protocol, port). An empty list is
// default-allow (zero NACL rules in that direction = no filter). Returns
// false only when rules exist AND none match.
func naclAllows(entries []sgAllowEntry, protocol string, port int) bool {
	if len(entries) == 0 {
		return true
	}
	for _, e := range entries {
		if rangeCoversSG(e.Range, protocol, port) {
			return true
		}
	}
	return false
}

// crossVPCAllows reports whether src and dst are in the same VPC OR there
// is a peering/TGW/endpoint edge connecting the two VPCs.
func (idx *sgReachabilityIndex) crossVPCAllows(srcInfo, dstInfo *resourceInfo) bool {
	if srcInfo.VPC == "" || dstInfo.VPC == "" || srcInfo.VPC == dstInfo.VPC {
		return true
	}
	peers := idx.vpcPeerings[srcInfo.VPC]
	if peers == nil {
		return false
	}
	return peers[dstInfo.VPC]
}

// rangeCoversSG reports whether the stored (protocol, port range)
// allowedRange covers the query (protocol, port). Mirrors the K8s
// rangeCovers helper — the logic is identical but uses the AWS
// allowedRange type so we can keep the package namespace separate.
//
// Rules:
//   - Protocol: empty stored Protocol matches any query ("any"). Explicit
//     stored Protocol must equal query Protocol.
//   - Port range: PortFrom == 0 AND PortTo == 0 means "all ports".
//     Otherwise the query port must satisfy PortFrom <= port <= PortTo.
//     A stored range with PortFrom > 0 and PortTo == 0 is tolerated as a
//     single-port entry (PortFrom == PortTo).
func rangeCoversSG(r allowedRange, protocol string, port int) bool {
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

// worldReachableOn reports whether the SG rules attached to a resource
// permit ingress from 0.0.0.0/0 on the given (protocol, port). Consulted
// by the world-open classifier.
func (idx *sgReachabilityIndex) worldReachableOn(resourceID, protocol string, port int) bool {
	info, ok := idx.resources[resourceID]
	if !ok {
		return false
	}
	for peerID, ranges := range info.AllowsIngressFrom {
		if !isCIDRSentinel(peerID) {
			continue
		}
		cidr := cidrFromSentinel(peerID)
		if cidr != "0.0.0.0/0" && cidr != "::/0" {
			continue
		}
		for _, r := range ranges {
			if rangeCoversSG(r, protocol, port) {
				return true
			}
		}
	}
	return false
}

// isCIDRSentinel reports whether the given peer ID is an "aws:cidr:*"
// sentinel node written by the collector. SG-peer edges point at SG ARNs;
// CIDR edges point at sentinel nodes with the "aws:cidr:" prefix.
func isCIDRSentinel(peerID string) bool {
	const prefix = "aws:cidr:"
	if len(peerID) < len(prefix) {
		return false
	}
	return peerID[:len(prefix)] == prefix
}

// cidrFromSentinel extracts the CIDR text from an "aws:cidr:<cidr>"
// sentinel ID. Returns the empty string when peerID is not a sentinel.
func cidrFromSentinel(peerID string) string {
	const prefix = "aws:cidr:"
	if len(peerID) < len(prefix) || peerID[:len(prefix)] != prefix {
		return ""
	}
	return peerID[len(prefix):]
}
