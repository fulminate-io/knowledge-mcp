// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// labelSelectorFromLS converts a metav1.LabelSelector (parsed from a
// NetworkPolicy spec) to an apimachinery labels.Selector. A nil input
// returns labels.Nothing() (matches nothing) — this is the K8s semantics
// for an absent selector inside a peer. An empty but non-nil selector
// (`{}`) returns labels.Everything() which matches all label sets
// including the empty set.
func labelSelectorFromLS(ls *metav1.LabelSelector) (labels.Selector, error) {
	if ls == nil {
		return labels.Nothing(), nil
	}
	return metav1.LabelSelectorAsSelector(ls)
}

// podsMatching returns every podEntry in podIndex whose namespace equals
// ns and whose labels satisfy sel. A nil selector matches nothing (use
// labels.Everything() to match all).
func podsMatching(podIndex []podEntry, ns string, sel labels.Selector) []podEntry {
	if sel == nil {
		return nil
	}
	var out []podEntry
	for _, p := range podIndex {
		if p.namespace != ns {
			continue
		}
		if sel.Matches(labels.Set(p.labels)) {
			out = append(out, p)
		}
	}
	return out
}

// namespacesMatching returns every namespace name whose labels satisfy
// nsSel. Unlabeled namespaces (present in nsLabelIndex with an empty map)
// are matched when nsSel is labels.Everything() — this mirrors the K8s
// semantics of `namespaceSelector: {}` matching all namespaces including
// unlabeled ones. Match-criteria selectors (e.g. matchLabels or
// matchExpressions) naturally skip unlabeled namespaces because no label
// key can satisfy the requirement.
func namespacesMatching(nsLabelIndex map[string]map[string]string, nsSel labels.Selector) []string {
	if nsSel == nil {
		return nil
	}
	var out []string
	for name, lbls := range nsLabelIndex {
		if nsSel.Matches(labels.Set(lbls)) {
			out = append(out, name)
		}
	}
	return out
}

// resolvePeers turns a slice of NetworkPolicy peers into a flat list of
// concrete pod entries. Peer semantics:
//
//   - ipBlock set: skipped (analyzer handles ipBlock)
//   - podSelector only: pods in policyNS matching the selector
//   - namespaceSelector only: every pod in every namespace matching nsSel
//   - both: pods in namespaces matching nsSel, further filtered by podSel
//   - neither (empty peer): no pods emitted (K8s treats this as malformed;
//     the outer "empty from[]" allow-all case is not reached here because
//     resolvePeers is only called with at least one peer)
//
// Duplicate pods (same id) are de-duped so a single (target, source) pair
// is emitted at most once per rule. Per-branch resolution is delegated to
// resolvePeer so the outer loop stays low-complexity.
func resolvePeers(
	podIndex []podEntry,
	nsLabelIndex map[string]map[string]string,
	policyNS string,
	peers []npPeer,
) ([]podEntry, error) {
	if len(peers) == 0 {
		return nil, nil
	}

	seen := make(map[string]struct{})
	var out []podEntry
	appendPod := func(p podEntry) {
		if _, ok := seen[p.id]; ok {
			return
		}
		seen[p.id] = struct{}{}
		out = append(out, p)
	}

	for _, peer := range peers {
		if peer.IPBlock != nil {
			// ipBlock peers are handled by the topology analyzer, not the
			// collector — skip for pod-to-pod edge emission.
			continue
		}
		if err := resolvePeer(podIndex, nsLabelIndex, policyNS, peer, appendPod); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// resolvePeer dispatches a single (non-ipBlock) peer to the correct
// per-shape helper and routes matched pods through emit. Splitting this
// out of resolvePeers keeps the outer loop's cognitive complexity bounded.
func resolvePeer(
	podIndex []podEntry,
	nsLabelIndex map[string]map[string]string,
	policyNS string,
	peer npPeer,
	emit func(podEntry),
) error {
	podSel, err := labelSelectorFromLS(peer.PodSelector)
	if err != nil {
		return err
	}
	nsSel, err := labelSelectorFromLS(peer.NamespaceSelector)
	if err != nil {
		return err
	}

	switch {
	case peer.PodSelector != nil && peer.NamespaceSelector != nil:
		resolveCombinedSelectorPeers(podIndex, nsLabelIndex, nsSel, podSel, emit)
	case peer.PodSelector != nil:
		resolvePodSelectorPeers(podIndex, policyNS, podSel, emit)
	case peer.NamespaceSelector != nil:
		resolveNamespaceSelectorPeers(podIndex, nsLabelIndex, nsSel, emit)
	}
	// Empty peer with no selectors and no ipBlock falls through — skipped.
	return nil
}

// resolveCombinedSelectorPeers handles the "podSelector AND namespaceSelector"
// branch: pods in every matching namespace further filtered by the pod
// selector.
func resolveCombinedSelectorPeers(
	podIndex []podEntry,
	nsLabelIndex map[string]map[string]string,
	nsSel, podSel labels.Selector,
	emit func(podEntry),
) {
	for _, ns := range namespacesMatching(nsLabelIndex, nsSel) {
		for _, p := range podsMatching(podIndex, ns, podSel) {
			emit(p)
		}
	}
}

// resolvePodSelectorPeers handles the "podSelector only" branch: pods in the
// policy's own namespace matching the selector.
func resolvePodSelectorPeers(
	podIndex []podEntry,
	policyNS string,
	podSel labels.Selector,
	emit func(podEntry),
) {
	for _, p := range podsMatching(podIndex, policyNS, podSel) {
		emit(p)
	}
}

// resolveNamespaceSelectorPeers handles the "namespaceSelector only" branch:
// every pod in every namespace matched by nsSel.
func resolveNamespaceSelectorPeers(
	podIndex []podEntry,
	nsLabelIndex map[string]map[string]string,
	nsSel labels.Selector,
	emit func(podEntry),
) {
	nsNames := namespacesMatching(nsLabelIndex, nsSel)
	nsSet := make(map[string]struct{}, len(nsNames))
	for _, ns := range nsNames {
		nsSet[ns] = struct{}{}
	}
	for _, p := range podIndex {
		if _, ok := nsSet[p.namespace]; ok {
			emit(p)
		}
	}
}
