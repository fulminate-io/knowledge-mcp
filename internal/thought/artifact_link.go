// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"sort"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// artifact_link.go owns the COMPUTE half of the lever's artifact-link phase: the
// channel that ALWAYS links thoughts sharing a real STANDALONE artifact
// (decision/research/rule/finding/document/question) that does NOT resolve to a
// work-item tree root — the complement of tree-link, which owns the work-item-rooted
// artifacts. It mirrors tree_link.go's resolution-vs-write split: the exclusion
// predicate + the in-memory clique selector live here; the write + phase orchestration
// (writeArtifactLinkEdges / runArtifactLinkPhase / fillArtifactLinkReport) live in
// artifact_link_write.go. Both files stay under the 500-line cap.
//
// The phase consumes the SAME treeResolution bundle the tree-link phase already
// resolved this pass (artifactsByThought + artifactByID + resolvedArtifactRoot +
// rootNodeByID), so it issues NO new bulk read beyond the one idempotency pre-read —
// the resolution is shared, not repeated.

// realArtifactTypes is the set of REAL standalone knowledge-artifact types whose shared
// reference is a deliberate "these thoughts belong together" signal: a hand-authored
// decision/research/rule/finding/document/question. Two thoughts informed-by the same
// decision are as deliberately co-referenced as two thoughts in the same work-item tree.
// Hoisted to package scope (allocated ONCE, never per artifact). The constants are the
// kgtypes node types verified against node_types.go: NodeDecision/NodeResearch/NodeRule/
// NodeFinding/NodeDocument/NodeQuestion. Work-item container types (project/ticket/plan/
// phase/step) are deliberately ABSENT — a thought sharing one of those is tree-link's
// domain and is excluded here by the work-item-root gate in computeArtifactLinkEdges.
var realArtifactTypes = map[kgtypes.NodeType]bool{
	kgtypes.NodeDecision: true,
	kgtypes.NodeResearch: true,
	kgtypes.NodeRule:     true,
	kgtypes.NodeFinding:  true,
	kgtypes.NodeDocument: true,
	kgtypes.NodeQuestion: true,
}

// analyzerFindingTitlePrefixes is the LOAD-BEARING exclusion marker for auto-generated
// topology-analyzer findings: a finding whose SymbolName starts with one of these
// prefixes is machine-generated (the topology graph analyzers stamp these titles, e.g.
// articulation.go's "Articulation point: <id>"). Such findings would otherwise form
// dense spurious cliques among every thought that happens to touch the same analyzer
// output, so they are excluded from this channel (the separate analyzer-finding CLEANUP
// is its own ticket — this channel only declines to link on them). Hoisted to package
// scope (allocated ONCE, never per call).
//
// PREFIX = LOAD-BEARING; metadata = FORWARD-COMPATIBLE. The SymbolName prefix is the
// verified exclusion path: it is what the analyzer source actually stamps on the
// finding title today. The `algorithm` metadata branch in isAnalyzerFinding is a CHEAP
// FORWARD-COMPATIBLE OR — no production path was confirmed to persist analyzer findings
// to the knowledge graph carrying that key, so it is NOT relied upon as the marker; it
// is kept so the future cleanup ticket can stamp an explicit `algorithm` marker and have
// this channel honor it without a code change. The fails-when-absent test gates on a
// prefix-only fixture (no algorithm metadata) being excluded.
var analyzerFindingTitlePrefixes = []string{
	"Articulation point:",
}

// isAnalyzerFinding reports whether n is an auto-generated topology-analyzer finding
// that must be EXCLUDED from the artifact-link channel. It is a single standalone
// predicate (the swap point for the future analyzer-finding cleanup ticket): the marker
// definition lives here and nowhere else, so the cleanup ticket changes ONLY this
// function + analyzerFindingTitlePrefixes.
//
// Only `finding` nodes can be analyzer findings — any other type returns false
// immediately. A finding is an analyzer finding when EITHER:
//   - its SymbolName starts with a known analyzer title prefix (LOAD-BEARING — what the
//     topology analyzers actually stamp today); OR
//   - it carries a non-empty `algorithm` metadata key (FORWARD-COMPATIBLE — honored if a
//     future cleanup ticket stamps it, but not a verified writer contract today).
func isAnalyzerFinding(n *knowledgev1.Node) bool {
	if n == nil || kgtypes.NodeType(n.GetType()) != kgtypes.NodeFinding {
		return false
	}
	name := n.GetSymbolName()
	for _, prefix := range analyzerFindingTitlePrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	// Forward-compatible OR: a future cleanup ticket may stamp an explicit `algorithm`
	// marker on persisted analyzer findings; honor it now so no code change is needed
	// then. NOT relied upon as the load-bearing marker (the prefix above is).
	if n.GetMetadata()["algorithm"] != "" {
		return true
	}
	return false
}

