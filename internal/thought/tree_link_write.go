// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// tree_link_write.go holds the clique WRITE half of the tree-link phase: given the
// thought→tree-root grouping that ResolveTreeRoots (tree_link.go) produces, it computes
// the per-tree full clique, writes it in ONE batched create_batch, and orchestrates the
// pass (runTreeLinkPhase). Split out of tree_link.go to keep each file under the
// 500-line cap; the resolution contract stays in tree_link.go.

// TreeLinkTreeStat is the per-tree tree-link accounting the lever report renders: the
// tree's root id+name, the count of thoughts grouped under that root this pass, and the
// count of clique edges actually written for it (post-idempotency, so a re-run over an
// already-linked tree reports zero EdgesWritten while still naming the tree).
type TreeLinkTreeStat struct {
	RootID       string
	RootName     string
	ThoughtCount int
	EdgesWritten int
}

// treeLinkCandidate is one undirected clique-member pair (A < B canonically). It
// becomes a relates-to edge unless the idempotency filter drops it. Unlike
// densifyCandidate (densify.go:30) it carries NO similarity score — clique membership
// is purely STRUCTURAL (two thoughts share a work-item tree), not similarity-graded.
type treeLinkCandidate struct {
	A, B string
}

// computeTreeLinkEdges groups thought IDs by their shared work-item tree root and, for
// each tree with ≥2 thoughts, emits the FULL CLIQUE of unordered member pairs
// (N tree thoughts → N(N−1)/2 pairs), canonicalized (A < B) and deduped via
// unorderedPairKey (similarity.go:310), then drops any pair already present in the
// existing any-provenance/either-direction set (idempotency, same logic as
// dropExisting densify.go:162) so a re-run over an already-linked tree emits zero.
//
// DETERMINISM: tree roots are sorted, each tree's member IDs are sorted, and pairs are
// emitted in (A,B) sorted order — identical inputs produce an identical edge set in
// identical order (mirrors the densify selector's sorted output, densify.go:110-120).
//
// NO PER-RUN EDGE BUDGET (intentional, unlike densify's kNN budget): a full clique IS
// the chosen topology and the work-item trees are small and bounded (measured: a few
// dozen trees, no oversized tree), so there is no runaway edge count to cap — the
// one-time clique totals converge and subsequent passes write only the incremental
// top-up the idempotency filter leaves. The pure in-memory selector does NO wire I/O.
func computeTreeLinkEdges(rootByThought map[string]string, existing map[string]bool) []treeLinkCandidate {
	// Invert rootByThought → members per tree root.
	membersByRoot := map[string][]string{}
	for tid, root := range rootByThought {
		membersByRoot[root] = append(membersByRoot[root], tid)
	}

	// Deterministic root order.
	roots := make([]string, 0, len(membersByRoot))
	for root := range membersByRoot {
		roots = append(roots, root)
	}
	sort.Strings(roots)

	var out []treeLinkCandidate
	for _, root := range roots {
		members := membersByRoot[root]
		if len(members) < 2 {
			continue // a single-thought tree forms no pair
		}
		sort.Strings(members)
		for i := range members {
			for j := i + 1; j < len(members); j++ {
				a, b := members[i], members[j]
				if a > b {
					a, b = b, a
				}
				if existing[unorderedPairKey(a, b)] {
					continue // already joined (any provenance, either direction) — idempotent
				}
				out = append(out, treeLinkCandidate{A: a, B: b})
			}
		}
	}
	return out
}

// writeTreeLinkEdges materializes the clique pairs as relates-to edges in ONE batched
// mutate(create_batch) Execute — NOT a per-edge loop (the load-bearing 1-RPC bound).
// Every edge is stamped Type=relates-to, Method="tree-link",
// Confidence=treeLinkEdgeConfidence so it is provenance-identifiable. Both endpoints
// are existing thoughts, so the batch carries edges-only (from_id/to_id, no node
// bodies). An empty pair set is a no-op (no Execute). Returns the count written. A thin
// per-Method twin of writeDensifyEdges (densify.go:331) rather than a parameterization,
// so each phase's provenance + error strings stay self-documenting.
func writeTreeLinkEdges(ctx context.Context, gc Caller, pairs []treeLinkCandidate) (int, error) {
	if len(pairs) == 0 {
		return 0, nil
	}
	edges := make([]map[string]any, 0, len(pairs))
	for _, p := range pairs {
		edges = append(edges, map[string]any{
			"from_id":    p.A,
			"to_id":      p.B,
			"type":       string(kgtypes.EdgeRelatesTo),
			"method":     treeLinkMethod,
			"confidence": treeLinkEdgeConfidence,
		})
	}
	args, err := json.Marshal(map[string]any{
		"operation": "create_batch",
		"edges":     edges,
	})
	if err != nil {
		return 0, fmt.Errorf("thought: writeTreeLinkEdges: marshal create_batch: %w", err)
	}
	if _, err := executeViaEngine(ctx, gc, "mutate", args); err != nil {
		return 0, fmt.Errorf("thought: writeTreeLinkEdges: edge write: %w", err)
	}
	return len(pairs), nil
}

