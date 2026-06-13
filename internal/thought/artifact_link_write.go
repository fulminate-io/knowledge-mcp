// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// artifact_link_write.go holds the clique WRITE half of the artifact-link phase: given
// the shared treeResolution bundle (computed ONCE for the whole pass by
// resolveArtifactsAndRoots and threaded in by the lever), it computes the per-artifact
// full clique (computeArtifactLinkEdges, artifact_link.go), writes it in ONE batched
// create_batch, and orchestrates the pass (runArtifactLinkPhase). Mirrors
// tree_link_write.go end-to-end; split out of artifact_link.go to keep each file under
// the 500-line cap.

// artifactLinkMethod tags every relates-to edge the artifact-link phase writes, so it is
// distinguishable from authored relates-to edges AND from the densify ("topic-densify"),
// medoid ("topic-similarity"), and tree-link ("tree-link") machine edges — its OWN
// distinct provenance so the four machine-edge origins are independently identifiable for
// cleanup or tension exclusion.
const artifactLinkMethod = "artifact-link"

// artifactLinkEdgeConfidence is the LOW explicit Confidence stamped on every artifact-link
// edge to MARK its machine origin (below the authored-edge convention: a bare authored
// mutate(link) leaves Confidence 0). Same value + consumer stance as treeLinkEdgeConfidence
// (tree_link.go:67): NO current consumer reads edge Confidence/Method for thought-graph
// reflection/trust/clustering, so discounting machine edges is NOTED AS AVAILABLE (the
// metadata is present + discountable the moment a consumer reads it) but is NOT WIRED NOW.
// A per-channel named const (matching the densify/tree-link convention) keeps the 0.25
// literal self-documenting at the write site.
const artifactLinkEdgeConfidence = 0.25

// ArtifactLinkStat is the per-artifact artifact-link accounting the lever report renders:
// the shared artifact's id+name, the count of thoughts grouped under it this pass, and the
// count of clique edges actually written for it (post-idempotency, so a re-run over an
// already-linked artifact reports zero EdgesWritten while still naming the artifact).
type ArtifactLinkStat struct {
	ArtifactID   string
	ArtifactName string
	ThoughtCount int
	EdgesWritten int
}

// writeArtifactLinkEdges materializes the clique pairs as relates-to edges in ONE batched
// mutate(create_batch) Execute — NOT a per-edge loop (the load-bearing 1-RPC bound). Every
// edge is stamped Type=relates-to, Method="artifact-link",
// Confidence=artifactLinkEdgeConfidence so it is provenance-identifiable. Both endpoints
// are existing thoughts, so the batch carries edges-only (from_id/to_id, no node bodies).
// An empty pair set is a no-op (no Execute). Returns the count written. A thin per-Method
// twin of writeTreeLinkEdges (tree_link_write.go:100) rather than a parameterization, so
// each phase's provenance + error strings stay self-documenting.
func writeArtifactLinkEdges(ctx context.Context, gc Caller, pairs []artifactLinkCandidate) (int, error) {
	if len(pairs) == 0 {
		return 0, nil
	}
	edges := make([]map[string]any, 0, len(pairs))
	for _, p := range pairs {
		edges = append(edges, map[string]any{
			"from_id":    p.A,
			"to_id":      p.B,
			"type":       string(kgtypes.EdgeRelatesTo),
			"method":     artifactLinkMethod,
			"confidence": artifactLinkEdgeConfidence,
		})
	}
	args, err := json.Marshal(map[string]any{
		"operation": "create_batch",
		"edges":     edges,
	})
	if err != nil {
		return 0, fmt.Errorf("thought: writeArtifactLinkEdges: marshal create_batch: %w", err)
	}
	if _, err := executeViaEngine(ctx, gc, "mutate", args); err != nil {
		return 0, fmt.Errorf("thought: writeArtifactLinkEdges: edge write: %w", err)
	}
	return len(pairs), nil
}

