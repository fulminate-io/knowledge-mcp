// SPDX-License-Identifier: Apache-2.0

package tools

// manage_status_coverage_quarantine.go — the QUARANTINE reading on manage(status):
// how many segments each of a graph's engines has withdrawn from service for
// corruption, and what that costs the graph.
//
// IT IS ONE SUBJECT ACROSS BOTH SURFACES, which is why the seam, the sentence and
// the markdown term live together rather than in the assembly and rendering files
// their neighbors sit in: the count and its consequence are a pair, and a reader
// meeting one of them without the other reads a statistic instead of a loss. The
// split also kept both of those files inside the repository's 500-line cap.
//
// WHAT THE READING MEANS. segmentdist withdraws a segment whose bytes a reader
// refuses: the file is moved into a quarantine subdirectory and its index entry is
// dropped, and NOTHING in this tree re-fetches or re-indexes it. So the documents
// that segment held are unreachable by any search of that graph until an operator
// runs manage rebuild_segments — and until this reading existed, the only record of
// that loss was one log line at the moment it happened.

import (
	"fmt"
	"slices"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// quarantinedSegmentCountsReader is the OPTIONAL deps capability the per-format
// QUARANTINE count is read through, type-asserted for the reason
// residentSegmentCountsReader states: a required method on SegmentCoverageReader
// would have to be implemented by every fake that already implements
// SegmentCoverage(), none of which holds a segment store to have withdrawn anything
// from. The composition root carries a compile-time assertion of this shape
// (bootstrap/client_segment_coverage.go), because an unwired seam and a drifted
// signature render identically here.
type quarantinedSegmentCountsReader interface {
	QuarantinedSegmentCounts(gt kgtypes.GraphType, name string) map[string]int
}

// quarantinedSegmentsFor reads one graph's per-format withdrawal count through that
// seam. An unwired seam reports NOTHING — a nil map, which renders as no reading —
// rather than a fabricated zero per format: a reader with no way to look has not
// observed an intact graph, it has not observed at all.
func quarantinedSegmentsFor(deps ClientDeps, gt kgtypes.GraphType, name string) map[string]int {
	sr, ok := deps.SegmentCoverage().(quarantinedSegmentCountsReader)
	if !ok {
		return nil
	}
	return sr.QuarantinedSegmentCounts(gt, name)
}

// quarantineImpactFor states what a row's quarantine counts COST, and returns the
// empty string when nothing is withdrawn.
//
// THE SENTENCE IS THE POINT AND IT IS CARRIED ON THE WIRE. A count alone reads as a
// statistic; what it names is that documents are missing from every search of this
// graph until an operator rebuilds its segments, because nothing in this tree
// re-fetches or re-indexes a quarantined segment. The wording is the one the
// quarantine's own log line uses, so an operator meeting it in two places meets one
// sentence.
//
// IT NAMES reset: true, AND THE FLAG IS THE DIFFERENCE BETWEEN A CURE AND A NO-OP. A
// default rebuild_segments scans only what changed since the last rebuild that landed,
// and a quarantine moves a .seg file aside without touching the node set or the stored
// watermark — so on a corpus whose nodes have not changed the bare command scans
// nothing, builds nothing and reports a clean run with the documents still
// unreachable. Executed in TestBareRebuildIsANoOpOnAnUnchangedCorpusAndResetIsNot.
func quarantineImpactFor(counts map[string]int) string {
	total := 0
	for _, n := range counts {
		total += n
	}
	if total == 0 {
		return ""
	}
	return fmt.Sprintf(
		"%d segment(s) withdrawn: the documents they held are unreachable until this graph's segments are rebuilt "+
			`(manage rebuild_segments with reset: true — a default rebuild scans only what changed and would restore nothing)`,
		total)
}

// coverageQuarantineSuffix names the segments this graph's engines have WITHDRAWN
// FROM SERVICE for corruption, per format, and returns the empty string when nothing
// is withdrawn — so every healthy row is byte-identical to what it was before this
// term existed, which is every row on every install that has never hit corruption.
//
// IT CARRIES THE CONSEQUENCE, NOT JUST THE COUNT, and that is the whole reason the
// term reads the way it does. "quarantined bm25v2 2" is a statistic an operator can
// scroll past; what it means is that some documents cannot be found by any search of
// this graph until the graph's segments are rebuilt, because nothing re-fetches or
// re-indexes a withdrawn segment. The sentence is the one the quarantine's own log
// line uses, so an operator meets one wording in both places.
//
// IT IS LOUD IN A TABLE OF MEASUREMENTS. Its neighbors report capacity; this
// reports loss, so it is spelled in capitals where they are not.
//
// AND IT NAMES reset: true FOR THE REASON quarantineImpactFor DOES: the bare command
// is a no-op on exactly the corpus this cell is rendered for.
func coverageQuarantineSuffix(r CoverageRow) string {
	formats := make([]string, 0, len(r.QuarantinedSegments))
	for f, n := range r.QuarantinedSegments {
		if n > 0 {
			formats = append(formats, f)
		}
	}
	if len(formats) == 0 {
		return ""
	}
	slices.Sort(formats)
	parts := make([]string, 0, len(formats))
	for _, f := range formats {
		parts = append(parts, quarantineTerm(r, f))
	}
	return " · QUARANTINED " + strings.Join(parts, ", ") +
		" (documents unreachable until this graph's segments are rebuilt — manage rebuild_segments, reset: true)"
}

// quarantineTerm renders ONE format's term of that suffix: the count, and the
// disclosure that says what kind of number it is.
//
// THE COUNT IS A SUM ACROSS DESTINATIONS WHERE ITS NEIGHBOR IS A MAXIMUM, and above
// one destination a bare number does not say which. The resident term beside it
// renders `bm25v2 100 (peak 500, 2 destinations)` precisely because a maximum over
// two engines printed as a bare number reads like a reading of one; the same reasoning
// applies here in the other direction — `QUARANTINED bm25v2 3` beside it reads as
// three of THAT engine's segments, while it may be two withdrawn from one store and
// one from another, since every destination keeps its own segment store. The term is
// named only ABOVE one destination, so every single-destination install — every OSS
// install — renders exactly as it did before this disclosure existed.
func quarantineTerm(r CoverageRow, format string) string {
	term := fmt.Sprintf("%s %d", format, r.QuarantinedSegments[format])
	if d := r.ResidentSegmentDestinations[format]; d > 1 {
		return fmt.Sprintf("%s across %d destinations", term, d)
	}
	return term
}
