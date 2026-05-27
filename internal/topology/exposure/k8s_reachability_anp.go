// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// k8s_reachability_anp.go holds the AdminNetworkPolicy (ANP) priority dispatch
// for the K8s reachability index. ANP is a Kubernetes 1.29+ cluster-scoped
// policy type that overrides regular NetworkPolicy with explicit Allow/Deny/
// Pass actions ordered by integer priority (lower number = higher priority).
//
// LAYERING
//
// topology/ must not import cloud/k8s/, so the JSON schema for ANP-tagged
// edge metadata is duplicated here via anpEdgeMetadata. The field tags must
// match cloud/k8s's anpPortMetadata encoder exactly or priority dispatch
// silently breaks. Whenever you change one schema, change the other.
//
// SEMANTICS
//
//   - ANP Allow at the highest priority (lowest number) → reach succeeds.
//   - ANP Deny at the highest priority → reach fails.
//   - ANP Pass at the highest priority → fall through to NetworkPolicy.
//   - No ANP edge for the (src,dst,protocol,port) tuple → fall through to
//     NetworkPolicy.
//
// Both directions (src egress + dst ingress) are evaluated independently
// using the same priority dispatch. The final canReach result is the AND of
// both directions, mirroring the regular NetworkPolicy contract.

// anpEdgeMetadata is the local mirror of cloud/k8s's anpPortMetadata. Used to
// decode an Edge.Evidence field into an anpRange entry without importing
// cloud/.
type anpEdgeMetadata struct {
	Protocol       string `json:"protocol,omitempty"`
	PortFrom       int    `json:"port_from,omitempty"`
	PortTo         int    `json:"port_to,omitempty"`
	NamedPort      string `json:"named_port,omitempty"`
	PortUnresolved bool   `json:"port_unresolved,omitempty"`
	IsANP          bool   `json:"is_anp,omitempty"`
	ANPPriority    int    `json:"anp_priority"`
	ANPAction      string `json:"anp_action,omitempty"`
}

// anpAction enumerates the three legal ANP rule actions. Strings match the
// K8s ANP API exactly so anpEdgeMetadata.ANPAction can be compared directly
// without normalization.
const (
	anpActionAllow = "Allow"
	anpActionDeny  = "Deny"
	anpActionPass  = "Pass"
)

// anpRange is the decoded per-edge ANP entry consumed by the priority
// dispatch in egressAllowsANP / ingressAllowsANP. PortRange holds the
// underlying (protocol, port range) tuple; Priority and Action drive the
// dispatch order.
type anpRange struct {
	PortRange portRange
	Priority  int
	Action    string
}

// parseANPEdge inspects an Edge.Evidence field and decodes ANP entries. The
// store keys edges by (FromID, Type, ToID), so multiple ANP rules covering
// the same pod pair MUST be packed into a single edge. parseANPEdge accepts
// two payload shapes:
//
//   - JSON object form: {"is_anp":true,"anp_priority":N,...} — a single
//     ANP entry. Returned as a one-element slice.
//   - JSON array form: [{"is_anp":true,...},{"is_anp":true,...}] — multiple
//     ANP entries packed into one edge. Returned as a multi-element slice.
//
// Returns (entries, true) when at least one is_anp=true entry decodes
// successfully, or (nil, false) for regular NetworkPolicy edges (empty
// evidence, malformed JSON, or no is_anp marker).
func parseANPEdge(evidence string) ([]anpRange, bool) {
	if evidence == "" {
		return nil, false
	}
	// Try array form first — distinguishable by the leading '['.
	if evidence[0] == '[' {
		var arr []anpEdgeMetadata
		if err := json.Unmarshal([]byte(evidence), &arr); err != nil {
			return nil, false
		}
		entries := make([]anpRange, 0, len(arr))
		for _, m := range arr {
			if !m.IsANP {
				continue
			}
			entries = append(entries, anpEntryFromMeta(m))
		}
		if len(entries) == 0 {
			return nil, false
		}
		return entries, true
	}
	var meta anpEdgeMetadata
	if err := json.Unmarshal([]byte(evidence), &meta); err != nil {
		return nil, false
	}
	if !meta.IsANP {
		return nil, false
	}
	return []anpRange{anpEntryFromMeta(meta)}, true
}

// anpEntryFromMeta builds the canonical anpRange from a decoded
// anpEdgeMetadata, applying the unresolved-named-port fallback (protocol-only
// match) the same way the regular NetworkPolicy decoder does.
func anpEntryFromMeta(meta anpEdgeMetadata) anpRange {
	pr := portRange{
		Protocol: meta.Protocol,
		PortFrom: meta.PortFrom,
		PortTo:   meta.PortTo,
	}
	if meta.PortUnresolved {
		pr = portRange{Protocol: meta.Protocol}
	}
	return anpRange{
		PortRange: pr,
		Priority:  meta.ANPPriority,
		Action:    meta.ANPAction,
	}
}

