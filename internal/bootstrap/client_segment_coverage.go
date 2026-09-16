// SPDX-License-Identifier: Apache-2.0

// client_segment_coverage.go — the manage(status) SEGMENT-COVERAGE adapter.
//
// SPLIT OUT OF client_segment.go, unchanged, when that file reached the repo's
// hard 500-line cap. The seam is the one this package's siblings already use:
// client_segment.go keeps the segment manager's lifecycle and the other consumer
// seams, and this file keeps the one adapter whose job is a VOCABULARY MAPPING
// rather than a pass-through — see the type's own doc below for why that mapping
// has to live in the composition root.

package bootstrap

import (
	"context"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/segmentdist"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

// segmentCoverageAdapter is the ONE place the backstop's record crosses from the
// segment package into the tools carrier.
//
// IT EXISTS BECAUSE THE IMPORT CANNOT GO THE OTHER WAY: the segment package's own
// in-package tests import tools, so tools may not import the segment package in
// production, and therefore cannot name segmentdist.RepairState. bootstrap is the
// composition root and already imports both, so the conversion belongs here rather
// than inverting either layer. Every other method is a straight pass-through.
type segmentCoverageAdapter struct{ mgr *segmentdist.Manager }

func (a segmentCoverageAdapter) ShippedSegmentDocCount(
	ctx context.Context, gt kgtypes.GraphType, name string,
) (int, error) {
	return a.mgr.ShippedSegmentDocCount(ctx, gt, name)
}

func (a segmentCoverageAdapter) ResidentDocCount(gt kgtypes.GraphType, name string) int {
	return a.mgr.ResidentDocCount(gt, name)
}

// LoadRebuildState / LoadMergeWatermark forward this client's own consumer
// positions, which the status row renders as "how long since each advanced".
// Both are local record reads on the Manager — no RPC.
func (a segmentCoverageAdapter) LoadRebuildState(
	gt kgtypes.GraphType, name string,
) (int64, []searchengine.ExternalID, error) {
	return a.mgr.LoadRebuildState(gt, name)
}

func (a segmentCoverageAdapter) LoadMergeWatermark(gt kgtypes.GraphType, name string) (int64, error) {
	return a.mgr.LoadMergeWatermark(gt, name)
}

func (a segmentCoverageAdapter) LiveResidentDocCount(gt kgtypes.GraphType, name string) int {
	return a.mgr.LiveResidentDocCount(gt, name)
}

// LiveResidentDocCountFor forwards the SAME reading resolved for the destination
// the call is bound to. It is what the status cell reads, so that cell's `live`
// figure comes off the engine its `shipped` figure came off; the root-scoped
// neighbor above is left for the code-search gate that still reads it. Like every
// neighbor here it is a local read and it constructs no arm.
func (a segmentCoverageAdapter) LiveResidentDocCountFor(
	ctx context.Context, gt kgtypes.GraphType, name string,
) int {
	return a.mgr.LiveResidentDocCountFor(ctx, gt, name)
}

// BM25SegmentDocCounts forwards the TEXT pool's (shipped, live) pair, which the
// status segment-coverage cell reads for a graph with no vector engine. A
// destination-resolved, single-observation read on the Manager — no RPC — like its
// neighbors. See Manager.BM25SegmentDocCounts for why the cell needs it.
func (a segmentCoverageAdapter) BM25SegmentDocCounts(
	ctx context.Context, gt kgtypes.GraphType, name string,
) (shipped, live int, skipped bool, err error) {
	return a.mgr.BM25SegmentDocCounts(ctx, gt, name)
}

// COMPILE-TIME PROOF THAT THE ADAPTER CARRIES THE PER-FORMAT RESIDENT SEGMENT COUNT
// the tools layer's optional seam type-asserts for. That seam
// (residentSegmentCountsReader) is unexported in tools and resolved by a RUNTIME type
// assertion, so a drift in this method's SHAPE — a renamed method, a reordered
// parameter, a changed result type — would not break the build. It would silently
// make the assertion fail, and every coverage row would report no resident segment
// count forever, which is the row's own honest rendering for a reader that cannot
// look and therefore reads exactly like a working one. This line is what turns that
// into a build error. It is the same device client_segment.go uses for
// loadLiveResidentReader, for the same reason.
// THE SAME DEVICE COVERS THE DESTINATION-RESOLVED LIVE READER beside it, which the
// cell resolves by the same kind of runtime assertion and which declines to the
// root-scoped reading when it misses — a decline that renders a plausible number
// rather than an error, so only the compiler can catch its shape drifting.
// THE SAME DEVICE NOW COVERS THE QUARANTINE READING, which is resolved by a runtime
// assertion of its own (quarantinedSegmentCountsReader in tools) and whose decline is
// SILENT BY DESIGN: a row that cannot look reports no quarantine, which renders
// exactly like a graph that has lost nothing. That is the honest rendering for an
// unwired seam and the dangerous one for a drifted signature, so the compiler holds
// the shape.
var _ interface {
	ResidentSegmentReadings(kgtypes.GraphType, string) (counts, peaks, destinations map[string]int)
	ResidentSegmentCounts(kgtypes.GraphType, string) map[string]int
	ResidentSegmentPeaks(kgtypes.GraphType, string) map[string]int
	ResidentSegmentDestinations(kgtypes.GraphType, string) map[string]int
	QuarantinedSegmentCounts(kgtypes.GraphType, string) map[string]int
	LiveResidentDocCountFor(context.Context, kgtypes.GraphType, string) int
} = segmentCoverageAdapter{}

// QuarantinedSegmentCounts forwards how many segments each of this graph's engines
// has WITHDRAWN FROM SERVICE for corruption, per format. Like its neighbors it is a
// local snapshot read on the Manager — no RPC — and it constructs nothing.
//
// IT IS A LOSS READING, NOT A CACHE STATISTIC, which is why it sits beside the
// resident counts rather than with the eviction ones: a withdrawn segment's documents
// are unreachable until the graph's segments are rebuilt, and until this reached the
// status table the ONLY record of that loss was one log line at the moment it
// happened.
func (a segmentCoverageAdapter) QuarantinedSegmentCounts(gt kgtypes.GraphType, name string) map[string]int {
	return a.mgr.QuarantinedSegmentCounts(gt, name)
}

// ResidentSegmentCounts forwards the per-format resident SEGMENT count, which the
// status segment-coverage cell renders and the release smoke reads to assert the
// resident-growth bound is holding. A local snapshot read on the Manager — no RPC,
// and it CONSTRUCTS NOTHING (see Manager.ResidentSegmentCounts) — like its
// neighbors.
//
// IT IS HANDED THE ROOT MANAGER AND THAT IS NOW CORRECT, where for the readings
// this adapter's neighbors take it would not be. The Manager's own reading walks
// the root AND every {storage,account} child, so a root receiver is how a status
// read observes every engine this daemon holds for the graph rather than the one
// destination a ctx happens to name; the neighbors that must serve ONE destination
// resolve it themselves, from their ctx, inside the Manager (forRequest).
func (a segmentCoverageAdapter) ResidentSegmentCounts(gt kgtypes.GraphType, name string) map[string]int {
	return a.mgr.ResidentSegmentCounts(gt, name)
}

// ResidentSegmentPeaks forwards the seal-path high-water beside the current count.
// It is the reading the current count CANNOT give — see Manager.ResidentSegmentPeaks
// — and, like its neighbor, a local snapshot read that constructs nothing.
func (a segmentCoverageAdapter) ResidentSegmentPeaks(gt kgtypes.GraphType, name string) map[string]int {
	return a.mgr.ResidentSegmentPeaks(gt, name)
}

// ResidentSegmentDestinations forwards how many destinations the pair above was
// folded from, on the same terms. It is what keeps the rendered cell from stating a
// maximum over several engines as a reading of one.
func (a segmentCoverageAdapter) ResidentSegmentDestinations(gt kgtypes.GraphType, name string) map[string]int {
	return a.mgr.ResidentSegmentDestinations(gt, name)
}

// ResidentSegmentReadings forwards all three of those readings from ONE walk of the
// destinations, and it is the one the status row actually reads: the row renders the
// triple side by side, so three calls to the projections above would assemble it
// from three walks that can disagree about which arms exist. The three remain for
// the callers that want one of them.
func (a segmentCoverageAdapter) ResidentSegmentReadings(
	gt kgtypes.GraphType, name string,
) (counts, peaks, destinations map[string]int) {
	return a.mgr.ResidentSegmentReadings(gt, name)
}

// BM25DegradeCounts forwards this client's accumulated per-graph BM25 drop
// census. A local record read on the Manager — no RPC — like its neighbors.
func (a segmentCoverageAdapter) BM25DegradeCounts(gt kgtypes.GraphType, name string) map[string]int {
	return a.mgr.BM25DegradeCounts(gt, name)
}

func (a segmentCoverageAdapter) RepairVerification(
	gt kgtypes.GraphType, name string,
) (tools.RepairVerification, bool) {
	st, ok := a.mgr.RepairStateCached(gt, name)
	if !ok {
		return tools.RepairVerification{}, false
	}
	return tools.RepairVerification{
		Residue:         st.Residue,
		Converged:       st.Converged,
		Scanned:         st.Scanned,
		VerifiedAtNanos: st.VerifiedAtNanos,
	}, true
}
