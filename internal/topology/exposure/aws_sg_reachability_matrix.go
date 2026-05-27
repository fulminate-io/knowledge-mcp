// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"encoding/json"
	"fmt"
	"sort"
)

// aws_sg_reachability_matrix.go implements the raw-data emitter that
// turns sgReachabilityIndex into a single aws_sg_reachability_matrix
// finding so downstream analyzers (public_exposure) can query the
// matrix by name+type rather than re-running the analyzer.
//
// Emission is suppressed when the index is the skipped sentinel or when
// req.Extra["emit_matrix"] = "false". Finding fields: Algorithm
// "aws_sg_reachability_matrix", Summary JSON-serialized []sgMatrixEntry,
// Metadata account_id / entry_count / capped_by_limit. The matrix is
// capped at matrixMaxEntries — on overflow the slice holds the first
// matrixMaxEntries rows followed by one sentinel with Src == "truncated".

// matrixMaxEntriesForTesting is the effective per-run cap on sgMatrixEntry
// emission. Defaults to matrixMaxEntries (10000, reused from the K8s
// matrix emitter). Declared as a var so tests can lower the cap without
// allocating tens of thousands of synthetic resources.
var matrixMaxEntriesForTesting = matrixMaxEntries

// sgMatrixEntry is one row of the reachability matrix serialized into the
// matrix finding's Summary field. Field tags are stable because
// downstream consumers (public_exposure) decode them by name.
type sgMatrixEntry struct {
	Src      string `json:"src"`
	Dst      string `json:"dst"`
	Protocol string `json:"protocol,omitempty"`
	PortFrom int    `json:"port_from,omitempty"`
	PortTo   int    `json:"port_to,omitempty"`
	Via      string `json:"via,omitempty"`
	Note     string `json:"note,omitempty"`
}

// emitSGReachabilityMatrix builds and returns the single matrix finding
// for the given scoped account. Returns (zero, false) when the index is
// the skipped sentinel, has no resources, or the caller explicitly opted
// out via req.Extra["emit_matrix"] = "false".
func emitSGReachabilityMatrix(req Request, index *sgReachabilityIndex) (Finding, bool) {
	if index == nil || index.skipped || len(index.resources) == 0 {
		return Finding{}, false
	}
	if req.Extra != nil {
		if v, ok := req.Extra["emit_matrix"]; ok && v == "false" {
			return Finding{}, false
		}
	}
	entries, total := buildSGMatrixEntries(index)
	payload, err := json.Marshal(entries)
	if err != nil {
		return Finding{}, false
	}
	capped := "false"
	if total > matrixMaxEntriesForTesting {
		capped = "true"
	}
	return Finding{
		Algorithm: "aws_sg_reachability_matrix",
		Severity:  SeverityInfo,
		Title:     "aws_sg_reachability_matrix",
		Summary:   string(payload),
		Metrics: map[string]float64{
			"resource_count": float64(len(index.resources)),
			"entry_count":    float64(total),
		},
		Metadata: map[string]string{
			"account_id":      req.Name,
			"entry_count":     fmt.Sprintf("%d", total),
			"capped_by_limit": capped,
		},
	}, true
}

// buildSGMatrixEntries enumerates one entry per reachable
// (src, dst, protocol, port) tuple. Returns the entries slice
// (truncated to matrixMaxEntries + 1 sentinel if needed) and the
// pre-truncation total.
//
// Enumeration is driven from dst.AllowsIngressFrom — the sparse side
// of the graph under default-egress-allow fixtures. For each dst with
// ingress rules, src candidates are resolved via a prebuilt
// sg → []resource reverse index (or all resources for a CIDR peer),
// then per (src, probe) the full reachability filter chain applies
// (ingress / egress / NACL / cross-VPC). Dsts with no ingress entries
// are default-deny and skipped entirely — the decisive optimization on
// real AWS graphs where most resources carry a handful of ingress
// rules. The old pair scan was O(R²·P); this walk is
// O(Σ_dst |ingress_peers(dst)|·|sg_members|·P). Only REACHABLE entries
// are emitted — the dense per-pair output would dwarf the 10000-entry
// cap; downstream classifiers treat missing entries as "not reachable".
func buildSGMatrixEntries(index *sgReachabilityIndex) ([]sgMatrixEntry, int) {
	ids := sortedResourceIDs(index)
	probes := collectSGProbes(index)
	sgToResources := buildSGToResourceIndex(index, ids)
	ctx := matrixCollectCtx{
		index:         index,
		probes:        probes,
		sgToResources: sgToResources,
		ids:           ids,
		entries:       make([]sgMatrixEntry, 0, matrixMaxEntries+1),
	}
	for _, dst := range ids {
		ctx.collectForDst(dst)
	}
	entries := ctx.finalize()
	return entries, ctx.total
}

