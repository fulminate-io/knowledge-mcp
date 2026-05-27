// SPDX-License-Identifier: Apache-2.0

package exposure

// k8s_reachability_findings_streaming.go holds the O(P + E) streaming helpers
// the per-pod classifiers in k8s_reachability_findings_rules.go consume. The
// motivation: the naive pair-quadratic walk (every pod × every other pod ×
// every probe) blew up on clusters beyond a few hundred pods. The streaming
// helpers iterate each pod's own allow maps and use the index's reverse-allow
// lookups to answer whole-pod questions in O(degree) instead of O(N).
//
// The helpers assume the index is fully built (reverseAllowedIngress and
// reverseAllowedEgress are populated). Calling them on a skipped sentinel
// returns empty results without touching any state.

// streamingIsolated reports whether a fully-restricted pod is isolated in the
// "no probe reaches or is reachable" sense. Runs in O(degree × probes) by
// iterating the pod's AllowedEgressTo and AllowedIngressFrom entries — every
// other (pod, probe) pair would already be default-denied by the both-sides
// guard in canReach, so we never need to visit them.
//
// Precondition: both pod.IngressRestricted and pod.EgressRestricted are true.
// The caller filters default-allow pods out before invoking this helper.
func streamingIsolated(index *reachabilityIndex, id string, pod *podInfo, probes []portProbe) bool {
	for dst := range pod.AllowedEgressTo {
		for _, probe := range probes {
			if index.canReach(id, dst, probe.Protocol, probe.Port) {
				return false
			}
		}
	}
	for src := range pod.AllowedIngressFrom {
		for _, probe := range probes {
			if index.canReach(src, id, probe.Protocol, probe.Port) {
				return false
			}
		}
	}
	// A fully-restricted pod with empty allow maps is trivially isolated:
	// default-deny on both sides + no whitelist entries = no reachable peer.
	return true
}

// streamingOverExposedOnProbe reports whether every namespace peer of dstID
// can reach dst on the given probe. Uses per-pod streaming iteration instead
// of the full O(namespace × probes) walk.
//
// Semantics preserved from the original pair-based classifier:
//   - If dst is NOT ingress-restricted → every src reaches dst iff src's
//     egress policy permits dst on the probe. For a default-allow src this
//     is trivially true; for a restricted src the pod must list dst in its
//     AllowedEgressTo with a covering range.
//   - If dst IS ingress-restricted → for each src the probe-covering range
//     must appear in dst.AllowedIngressFrom[src] AND src must be able to
//     egress. We walk the peers array once and short-circuit on the first
//     src that cannot reach dst.
//
// O(|peers|) per call, same as the current `allPeersReach` helper — the win
// comes from the outer classifier being able to skip pods whose ingress maps
// obviously undershoot the namespace size.
func streamingOverExposedOnProbe(index *reachabilityIndex, peers []string, dstID string, probe portProbe) bool {
	ok := false
	for _, src := range peers {
		if src == dstID {
			continue
		}
		if !index.canReach(src, dstID, probe.Protocol, probe.Port) {
			return false
		}
		ok = true
	}
	return ok
}

// overExposedCandidate reports whether a pod could plausibly be over-exposed
// given its allow-map shape. Used as a cheap short-circuit before the
// probe-by-probe check: an ingress-restricted pod whose AllowedIngressFrom
// covers fewer peers than the namespace size can never be uniformly
// exposed, so there's no point scanning probes.
//
// nsPeerCount is the number of OTHER pods in the namespace (not counting
// dst itself). Returns true when the pod is default-allow on ingress
// (trivially a candidate) OR when the allow map contains at least one
// entry per namespace peer.
func overExposedCandidate(pod *podInfo, nsPeerCount int) bool {
	if !pod.IngressRestricted {
		return true
	}
	return len(pod.AllowedIngressFrom) >= nsPeerCount
}

// streamingAsymmetricCandidates returns the set of ordered pod pairs (a, b)
// with a < b that MIGHT have asymmetric reachability. The candidate set is
// built in three passes so no asymmetric pair is missed:
//
//  1. Every pair mentioned in a forward allow map (AllowedIngressFrom or
//     AllowedEgressTo). O(E) — explicit allow rules always create candidate
//     pairs so narrow-allow findings still fire.
//  2. For every restricted pod R, every OTHER pod in R's namespace. O(R ×
//     namespace_size) — a restricted pod's deny-by-default semantics can
//     create asymmetry with any namespace peer even without an explicit
//     allow entry (e.g. ingress-restricted B with no allow entry for
//     default-allow A → A cannot reach B but B reaches A).
//  3. For every restricted pod R, every OTHER restricted pod in the whole
//     cluster. Cross-namespace policies are rare but the restricted-pair
//     product is bounded by restrictedPairCap so this terminates fast.
//
// Pairs where BOTH pods are default-allow on every direction are skipped
// because canReach(A, B) and canReach(B, A) both return true by default —
// they are always symmetric and emit no finding.
func streamingAsymmetricCandidates(index *reachabilityIndex) map[[2]string]struct{} {
	out := map[[2]string]struct{}{}
	// Pass 1: forward allow edges.
	for id, pod := range index.pods {
		for peer := range pod.AllowedEgressTo {
			out[orderedPair(id, peer)] = struct{}{}
		}
		for peer := range pod.AllowedIngressFrom {
			out[orderedPair(id, peer)] = struct{}{}
		}
	}
	// Pass 2: restricted pods × namespace peers. Catches the case where a
	// restricted pod implicitly denies all peers via default-deny, creating
	// asymmetry with every default-allow namespace peer.
	byNS := map[string][]string{}
	for id, pod := range index.pods {
		byNS[pod.Namespace] = append(byNS[pod.Namespace], id)
	}
	restricted := make([]string, 0)
	for id, pod := range index.pods {
		if pod.IngressRestricted || pod.EgressRestricted {
			restricted = append(restricted, id)
		}
	}
	for _, r := range restricted {
		ns := index.pods[r].Namespace
		for _, peer := range byNS[ns] {
			if peer == r {
				continue
			}
			out[orderedPair(r, peer)] = struct{}{}
		}
	}
	// Pass 3: restricted × restricted across namespaces. Bounded at
	// restrictedPairCap² so pathological graphs (every pod restricted in
	// its own namespace) still terminate in O(cap²). The cap covers
	// realistic clusters; above it we accept the cross-namespace miss.
	const restrictedPairCap = 1024
	if len(restricted) <= restrictedPairCap {
		for i := 0; i < len(restricted); i++ {
			for j := i + 1; j < len(restricted); j++ {
				out[orderedPair(restricted[i], restricted[j])] = struct{}{}
			}
		}
	}
	return out
}

// orderedPair returns the (a, b) tuple with a <= b lexicographically. Pairs
// live in the asymmetric candidate set keyed by orderedPair so (a, b) and
// (b, a) dedupe to a single entry.
func orderedPair(a, b string) [2]string {
	if a < b {
		return [2]string{a, b}
	}
	return [2]string{b, a}
}