// runArtifactLinkPhase materializes the per-shared-artifact clique of relates-to edges
// over the SHARED resolution bundle — the SAME treeResolution the tree-link phase used
// this pass, passed IN so the phase does NO re-resolution and issues NO new bulk read
// beyond the one idempotency pre-read. It collects the union of member thoughts across
// the gate-qualifying artifacts, does ONE bulk any-provenance idempotency pre-read,
// computes the clique edge set in memory (computeArtifactLinkEdges), writes every edge in
// ONE batched create_batch, and fills the report's artifact-link fields with one loud
// per-artifact stat. Every per-stage failure (write) is recorded via addStageError and
// the pass CONTINUES — an artifact-link failure never aborts the lever. Mirrors
// runTreeLinkPhase (tree_link_write.go:135), differing only in that it consumes the
// passed-in bundle rather than calling ResolveTreeRoots itself.
func (p *PropagationLoop) runArtifactLinkPhase(ctx context.Context, bundle treeResolution, rep *SimilarityReport) {
	// The union of member thoughts across the gate-qualifying artifacts — only thoughts
	// attached to a qualifying shared artifact can form a clique pair, so the idempotency
	// pre-read is scoped to exactly them.
	memberSet := map[string]struct{}{}
	for tid, arts := range bundle.artifactsByThought {
		for _, a := range arts {
			if qualifiesForArtifactLink(a, bundle) {
				memberSet[tid] = struct{}{}
				break
			}
		}
	}
	if len(memberSet) == 0 {
		return // no qualifying shared artifact this pass — nothing to do, nothing to report
	}
	members := make([]string, 0, len(memberSet))
	for tid := range memberSet {
		members = append(members, tid)
	}

	// ONE bulk any-provenance relates-to idempotency pre-read over the member thought set.
	existing, eerr := fetchExistingPairs(ctx, p.gc, members)
	if eerr != nil {
		rep.addStageError("artifact-link idempotency pre-read failed; treating as no existing edges", eerr)
		existing = map[string]bool{}
	}

	pairs := computeArtifactLinkEdges(bundle, existing)

	written, werr := writeArtifactLinkEdges(ctx, p.gc, pairs)
	if werr != nil {
		rep.addStageError("artifact-link edge write failed", werr)
	}

	fillArtifactLinkReport(rep, bundle, pairs, written)
}

// fillArtifactLinkReport attributes the grouped thoughts + written edges back to their
// shared artifact and appends one loud ArtifactLinkStat per qualifying artifact (≥2
// thoughts) in deterministic artifact-ID order, then records the run total. Mirrors
// fillTreeLinkReport (tree_link_write.go:182).
func fillArtifactLinkReport(rep *SimilarityReport, bundle treeResolution, pairs []artifactLinkCandidate, written int) {
	// Members per qualifying artifact (distinct), for the per-artifact thought count.
	membersByArtifact := map[string][]string{}
	for tid, arts := range bundle.artifactsByThought {
		for _, a := range arts {
			if qualifiesForArtifactLink(a, bundle) {
				membersByArtifact[a] = append(membersByArtifact[a], tid)
			}
		}
	}

	// Edges this pass per artifact: a pair belongs to every qualifying artifact that BOTH
	// its endpoints share. Count it under each such artifact so the per-artifact
	// EdgesWritten reflects what this pass wrote for that artifact's clique.
	edgesByArtifact := map[string]int{}
	for _, pr := range pairs {
		for a, mem := range membersByArtifact {
			set := make(map[string]bool, len(mem))
			for _, m := range mem {
				set[m] = true
			}
			if set[pr.A] && set[pr.B] {
				edgesByArtifact[a]++
			}
		}
	}

	artifacts := make([]string, 0, len(membersByArtifact))
	for a := range membersByArtifact {
		artifacts = append(artifacts, a)
	}
	sort.Strings(artifacts)
	for _, a := range artifacts {
		count := len(dedupeSorted(membersByArtifact[a]))
		if count < 2 {
			continue // a single-thought artifact forms no clique — not a grouping artifact
		}
		name := ""
		if n, ok := bundle.artifactByID[a]; ok {
			name = n.GetSymbolName()
		}
		rep.ArtifactLinkPerArtifact = append(rep.ArtifactLinkPerArtifact, ArtifactLinkStat{
			ArtifactID:   a,
			ArtifactName: name,
			ThoughtCount: count,
			EdgesWritten: edgesByArtifact[a],
		})
	}
	rep.ArtifactLinkEdgesTotal = written
}
