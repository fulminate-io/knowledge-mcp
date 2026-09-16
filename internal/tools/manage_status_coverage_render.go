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

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
)

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
// A REGISTERED FAMILY WITH NO GRAPH IS DISPATCHED AHEAD OF BOTH, because its
// counts are absent for a third reason: there is nothing to count yet. The
// unmanaged shape below would tell an operator a graph exists whose counts a
// backend declined, and the empty-graph arm would tell them one exists and is
// empty. See manage_status_coverage_registered.go.
func formatCoverageRow(r CoverageRow) string {
	if r.NoInstance {
		return formatRegisteredCoverageRow(r)
	}
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
	return fmt.Sprintf("shipped %d · live %d [%s]%s%s%s",
		r.SegCovered, r.LiveResident, coverageBandTerm(r),
		coverageResidentSegmentsSuffix(r), coverageQuarantineSuffix(r), coverageDegradeSuffix(r))
}

// coverageResidentSegmentsSuffix names how many sealed SEGMENTS each of the row's
// engines holds, per format, or returns the empty string when nothing was measured
// — so a row whose pool reported no counts is byte-identical to what it was before
// this term existed.
//
// IT IS A FOURTH INDEPENDENT MEASUREMENT and is labeled as its own term for the
// reason the cell's own paragraph gives about the first three: shipped and live are
// DOCUMENT counts, this is a SEGMENT count, and neither bounds the other. Joining
// them with "of" would assert a containment that does not exist.
//
// EACH FORMAT IS NAMED BESIDE ITS COUNT, and that is what the release smoke reads.
// Its only reader of this table locates a column by its HEADER and takes the cell
// whole, so a value reachable only by counting fields would not be readable at all;
// naming the format also makes a render that silently dropped one engine a FAILED
// cell rather than a shorter line. Ordered by format name so the cell is stable.
func coverageResidentSegmentsSuffix(r CoverageRow) string {
	if len(r.ResidentSegments) == 0 {
		return ""
	}
	formats := make([]string, 0, len(r.ResidentSegments))
	for f := range r.ResidentSegments {
		formats = append(formats, f)
	}
	slices.Sort(formats)
	parts := make([]string, 0, len(formats))
	for _, f := range formats {
		parts = append(parts, residentSegmentTerm(r, f))
	}
	return " · segments " + strings.Join(parts, ", ")
}

// residentSegmentTerm renders ONE format's term of that suffix: the count, and the
// two qualifiers that say what kind of number it is.
//
// THE PEAK RIDES BESIDE THE COUNT, in the same term, because a cell carrying only
// the current number would show a bound holding perfectly through an excursion it
// cannot see. A peak EQUAL to the count adds nothing and is omitted, so a graph that
// never crossed reads exactly as it did before that term existed.
//
// THE TWO ARE ONE READING OF ONE ENGINE ONLY AT ONE DESTINATION. Above one they are
// INDEPENDENT MAXIMA: a daemon whose child A holds count 100 / peak 100 and whose
// child B holds count 10 / peak 500 renders `bm25v2 100 (peak 500, 2 destinations)`,
// a peak that never belonged to the engine reporting the count. The destination term
// is what keeps that honest — it is the only thing in the cell telling a reader the
// two numbers may be about different engines — which is why it is not optional above
// one.
//
// THE DESTINATION COUNT RIDES THERE TOO, and only above one. Each number is a
// MAXIMUM across the {storage,account} destinations this client holds an engine for
// — the budget is per engine, so a maximum is the fence-relevant reading and a sum
// would report two half-full engines as a breach — and a maximum over two engines
// printed as a bare number reads exactly like a reading of one. Naming a SINGLE
// destination would instead put a term saying nothing on every row of every
// single-account install, which is every install of the OSS client.
func residentSegmentTerm(r CoverageRow, format string) string {
	term := fmt.Sprintf("%s %d", format, r.ResidentSegments[format])
	quals := make([]string, 0, 2)
	if peak := r.ResidentSegmentsPeak[format]; peak > r.ResidentSegments[format] {
		quals = append(quals, fmt.Sprintf("peak %d", peak))
	}
	if destinations := r.ResidentSegmentDestinations[format]; destinations > 1 {
		quals = append(quals, fmt.Sprintf("%d destinations", destinations))
	}
	if len(quals) == 0 {
		return term
	}
	return term + " (" + strings.Join(quals, ", ") + ")"
}

// coverageDegradeSuffix names input this client's BM25 builds dropped for the
// row's graph, or returns the empty string when it dropped nothing — so a clean
// row is byte-identical to what it was before the census existed.
//
// THE WORD IS "dropped", NOT "degraded". Every other cell in this row is a count
// of what IS indexed, and the bracketed term beside it is already a BAND, so
// "degraded" here would read as another band. What this names is input that never
// entered any of those counts.
func coverageDegradeSuffix(r CoverageRow) string {
	list := degradeClassList(r.Degraded)
	if list == "" {
		return ""
	}
	return " · dropped (" + list + ")"
}

// degradeClassList renders a per-class degrade census as `class n` pairs joined by
// ", ", and is shared by BOTH operator surfaces that carry one — this row's cell
// and the manage(rebuild_segments) response — so one product does not grow two
// spellings of the same census.
//
// The three rules are the ones collector/composition.go:171-206 holds itself to,
// adopted verbatim: render NOTHING when the census is empty, skip non-positive
// counts, and order count-descending then name-ascending. The RULES are delegated,
// not the code: that renderer lives in another package and formats a different
// line, and importing across the boundary to share nine lines of sorting would
// couple the status table to the collector's own render.
func degradeClassList(census map[string]int) string {
	if len(census) == 0 {
		return ""
	}
	type row struct {
		name  string
		count int
	}
	rows := make([]row, 0, len(census))
	for name, n := range census {
		if n <= 0 {
			continue
		}
		rows = append(rows, row{name: name, count: n})
	}
	if len(rows) == 0 {
		return ""
	}
	slices.SortFunc(rows, func(a, b row) int {
		if n := cmp.Compare(b.count, a.count); n != 0 {
			return n
		}
		return cmp.Compare(a.name, b.name)
	})
	parts := make([]string, 0, len(rows))
	for _, r := range rows {
		parts = append(parts, fmt.Sprintf("%s %d", r.name, r.count))
	}
	return strings.Join(parts, ", ")
}