// matrixCollectCtx carries the mutable state threaded through the matrix
// build so buildSGMatrixEntries stays under the cognitive-complexity cap.
// Scratch buffers are reused across dsts to keep per-Run allocations O(1)
// in the number of dsts with ingress rules.
type matrixCollectCtx struct {
	index         *sgReachabilityIndex
	probes        []portProbe
	sgToResources map[string][]string
	ids           []string
	entries       []sgMatrixEntry
	total         int
	capReached    bool
	scratchSet    map[string]struct{}
	scratchSlice  []string
}

func (c *matrixCollectCtx) collectForDst(dst string) {
	dstInfo := c.index.resources[dst]
	if dstInfo == nil || len(dstInfo.AllowsIngressFrom) == 0 {
		return
	}
	candidates := ingressCandidatesFor(dstInfo, c.ids, c.sgToResources, &c.scratchSet, &c.scratchSlice)
	for _, src := range candidates {
		if src == dst {
			continue
		}
		srcInfo := c.index.resources[src]
		if srcInfo == nil {
			continue
		}
		if !c.index.crossVPCAllows(srcInfo, dstInfo) {
			continue
		}
		c.collectProbes(src, dst, srcInfo, dstInfo)
	}
}

func (c *matrixCollectCtx) collectProbes(src, dst string, srcInfo, dstInfo *resourceInfo) {
	for _, probe := range c.probes {
		if !ingressSGAllows(srcInfo, dstInfo, probe.Protocol, probe.Port) {
			continue
		}
		if !egressSGAllows(srcInfo, dstInfo, probe.Protocol, probe.Port) {
			continue
		}
		if !c.index.naclLayerAllows(srcInfo, dstInfo, probe.Protocol, probe.Port) {
			continue
		}
		c.total++
		if c.capReached || len(c.entries) >= matrixMaxEntriesForTesting {
			continue
		}
		c.entries = append(c.entries, sgMatrixEntry{
			Src:      src,
			Dst:      dst,
			Protocol: probe.Protocol,
			PortFrom: probe.Port,
			PortTo:   probe.Port,
			Via:      matrixVia(c.index, src, dst),
		})
		if len(c.entries) >= matrixMaxEntriesForTesting {
			c.capReached = true
		}
	}
}

func (c *matrixCollectCtx) finalize() []sgMatrixEntry {
	if c.total > matrixMaxEntriesForTesting {
		c.entries = append(c.entries, sgMatrixEntry{
			Src:  "truncated",
			Dst:  "truncated",
			Note: fmt.Sprintf("matrix truncated at %d entries (original total %d)", matrixMaxEntries, c.total),
		})
	}
	sort.SliceStable(c.entries, func(i, j int) bool {
		if c.entries[i].Src != c.entries[j].Src {
			return c.entries[i].Src < c.entries[j].Src
		}
		if c.entries[i].Dst != c.entries[j].Dst {
			return c.entries[i].Dst < c.entries[j].Dst
		}
		if c.entries[i].Protocol != c.entries[j].Protocol {
			return c.entries[i].Protocol < c.entries[j].Protocol
		}
		return c.entries[i].PortFrom < c.entries[j].PortFrom
	})
	return c.entries
}

