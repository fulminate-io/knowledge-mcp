package structtree

import (
	"log/slog"
	"sort"
	"strconv"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/layout"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/text"
)

// HybridFallback merges a tagged-region walk with heuristic blocks
// for the untagged residue of the same page. Used by the top-level
// pdf package when a partially-tagged document mixes /StructTreeRoot
// content with prose the producer didn't tag (very common — figures
// and captions are often tagged while surrounding body prose isn't).
//
// Input contract:
//   - taggedBlocks: result of Walk(ctx, info.PageIndex) for this
//     page; already pageFilter-pruned at emit time inside the walker.
//     The caller does NOT pass a whole-doc walk + post-filter.
//   - allRuns: the page's full TextRun set, including untagged runs
//     (MCID == 0) and any anomalous orphan-MCID runs (MCID > 0 but
//     not referenced by any structure element).
//   - info: the page's PageInfo (PageIndex / MediaBox / Rotation).
//
// Algorithm:
//  1. Build reachable-MCID set from taggedBlocks (parsed from the
//     "mcids" metadata stamp Phase 4 emits).
//  2. Partition allRuns into residue: runs with MCID == 0 (untagged
//     contract) OR MCID > 0 but not in the reachable set (orphan;
//     surfaces a debug warning).
//  3. If residue is empty, return taggedBlocks unchanged.
//  4. Cluster residue via layout.ClusterWithParams using
//     layout.DefaultLayoutParams. Resulting blocks carry empty
//     StructRole — that's the heuristic-origin marker.
//  5. Tagged-wins overlap rule (open_question Q2 locked option a):
//     drop any residue block whose bbox intersects any tagged block.
//  6. Merge tagged + filtered residue by Y0 ascending; tie-break by
//     X0 ascending. Return.
func HybridFallback(taggedBlocks []layout.Block, allRuns []text.TextRun, info layout.PageInfo) ([]layout.Block, error) {
	reachable := reachableMCIDs(taggedBlocks)
	residueRuns := partitionResidue(allRuns, reachable)
	if len(residueRuns) == 0 {
		return taggedBlocks, nil
	}
	residue, err := layout.ClusterWithParams(residueRuns, info, layout.DefaultLayoutParams)
	if err != nil {
		return nil, err
	}
	residue = dropOverlapping(residue, taggedBlocks)
	if len(residue) == 0 {
		return taggedBlocks, nil
	}
	merged := make([]layout.Block, 0, len(taggedBlocks)+len(residue))
	merged = append(merged, taggedBlocks...)
	merged = append(merged, residue...)
	mergeYAscending(merged)
	return merged, nil
}

// reachableMCIDs collects every MCID referenced by any taggedBlock's
// "mcids" metadata stamp into a set. Empty blocks (figures with no
// MCIDs, walk-throughs that emitted nothing) contribute nothing.
func reachableMCIDs(blocks []layout.Block) map[int]struct{} {
	out := make(map[int]struct{})
	for _, b := range blocks {
		for _, id := range mcidsFromBlock(b) {
			out[id] = struct{}{}
		}
	}
	return out
}

// partitionResidue returns the runs that should pass through the
// heuristic clusterer — the untagged content that no structure
// element claimed.
func partitionResidue(runs []text.TextRun, reachable map[int]struct{}) []text.TextRun {
	out := make([]text.TextRun, 0, len(runs))
	for _, r := range runs {
		if r.MCID <= 0 {
			out = append(out, r)
			continue
		}
		if _, ok := reachable[r.MCID]; !ok {
			// Orphan: run has a positive MCID but no structure element
			// references it. Treat as residue and surface for debug
			// visibility — common cause is a producer bug or a
			// trimmed-down structure tree.
			slog.Debug("pdf/structtree: orphan-MCID run (no structure ref); routing to residue",
				"mcid", r.MCID, "page", r.X)
			out = append(out, r)
		}
	}
	return out
}

// dropOverlapping returns residue blocks whose bbox does NOT
// intersect any tagged block's bbox. Implements the locked
// tagged-wins overlap rule (open_question Q2 option a).
func dropOverlapping(residue, tagged []layout.Block) []layout.Block {
	if len(tagged) == 0 {
		return residue
	}
	out := make([]layout.Block, 0, len(residue))
	for _, r := range residue {
		clash := false
		for _, t := range tagged {
			if bboxIntersects(r.BBox, t.BBox) {
				clash = true
				break
			}
		}
		if !clash {
			out = append(out, r)
		}
	}
	return out
}

// mergeYAscending stable-sorts blocks by Y0 ascending; tie-breaks by
// X0 ascending. Mirrors layout/blocks.go's groupLinesToBlocks final
// stable-sort. Stable-sort preserves emission order for ties.
func mergeYAscending(blocks []layout.Block) {
	sort.SliceStable(blocks, func(i, j int) bool {
		if blocks[i].BBox.Y0 != blocks[j].BBox.Y0 {
			return blocks[i].BBox.Y0 < blocks[j].BBox.Y0
		}
		return blocks[i].BBox.X0 < blocks[j].BBox.X0
	})
}

// bboxIntersects reports whether two axis-aligned rectangles overlap.
// Standard AABB test: rectangles do NOT overlap iff one is fully to
// the left, right, above, or below the other.
func bboxIntersects(a, b layout.Rect) bool {
	return !(a.X1 <= b.X0 || b.X1 <= a.X0 || a.Y1 <= b.Y0 || b.Y1 <= a.Y0)
}

// mcidsFromBlock parses Block.Metadata["mcids"] (a comma-separated
// integer list emitted by walk_emit.go's emitBlock + emitFigure)
// into a slice of ints. Missing metadata, empty value, or any parse
// error returns nil — the block contributes no reachable MCIDs.
func mcidsFromBlock(b layout.Block) []int {
	if b.Metadata == nil {
		return nil
	}
	raw, ok := b.Metadata["mcids"]
	if !ok || raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		v, err := strconv.Atoi(p)
		if err != nil {
			continue
		}
		out = append(out, v)
	}
	return out
}
