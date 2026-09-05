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
//  4. Cluster residue via layout.BlocksFromRuns using
//     layout.DefaultLayoutParams. Resulting blocks carry empty
//     StructRole — that's the heuristic-origin marker.
//  5. Tagged-wins overlap rule (open_question Q2 locked option a):
//     drop any residue block whose bbox intersects any tagged block.
//  6. Merge tagged + filtered residue into READING ORDER, keyed on
//     each block's reading anchor transformed by the page's /Rotate:
//     normalized y descending (down the page as a viewer sees it),
//     tie-break normalized x ascending. Return.
func HybridFallback(taggedBlocks []layout.Block, allRuns []text.TextRun, info layout.PageInfo) ([]layout.Block, error) {
	reachable := reachableMCIDs(taggedBlocks)
	residueRuns := partitionResidue(allRuns, reachable)
	if len(residueRuns) == 0 {
		return taggedBlocks, nil
	}
	// ELEMENT SCALE, not page scale. This residue is whatever no
	// structure element claimed, which on a well-tagged page is one or
	// two runs; the page-scale grouper would emit one block per run and
	// split a two-run footer in half.
	residue, err := layout.BlocksFromRuns(residueRuns, info, layout.DefaultLayoutParams)
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
	sortReadingOrder(merged, info)
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

// sortReadingOrder stable-sorts blocks into reading order: down the
// page first, then left to right. The key is each block's READING
// ANCHOR — its natural-frame top-left corner, (BBox.X0, BBox.Y1) —
// transformed by the page's /Rotate through layout.NormalizePoint.
//
// COORDINATE FRAME, stated because getting it wrong is invisible until
// a whole document comes back upside down. BOTH inputs arrive in the
// page's natural, unrotated user-space frame, where +y points UP:
// residue comes from layout.BlocksFromRuns, whose clusterAtScale
// un-flips and denormalizes back before returning, and tagged bboxes
// are computed by computeMCIDBBox over raw run coordinates that were
// never flipped or normalized. The two sets are consistent, which is
// why ONE transform applied uniformly is correct.
//
// WHY THE ROTATION TERM. In that natural frame "higher on the page"
// is a larger y only when the page is displayed unrotated. A page
// carrying /Rotate 90 or 180 is read along a different axis, and a
// comparator with no rotation term emits its sections and paragraphs
// in the unrotated order — the defect this key exists to fix.
// NormalizePoint maps the anchor into the frame a viewer actually
// reads, and the comparator below is unchanged from the one that was
// already here; only the coordinates it reads moved frame. At
// /Rotate 0 the transform is the identity, so unrotated ordering is
// byte-identical by construction rather than by measurement.
//
// WHY AN ANCHOR POINT AND NOT THE BBOX. Normalizing the whole
// rectangle rotates its EXTENT too, so a normalized corner's
// coordinates shift by the block's ORIGINAL width and no single
// corner is a stable key at all four rotations — measured: a
// bbox-normalizing implementation orders 0, 90 and 180 correctly and
// gets 270 wrong. A point has no extent to rotate.
//
// Y1 rather than Y0 because Y1 is the TOP edge of the box, which is
// what "higher on the page" means for two blocks of different height:
// a tall paragraph and the short heading above it can share a Y0
// ordering that puts the paragraph first. Stable-sort preserves
// emission order for exact ties.
//
// The keys are precomputed and an index permutation is sorted, so the
// transform runs once per block rather than once per comparison.
func sortReadingOrder(blocks []layout.Block, info layout.PageInfo) {
	type readingAnchor struct{ x, y float64 }
	keys := make([]readingAnchor, len(blocks))
	for i, b := range blocks {
		x, y := layout.NormalizePoint(b.BBox.X0, b.BBox.Y1, info.Rotation, info.MediaBox)
		keys[i] = readingAnchor{x: x, y: y}
	}
	order := make([]int, len(blocks))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := keys[order[i]], keys[order[j]]
		if a.y != b.y {
			return a.y > b.y
		}
		return a.x < b.x
	})
	sorted := make([]layout.Block, len(blocks))
	for i, idx := range order {
		sorted[i] = blocks[idx]
	}
	copy(blocks, sorted)
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