// runStructuralCliquePhases is the FINAL lever stage: it resolves the artifact/root
// bundle ONCE for the raw thought set (resolveArtifactsAndRoots — the single resolution
// shared by both clique channels), then runs the tree-link clique FIRST and the
// artifact-link clique STRICTLY AFTER, both off the SAME bundle. The ordering matters
// for the same reason tree-link runs after densify: artifact-link's idempotency pre-read
// must see tree-link's committed edges, so a thought pair that tree-link already joined
// is not re-written by artifact-link. A nil/empty thought set is a no-op; a resolution
// error is recorded once and skips BOTH clique phases (neither can run without the
// bundle). Mirrors the densify→tree-link ordering rationale (similarity_lever.go).
func (p *PropagationLoop) runStructuralCliquePhases(ctx context.Context, clusters []ThoughtCluster, rep *SimilarityReport) {
	// Flatten clusters → the deduped raw thought set.
	thoughtSet := map[string]struct{}{}
	for _, c := range clusters {
		for _, id := range c.ThoughtIDs {
			thoughtSet[id] = struct{}{}
		}
	}
	if len(thoughtSet) == 0 {
		return
	}
	allThoughtIDs := make([]string, 0, len(thoughtSet))
	for id := range thoughtSet {
		allThoughtIDs = append(allThoughtIDs, id)
	}

	// ONE resolution for the whole pass — threaded to BOTH clique phases.
	bundle, rerr := resolveArtifactsAndRoots(ctx, p.gc, allThoughtIDs)
	if rerr != nil {
		rep.addStageError("structural-clique root resolution failed; no tree-link or artifact-link edges written", rerr)
		return
	}

	p.runTreeLinkPhase(ctx, bundle, rep)
	p.runArtifactLinkPhase(ctx, bundle, rep)
}

// runTreeLinkPhase materializes the per-work-item-tree clique of relates-to edges from
// the SHARED resolution bundle (resolveArtifactsAndRoots, computed ONCE for the pass and
// passed in by runStructuralCliquePhases — NO re-resolution here). It does ONE bulk
// any-provenance idempotency pre-read over the rooted thoughts, computes the clique edge
// set in memory, writes every edge in ONE batched create_batch, and fills the report's
// tree-link fields with one loud per-tree stat. Every per-stage failure (write) is
// recorded via addStageError and the pass CONTINUES — a tree-link failure never aborts
// the lever. Mirrors runArtifactLinkPhase (artifact_link_write.go), differing only in the
// grouping key (work-item tree root vs shared standalone artifact).
func (p *PropagationLoop) runTreeLinkPhase(ctx context.Context, bundle treeResolution, rep *SimilarityReport) {
	rootByThought, rootNames := bundle.rootByThought, bundle.rootNames

	// ONE bulk any-provenance relates-to idempotency pre-read over the resolved thought
	// set (only thoughts that landed in a tree can form clique pairs).
	rootedThoughts := make([]string, 0, len(rootByThought))
	for tid := range rootByThought {
		rootedThoughts = append(rootedThoughts, tid)
	}
	existing, eerr := fetchExistingPairs(ctx, p.gc, rootedThoughts)
	if eerr != nil {
		rep.addStageError("tree-link idempotency pre-read failed; treating as no existing edges", eerr)
		existing = map[string]bool{}
	}

	pairs := computeTreeLinkEdges(rootByThought, existing)

	written, werr := writeTreeLinkEdges(ctx, p.gc, pairs)
	if werr != nil {
		rep.addStageError("tree-link edge write failed", werr)
	}

	fillTreeLinkReport(rep, rootByThought, rootNames, pairs, written)
}

// fillTreeLinkReport attributes the grouped thoughts + written edges back to their tree
// roots and appends one loud TreeLinkTreeStat per grouping tree (≥2 thoughts) in
// deterministic root order, then records the run total.
func fillTreeLinkReport(rep *SimilarityReport, rootByThought, rootNames map[string]string, pairs []treeLinkCandidate, written int) {
	thoughtCountByRoot := map[string]int{}
	for _, root := range rootByThought {
		thoughtCountByRoot[root]++
	}
	// A pair's tree is the (shared) root of both endpoints, so count an edge under the
	// root of its A endpoint.
	edgesByRoot := map[string]int{}
	for _, pr := range pairs {
		edgesByRoot[rootByThought[pr.A]]++
	}

	roots := make([]string, 0, len(thoughtCountByRoot))
	for root, n := range thoughtCountByRoot {
		if n >= 2 {
			roots = append(roots, root)
		}
	}
	sort.Strings(roots)
	for _, root := range roots {
		rep.TreeLinkPerTree = append(rep.TreeLinkPerTree, TreeLinkTreeStat{
			RootID:       root,
			RootName:     rootNames[root],
			ThoughtCount: thoughtCountByRoot[root],
			EdgesWritten: edgesByRoot[root],
		})
	}
	rep.TreeLinkEdgesTotal = written
}