// anpDecision is the outcome of an ANP priority walk for a single direction.
// Allowed and Denied are mutually exclusive — set when at least one ANP edge
// matched the (protocol, port) query at the winning priority. Fallthrough is
// true when either no ANP edge matched OR the highest-priority match was a
// Pass action; in both cases the caller falls through to NetworkPolicy.
type anpDecision struct {
	Allowed     bool
	Denied      bool
	Fallthrough bool
}

// evaluateANP applies priority ordering to a slice of anpRange entries for a
// fixed query (protocol, port). The walk:
//
//  1. Filters entries to those whose portRange covers the query.
//  2. Sorts the filtered set by Priority ascending (lowest number wins).
//  3. Iterates in priority order:
//     - First Allow → return Allowed.
//     - First Deny → return Denied.
//     - First Pass → return Fallthrough (the rule explicitly defers).
//
// An empty (or no-match) input returns Fallthrough so the caller defers to
// NetworkPolicy. Multiple ANPs at the SAME priority are tolerated: the first
// non-Pass action wins (deterministic order from the post-sort iteration).
func evaluateANP(entries []anpRange, protocol string, port int) anpDecision {
	matches := make([]anpRange, 0, len(entries))
	for _, e := range entries {
		if rangeCovers(e.PortRange, protocol, port) {
			matches = append(matches, e)
		}
	}
	if len(matches) == 0 {
		return anpDecision{Fallthrough: true}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		return matches[i].Priority < matches[j].Priority
	})
	for _, m := range matches {
		switch m.Action {
		case anpActionAllow:
			return anpDecision{Allowed: true}
		case anpActionDeny:
			return anpDecision{Denied: true}
		case anpActionPass:
			return anpDecision{Fallthrough: true}
		}
	}
	// All entries had unrecognized actions — fall through.
	return anpDecision{Fallthrough: true}
}

// egressDispatchANP runs the ANP priority walk for srcPod's egress towards
// dst on (protocol, port). Returns (allowed, denied, fellThrough). Callers
// use the third return value to decide whether to consult the regular
// NetworkPolicy egressAllows path.
func egressDispatchANP(srcPod *podInfo, dst, protocol string, port int) (bool, bool, bool) {
	entries := srcPod.ANPEgressTo[dst]
	if len(entries) == 0 {
		return false, false, true
	}
	d := evaluateANP(entries, protocol, port)
	return d.Allowed, d.Denied, d.Fallthrough
}

// ingressDispatchANP is the ingress counterpart of egressDispatchANP. Reads
// dstPod's ANPIngressFrom map for the given source pod ID.
func ingressDispatchANP(dstPod *podInfo, src, protocol string, port int) (bool, bool, bool) {
	entries := dstPod.ANPIngressFrom[src]
	if len(entries) == 0 {
		return false, false, true
	}
	d := evaluateANP(entries, protocol, port)
	return d.Allowed, d.Denied, d.Fallthrough
}

// populateANPEdges walks the ANP-specific edge types (EdgeANPIngressFrom /
// EdgeANPEgressTo) and bucketizes their decoded entries into the per-pod ANP
// allow/deny maps. The dedicated edge types isolate ANP entries from regular
// NetworkPolicy edges so the (FromID,Type,ToID)-keyed edgeMeta map can store
// both side-by-side without dedup. Called once per pod from
// populatePodEdges in k8s_reachability_index.go.
func populateANPEdges(ctx context.Context, scoped *cloudReader, idx *reachabilityIndex, podID string, info *podInfo) error {
	ingress, _ := scoped.iterEdges(ctx, podID, outgoingEdges, []kgtypes.EdgeType{kgtypes.EdgeANPIngressFrom})
	for _, e := range ingress {
		if _, ok := idx.pods[e.ToId]; !ok {
			continue
		}
		entries, ok := parseANPEdge(e.Evidence)
		if !ok {
			continue
		}
		info.ANPIngressFrom[e.ToId] = append(info.ANPIngressFrom[e.ToId], entries...)
	}

	egress, _ := scoped.iterEdges(ctx, podID, outgoingEdges, []kgtypes.EdgeType{kgtypes.EdgeANPEgressTo})
	for _, e := range egress {
		if _, ok := idx.pods[e.ToId]; !ok {
			continue
		}
		entries, ok := parseANPEdge(e.Evidence)
		if !ok {
			continue
		}
		info.ANPEgressTo[e.ToId] = append(info.ANPEgressTo[e.ToId], entries...)
	}
	return nil
}