// buildSGToResourceIndex inverts the resources map into a lookup keyed
// by SG ARN. Resources appear in each SG bucket in the caller-supplied
// sorted-ID order so downstream enumeration is deterministic. SGs with
// no members are absent from the returned map.
func buildSGToResourceIndex(index *sgReachabilityIndex, sortedIDs []string) map[string][]string {
	out := make(map[string][]string, len(index.sgs))
	for _, id := range sortedIDs {
		info := index.resources[id]
		if info == nil {
			continue
		}
		for _, sgID := range info.SGs {
			out[sgID] = append(out[sgID], id)
		}
	}
	return out
}

// ingressCandidatesFor returns the sorted src resource IDs that dst's
// ingress rules (taken together, ignoring per-probe port matching)
// potentially permit. Mirrors the peer-resolution half of
// ingressSGAllows: a CIDR-sentinel peer broadcasts to every resource,
// and an SG-peer resolves to that SG's member set. The per-probe port
// match is applied later by the caller's inner loop via ingressSGAllows.
//
// Hot-path allocations are avoided in two ways. First, when dst has
// exactly one (non-CIDR) ingress peer, the sgToResources slice is
// returned directly because buildSGToResourceIndex already emits it in
// sorted order. Second, the scratch set + slice are reused across dsts
// by the caller so the common multi-peer case allocates at most once
// per Run rather than once per dst.
func ingressCandidatesFor(dstInfo *resourceInfo, allIDs []string, sgToResources map[string][]string,
	scratchSet *map[string]struct{}, scratchSlice *[]string,
) []string {
	// Fast path: exactly one peer, no CIDR — return the prebuilt slice.
	if len(dstInfo.AllowsIngressFrom) == 1 {
		for peerID := range dstInfo.AllowsIngressFrom {
			if isCIDRSentinel(peerID) {
				return allIDs
			}
			return sgToResources[peerID]
		}
	}
	seen := *scratchSet
	if seen == nil {
		seen = make(map[string]struct{}, 16)
		*scratchSet = seen
	} else {
		for k := range seen {
			delete(seen, k)
		}
	}
	for peerID := range dstInfo.AllowsIngressFrom {
		if isCIDRSentinel(peerID) {
			return allIDs
		}
		for _, src := range sgToResources[peerID] {
			seen[src] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := (*scratchSlice)[:0]
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	*scratchSlice = out
	return out
}

// collectSGProbes returns the sorted, deduplicated set of (protocol,
// port) probes the matrix emitter enumerates. Zero-valued probe means
// "any / any" and acts as the universal fallback when the index contains
// no explicit port metadata.
func collectSGProbes(index *sgReachabilityIndex) []portProbe {
	seen := map[portProbe]struct{}{}
	add := func(r allowedRange) {
		port := r.PortFrom
		if port == 0 && r.PortTo != 0 {
			port = r.PortTo
		}
		seen[portProbe{Protocol: r.Protocol, Port: port}] = struct{}{}
	}
	for _, entries := range index.sgIngress {
		for _, e := range entries {
			add(e.Range)
		}
	}
	for _, entries := range index.sgEgress {
		for _, e := range entries {
			add(e.Range)
		}
	}
	if len(seen) == 0 {
		return []portProbe{{}}
	}
	out := make([]portProbe, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Protocol != out[j].Protocol {
			return out[i].Protocol < out[j].Protocol
		}
		return out[i].Port < out[j].Port
	})
	return out
}

// matrixVia describes the network path taken from src to dst. Same VPC
// returns "same-vpc"; cross-VPC returns "peering" when the two VPCs are
// connected by a peering edge. Returns "" when the path is ambiguous.
func matrixVia(index *sgReachabilityIndex, src, dst string) string {
	srcInfo := index.resources[src]
	dstInfo := index.resources[dst]
	if srcInfo == nil || dstInfo == nil {
		return ""
	}
	if srcInfo.VPC == "" || dstInfo.VPC == "" || srcInfo.VPC == dstInfo.VPC {
		return "same-vpc"
	}
	if index.vpcPeerings[srcInfo.VPC] != nil && index.vpcPeerings[srcInfo.VPC][dstInfo.VPC] {
		return "peering"
	}
	return ""
}
