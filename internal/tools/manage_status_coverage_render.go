// SPDX-License-Identifier: Apache-2.0

// manage_status_coverage_render.go — the per-row RENDERERS of the coverage table.
//
// SPLIT OUT OF manage_status_coverage.go, unchanged, when that file reached the
// repo's hard 500-line cap. The seam is the same one its siblings already use:
// this file is how a row is SAID, while manage_status_coverage.go keeps what a row
// IS (the pinned CoverageRow wire contract) and manage_status_coverage_bands.go
// keeps the judgment made about it. The unmanaged row's own formatter stays beside
// its constructor in manage_status_coverage_unmanaged.go and calls into
// segmentCoverageCell below, so the segment cell has ONE spelling across all three
// row shapes.

package tools

import "fmt"

// formatCoverageRow renders one Markdown table row from a CoverageRow. An empty
// (zero-denominator) graph renders "(empty graph)" so a never-populated graph is
// visibly distinct from a covered one; otherwise summarized/embedded render as
// "X of N" so "0 of N summarized" is unambiguous against "N of N summarized".
//
// THE SEGMENT CELL CARRIES INDEPENDENT MEASUREMENTS AND NONE OF THEM BOUNDS
// ANOTHER, which is why it renders them as separately labeled terms —
// "shipped N · live M [band]" — rather than joining any two with "of":
//
//   - shipped (SegCovered) sums HNSW doc_counts across the RESIDENT segments, WITH
//     DUPLICATES: a document resident in more than one segment across an
//     un-reclaimed merge window contributes once per segment. On a churned graph it
//     routinely exceeds the embedded count by multiples. It was a shipped-manifest
//     sum until the manifest was deleted; the label is kept because the JSON key
//     seg_covered is a pinned wire name.
//   - live (LiveResident) is the distinct live-searchable doc count. A collapsed
//     live pool reads "live 0" against an intact shipped figure instead of being
//     masked behind it.
//   - embedded (Embedded) counts live graph nodes holding a binary vector. It is
//     deliberately NOT repeated here — the table gives it its own column — and it
//     is the denominator the band arms classify against, never a bound on either
//     figure above.
//
// The bracketed term is the band, and it names which arm OWNS the row. That is
// also the proof the counts are unordered: the residue band is DEFINED as
// live > embedded, so the very containment an "of" would assert is a state the
// bands treat as ordinary. An earlier revision rendered shipped-of-embedded, which
// printed readings like "4695 of 781" for a perfectly converged graph.
//
// The three readings that cell can carry — no pool, a pool nobody probed, a probed
// pool — are rendered by segmentCoverageCell below, which every row shape shares.
//
// The stuck band is the ONE band that renders more than its name: an age, because a
// state nothing is working to clear is only actionable once a reader can see how long
// it has persisted. Every other band names an arm that is running, so its name is the
// whole story.
// A ROW WHOSE COUNTS WERE NEVER READ IS DISPATCHED FIRST, ahead of the empty-graph
// arm, and the order is load-bearing: an unread row's Total is zero because nobody
// could answer, so the empty-graph arm below would report a 366k-node graph as empty.
// That state is now the FALLBACK rather than the rule for every unmanaged graph: the
// counts are read for every row whose backend can answer them without materializing
// the graph, and only a local server holding no durable counts for an old image
// leaves CountsRead false. See formatUnmanagedCoverageRow
// (manage_status_coverage_unmanaged.go) for what it says instead.
func formatCoverageRow(r CoverageRow) string {
	if !r.CountsRead {
		return formatUnmanagedCoverageRow(r)
	}
	if r.Total == 0 {
		return fmt.Sprintf("| %s | (empty graph) | | | | | | |", r.Graph)
	}
	return fmt.Sprintf("| %s | %d | %d of %d | %d of %d | %s | %d | %d | %s |",
		r.Graph, r.Total,
		r.Summarized, r.Total,
		r.Embedded, r.Total,
		segmentCoverageCell(r),
		r.SummaryFail, r.EmbedFail,
		erasureBacklogCell(r))
}

// segmentCoverageCell renders the segment column for EVERY row shape, so the three
// readings it can carry have one spelling rather than one per formatter.
//
//   - NO POOL AT ALL renders the bare dash: the row's disposition IS the dash, so
//     appending the band would print "— [—]".
//   - A POOL NOBODY PROBED renders "not read [band]". The band still names why the
//     probe was declined, and "not read" beside it keeps the band from being
//     mistaken for a reading of an empty pool. This is the state an UNMANAGED row
//     is in whether or not its counts were read: the two probes behind this cell
//     import the graph's L2 pool and construct its engine in this process, which is
//     the interaction an unmanaged graph is owed none of — counts and segment
//     probes are different reads with different costs.
//   - A PROBED POOL renders the two independent measurements and the band.
func segmentCoverageCell(r CoverageRow) string {
	if !r.HasSegments {
		return DispositionNoSegments
	}
	if !r.SegProbed {
		return fmt.Sprintf("not read [%s]", coverageBandTerm(r))
	}
	return fmt.Sprintf("shipped %d · live %d [%s]",
		r.SegCovered, r.LiveResident, coverageBandTerm(r))
}