// artifactLinkCandidate is one undirected clique-member pair (A < B canonically) sharing
// a real standalone artifact. It becomes a relates-to edge unless the idempotency filter
// drops it. Like treeLinkCandidate (tree_link_write.go:35) it carries NO similarity score
// — clique membership is purely STRUCTURAL (two thoughts share a real artifact), not
// similarity-graded.
type artifactLinkCandidate struct {
	A, B string
}

// computeArtifactLinkEdges groups the bundle's thought→artifact attachments by shared
// real STANDALONE artifact and, for each qualifying artifact with ≥2 attached thoughts,
// emits the FULL CLIQUE of unordered member pairs (N thoughts → N(N−1)/2 pairs),
// canonicalized (A < B) via unorderedPairKey (similarity.go:310) and deduped against the
// existing any-provenance/either-direction set (idempotency). It mirrors
// computeTreeLinkEdges (tree_link_write.go:55) but groups by ARTIFACT with a 3-way
// per-artifact gate instead of by precomputed tree root.
//
// PER-ARTIFACT GATE — an artifact contributes a clique only when ALL hold:
//
//	(1) REAL TYPE: artifactByID[a]'s type is in realArtifactTypes
//	    (decision/research/rule/finding/document/question);
//	(2) NOT WORK-ITEM-ROOTED: the artifact does NOT resolve to a work-item tree root —
//	    rootNodeByID[resolvedArtifactRoot[a]]'s type is NOT in treeRootEligibleTypes.
//	    A work-item-rooted artifact (e.g. a finding a ticket contains) is tree-link's
//	    domain; linking on it here would DOUBLE-LINK the same thoughts;
//	(3) NOT AN ANALYZER FINDING: !isAnalyzerFinding(artifactByID[a]).
//
// DETERMINISM: artifact IDs are sorted, each artifact's member thoughts are sorted, and
// pairs are emitted in (A,B) sorted order — identical inputs produce an identical edge
// set in identical order (the same discipline as computeTreeLinkEdges). The pure
// in-memory selector does NO wire I/O.
//
// NO PER-RUN EDGE BUDGET (intentional, as in tree-link): a full clique IS the chosen
// topology and a real artifact's attached-thought set is small and bounded, so there is
// no runaway edge count to cap — the one-time clique totals converge and later passes
// write only the incremental top-up the idempotency filter leaves.
func computeArtifactLinkEdges(bundle treeResolution, existing map[string]bool) []artifactLinkCandidate {
	// Invert artifactsByThought → members per artifact, ONLY for artifacts passing the
	// 3-way channel gate.
	membersByArtifact := map[string][]string{}
	for tid, arts := range bundle.artifactsByThought {
		for _, a := range arts {
			if !qualifiesForArtifactLink(a, bundle) {
				continue
			}
			membersByArtifact[a] = append(membersByArtifact[a], tid)
		}
	}

	// Deterministic artifact order.
	artifacts := make([]string, 0, len(membersByArtifact))
	for a := range membersByArtifact {
		artifacts = append(artifacts, a)
	}
	sort.Strings(artifacts)

	var out []artifactLinkCandidate
	for _, a := range artifacts {
		members := dedupeSorted(membersByArtifact[a])
		if len(members) < 2 {
			continue // a single-thought artifact forms no pair
		}
		for i := range members {
			for j := i + 1; j < len(members); j++ {
				x, y := members[i], members[j]
				if x > y {
					x, y = y, x
				}
				if existing[unorderedPairKey(x, y)] {
					continue // already joined (any provenance, either direction) — idempotent
				}
				out = append(out, artifactLinkCandidate{A: x, B: y})
			}
		}
	}
	return out
}

// qualifiesForArtifactLink applies the 3-way per-artifact channel gate (real type, not
// work-item-rooted, not an analyzer finding) to a single artifact ID against the shared
// resolution bundle. See computeArtifactLinkEdges for the gate rationale.
func qualifiesForArtifactLink(a string, bundle treeResolution) bool {
	n, ok := bundle.artifactByID[a]
	if !ok || !realArtifactTypes[kgtypes.NodeType(n.GetType())] {
		return false // (1) not a real standalone artifact type
	}
	// (2) work-item-rooted artifacts are tree-link's domain — exclude.
	if root, ok := bundle.resolvedArtifactRoot[a]; ok {
		if rn, ok := bundle.rootNodeByID[root]; ok && treeRootEligibleTypes[kgtypes.NodeType(rn.GetType())] {
			return false
		}
	}
	// (3) auto-generated analyzer findings are excluded (cleanup is a separate ticket).
	return !isAnalyzerFinding(n)
}

// dedupeSorted returns the distinct members of ids in sorted order (a small in-memory
// helper so each artifact's clique is computed over distinct, deterministically ordered
// thoughts — the same input shape computeTreeLinkEdges gets from its already-deduped
// rootByThought inversion).
func dedupeSorted(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
