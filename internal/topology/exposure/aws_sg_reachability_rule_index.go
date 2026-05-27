// SPDX-License-Identifier: Apache-2.0

package exposure

import "sort"

// aws_sg_reachability_rule_index.go defines sgPortIndex — a per-(resource,
// direction, peer-class) lookup that replaces the per-call map iteration in
// ingressSGAllows / egressSGAllows. The pre-pass to build it pays at index
// construction time so the matrix emitter's hot path becomes O(1) bucket
// lookup + O(small slice) port scan instead of an O(M) map walk per probe.
//
// The shape mirrors what AWS rule sets actually look like: a handful of
// distinct protocols (typically just "tcp" and "udp", plus the empty
// "match-any" entry), and within each protocol a small port-sorted slice
// (usually 1-3 entries). Rules with no protocol live in the allProto bucket
// and short-circuit any protocol query.
//
// LAYERING. Pure Go, no imports beyond stdlib. Stays under the 300-line
// soft cap so adding more buckets later (e.g. an interval tree variant for
// high-rule-count SGs) does not bloat the file.

// sgPortIndex is the pre-indexed lookup over a slice of allowedRange
// values. allProto holds entries whose Protocol is the empty string (the
// AWS "match-any" wildcard); byProto holds protocol-keyed buckets, each
// sorted by PortFrom ascending. Zero-value usable — covers always returns
// false on an empty index.
type sgPortIndex struct {
	allProto []allowedRange
	byProto  map[string][]allowedRange
}

// add inserts r into the appropriate bucket. Callers must finalize() once
// all entries have been added so the per-protocol buckets are sorted by
// PortFrom.
func (idx *sgPortIndex) add(r allowedRange) {
	if r.Protocol == "" {
		idx.allProto = append(idx.allProto, r)
		return
	}
	if idx.byProto == nil {
		idx.byProto = make(map[string][]allowedRange, 2)
	}
	idx.byProto[r.Protocol] = append(idx.byProto[r.Protocol], r)
}

// finalize sorts every per-protocol bucket by PortFrom ascending so
// covers() can early-exit once it has scanned past the query port. The
// allProto bucket is kept in insertion order — covers always scans it
// linearly because the protocol wildcard means there is no port-bucket
// short-circuit anyway.
func (idx *sgPortIndex) finalize() {
	for proto, entries := range idx.byProto {
		sort.SliceStable(entries, func(i, j int) bool {
			return entries[i].PortFrom < entries[j].PortFrom
		})
		idx.byProto[proto] = entries
	}
}

// covers reports whether any entry in the index permits (protocol, port).
// First scans the allProto bucket — those entries match every protocol
// query. Then looks up the protocol bucket and walks it with an early
// exit at PortFrom > port. The "all ports" sentinel (PortFrom == 0 AND
// PortTo == 0) is preserved by the linear scan because zero sorts first.
func (idx *sgPortIndex) covers(protocol string, port int) bool {
	for _, r := range idx.allProto {
		if portInRange(r, port) {
			return true
		}
	}
	if idx.byProto == nil {
		return false
	}
	bucket, ok := idx.byProto[protocol]
	if !ok {
		return false
	}
	for _, r := range bucket {
		if r.PortFrom > port && (r.PortFrom != 0 || r.PortTo != 0) {
			// Sorted ascending by PortFrom — once we pass the query
			// port no later entry can cover it.
			return false
		}
		if portInRange(r, port) {
			return true
		}
	}
	return false
}

// portInRange mirrors the port half of rangeCoversSG: zero/zero is the
// "all ports" sentinel, otherwise lo<=port<=hi with hi==0 tolerated as
// single-port shorthand. Protocol is matched at bucket-selection time, so
// it is intentionally not re-checked here.
func portInRange(r allowedRange, port int) bool {
	if r.PortFrom == 0 && r.PortTo == 0 {
		return true
	}
	lo, hi := r.PortFrom, r.PortTo
	if hi == 0 {
		hi = lo
	}
	return port >= lo && port <= hi
}

// addToDirectionalIndex routes one sgAllowEntry into the appropriate
// pre-indexed bucket. CIDR-sentinel peers go into the flat per-direction
// CIDR index (the reachability hot path only needs port/protocol there).
// SG-peer rules go into the per-peer-SG index keyed by the peer's ARN.
// Caller is responsible for invoking finalizeRuleIndexes() once after
// all entries have been added.
func addToDirectionalIndex(entry sgAllowEntry, cidrIndex *sgPortIndex, byPeer *map[string]*sgPortIndex) {
	if isCIDRSentinel(entry.PeerID) {
		cidrIndex.add(entry.Range)
		return
	}
	if *byPeer == nil {
		*byPeer = make(map[string]*sgPortIndex, 2)
	}
	bucket := (*byPeer)[entry.PeerID]
	if bucket == nil {
		bucket = &sgPortIndex{}
		(*byPeer)[entry.PeerID] = bucket
	}
	bucket.add(entry.Range)
}

// finalizeRuleIndexes sorts every per-protocol bucket on every
// directional index by PortFrom ascending. Called once at the end of
// populateSGAttachments after all entries have been added.
func finalizeRuleIndexes(info *resourceInfo) {
	info.ingressCIDRIndex.finalize()
	for _, bucket := range info.ingressBySGPeer {
		bucket.finalize()
	}
	info.egressCIDRIndex.finalize()
	for _, bucket := range info.egressBySGPeer {
		bucket.finalize()
	}
}
