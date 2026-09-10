// SPDX-License-Identifier: Apache-2.0

// manage_status_coverage_registered.go — THE REGISTERED-FAMILY ROW: how
// manage(status) reports a custom graph family an operator has registered and
// nothing has collected under yet.
//
// WHY THE ROW EXISTS. The coverage table's rows come from the per-GRAPH
// enumeration, so before its first collect a registered family had no graph, no
// target and no row — while `collector list` rendered it, the config file held
// it, and the operator had every reason to expect the machine's own inventory to
// name it. The live confirmation of the contrib collectors registered eight
// families and read a coverage table that named none of them.
//
// WHY IT IS ITS OWN SHAPE rather than an unmanaged row with different words. The
// unmanaged fallback says a backend could not produce counts without materializing
// the graph; this row says there is no graph. Those are different facts with
// different next moves — the first is diagnosed, the second is resolved by running
// a collect — and a reader cannot act on a cell that conflates them.
//
// WHAT IT IS BUILT FROM. The registration record, because it is the only record
// there is: the family name becomes the row label with no instance half, and the
// declared embedding half of the behavior cascade decides whether the segment
// cell says a pool was not read or that there can be no pool at all. No RPC is
// issued and no probe runs — see collectCoverageRows for both skips.

package tools

import "fmt"

// DispositionNotCollected is the band for a REGISTERED family with no collected
// graph. It sits in the band column because that column names WHICH ARM OWNS THE
// ROW, and the honest answer here is that no arm does and none is meant to: there
// is nothing to service until a collect creates a graph.
//
// IT IS NOT `unmanaged`, though both mean no arm is working. Unmanaged is a graph
// this client holds and has not been asked to work on — its counts are real and a
// search would admit it. This row has no graph at all, so borrowing that band
// would tell an operator a graph exists whose coverage nothing maintains.
const DispositionNotCollected = "not collected"

// newRegisteredCoverageRow assembles the row for a registered family with no
// collected graph, from the target alone.
//
// HasSegments IS THE REGISTRATION'S DECLARATION, not kgtypes.HasRebuildableSegments.
// That predicate answers about BUILTIN types and returns true for every name
// outside its one exclusion, so reading it here would assert a segment pool for a
// family whose own declaration says it embeds nothing.
//
// CountsRead is false, which is the row's own statement that its numbers are
// ABSENT rather than zero — the same contract every other unread row carries.
func newRegisteredCoverageRow(t coverageTarget) CoverageRow {
	row := CoverageRow{
		Graph:        t.label,
		NoInstance:   true,
		HasSegments:  t.declaredSegments,
		CountsRead:   false,
		InWorkingSet: false,
	}
	row.SegDisposition = segCoverageDisposition(row)
	return row
}

// formatRegisteredCoverageRow renders the whole registered-family row. It carries
// the SAME eight cells the header does — a row narrower or wider than its header
// is silently mangled by the markdown renderer, which is the defect
// TestCoverageTableHeaderMatchesRowCellCount exists to catch — and every column
// that has no value renders the em-dash rather than a zero a reader would take
// for a measurement.
//
// The segment cell is the SHARED one (segmentCoverageCell), so the three row
// shapes keep one spelling of that column: a family declaring embedding renders
// "not read [not collected]", and one declaring none renders the bare dash its
// disposition already is.
func formatRegisteredCoverageRow(r CoverageRow) string {
	return fmt.Sprintf("| %s | not collected (registered, no graph yet) | — | — | %s | — | — | — |",
		r.Graph, segmentCoverageCell(r))
}
