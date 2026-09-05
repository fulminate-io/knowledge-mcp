// SPDX-License-Identifier: Apache-2.0

// manage_status_coverage_unmanaged.go — the UNMANAGED row: how manage(status)
// reports a graph no direct interaction has admitted into this client's working
// set.
//
// THE RULE IT IMPLEMENTS is the recorded decision (CEO 2026-08-12, verbatim):
// "there must not be any background process in the client process that requests
// or interacts with graphs in any way unless some kind of mcp query like search,
// mutate, collect has interacted with it directly. manage operations do not count
// towards interaction."
//
// THE RULE'S SUBJECT IS LOADING, NOT COUNTING, and that distinction is the whole
// content of this file (CEO 2026-08-28, verbatim: "why cant we just do a count and
// not consider it managed"). An earlier revision read the rule as forbidding the
// per-graph Stats RPC as well, and so rendered "not read" for every count on every
// unmanaged row — which reported a 366k-node graph with four zeros on the wire.
// What the rule forbids is MATERIALIZING a graph nobody asked about. The three
// per-graph reads the walk can make are not one cost:
//
//   - The per-graph Stats(IncludeCoverage:true) RPC is ANSWERED FROM DURABLE STATE
//     AND MAKES NO GRAPH RESIDENT. A server answers it from counts it already
//     maintains for the graph rather than by opening the graph, and a server that
//     cannot answer without opening it FAILS THE CALL instead. Either way nothing
//     becomes resident, so the RPC is issued for every row.
//   - ShippedSegmentDocCount routes to Manager.LoadResidentDocCount and imports
//     every segment id in the graph's L2 cache. STILL DECLINED.
//   - LiveResidentDocCount / ResidentDocCount import nothing but reach
//     Manager.managerFor — a lazy CONSTRUCTION of the per-graph engine, its cache
//     directory and its branch seed, for a graph nobody asked about. STILL
//     DECLINED.
//
// So an unmanaged row renders REAL counts and a segment cell that says "not read",
// and the band stays [unmanaged] because membership, not readability, is what the
// band reports: no background arm services the graph either way.
//
// THE FALLBACK ROW, and it is a fallback rather than the steady state. When the
// backend cannot produce the counts without materializing the graph — a local
// server whose image predates the durable count record — its Stats call fails and
// the row is assembled from the target alone. The catalog enumeration the walk
// already issues (RETURN_MODE_GRAPH_NAMES -> store.ListGraphsLite ->
// Registry.listGraphs) is a bare os.ReadDir that returns each entry's on-disk image
// size, and that size is the one durable per-graph fact available with no
// interaction at all — so it is what that row renders in place of the counts, whose
// cells say "not read" rather than printing a zero a reader would take for a
// measurement.

package tools

import (
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// newUnmanagedCountedCoverageRow assembles the ORDINARY unmanaged row: real counts
// from the Stats response the backend answered without materializing the graph, and
// a segment cell nothing probed.
//
// IT IS NOT newCoverageRow WITH DIFFERENT ARGUMENTS, and the two differences are
// exactly the two the ruling turns on. HasSegments is taken from the graph TYPE
// rather than from a probe (see below), and SegProbed is false, so the segment
// column renders "not read" instead of a fabricated "shipped 0 · live 0". Every
// honest-band input newCoverageRow reads off local state — repair verification, the
// consumer positions, the stall latch, pool eviction — is deliberately not read
// here: each describes what an ARM is doing about the row, and the unmanaged band
// exists precisely because no arm is doing anything about it.
func newUnmanagedCountedCoverageRow(t coverageTarget, st *knowledgev1.GraphStats) CoverageRow {
	row := CoverageRow{
		Graph:                 t.label,
		Total:                 int(st.GetNonProxyNodeCount()),
		Summarized:            int(st.GetSummarizedCount()),
		Embedded:              int(st.GetBinaryVectorCount()),
		SummaryFail:           int(st.GetSummaryFailureCount()),
		EmbedFail:             int(st.GetEmbedFailureCount()),
		RetainedErasures:      int(st.GetRetainedErasureCount()),
		NewestErasureAgeNanos: st.GetNewestErasureAgeNanos(),
		HasSegments:           !t.overlay && kgtypes.HasRebuildableSegments(t.gt),
		SegProbed:             false,
		ImageBytes:            t.imageBytes,
		InWorkingSet:          false,
		CountsRead:            true,
	}
	row.SegDisposition = segCoverageDisposition(row)
	return row
}

// newUnmanagedCoverageRow assembles the FALLBACK row for a graph whose counts the
// backend could not produce without materializing it, from the target alone — no
// usable RPC answer, no probe, no engine.
//
// HasSegments IS SET FROM THE TYPE PREDICATE, not from a probe, and that is what
// keeps the [unmanaged] band derivable without a load. kgtypes.HasRebuildableSegments
// is a pure function of the graph TYPE, so it answers "could this family have a
// segment pool at all" without asking whether this instance does. The band
// classifier then runs its existing arms unchanged: a linkage row
// still falls to the bare dash on the no-segments arm, and everything else reaches
// the unmanaged arm below it.
//
// A BRANCH ROW DECLINES IT ANYWAY. The segment key space is base-keyed and cannot
// represent a branch graph, which is why the assembly loop already passes
// hasSeg=false for those rows; asserting a pool for one here would claim a
// residency that has no key to live under.
func newUnmanagedCoverageRow(t coverageTarget) CoverageRow {
	row := CoverageRow{
		Graph:       t.label,
		HasSegments: !t.overlay && kgtypes.HasRebuildableSegments(t.gt),
		ImageBytes:  t.imageBytes,
		// InWorkingSet is the input the unmanaged band arm reads, and it is false by
		// construction here: this row exists precisely because membership was false.
		InWorkingSet: false,
		// CountsRead is the row's own statement that its numbers are absent rather
		// than zero. Every count field stays at its zero value and nothing may read
		// one as a measurement while this is false.
		CountsRead: false,
	}
	row.SegDisposition = segCoverageDisposition(row)
	return row
}

// unmanagedCountsCell renders the fallback row's first data cell: the plain
// statement that the counts could not be read, plus the durable fact that was
// available without reading anything.
//
// THE SIZE IS OMITTED WHEN IT IS ZERO rather than printed as "0 B". A zero here
// means the catalog reported no size — an in-memory-only graph, or a backend whose
// enumeration does not carry one — and rendering it would assert an empty file
// where the honest answer is that the size is unknown.
func unmanagedCountsCell(r CoverageRow) string {
	if r.ImageBytes <= 0 {
		return "not read (unmanaged)"
	}
	return fmt.Sprintf("not read (unmanaged) · image %s", formatBytes(r.ImageBytes))
}

// formatUnmanagedCoverageRow renders the whole fallback row. It carries the SAME
// eight cells the header does — a row narrower or wider than its header is
// silently mangled by the markdown renderer, which is the defect
// TestCoverageTableHeaderMatchesRowCellCount exists to catch — and every column
// whose value was not read renders the em-dash rather than a zero.
//
// The segment cell is the SHARED one (segmentCoverageCell), which for this row is
// necessarily its unprobed reading: the band stays bracketed so the row still names
// WHY the cell is blank, paired with "not read" so the band is not mistaken for a
// reading of an empty pool. A row whose type has no segment pool at all renders the
// bare dash its disposition already is, exactly as a managed non-segment row does.
func formatUnmanagedCoverageRow(r CoverageRow) string {
	return fmt.Sprintf("| %s | %s | — | — | %s | — | — | — |",
		r.Graph, unmanagedCountsCell(r), segmentCoverageCell(r))
}
