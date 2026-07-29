// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// residentBackstopFloor and residentBackstopRatio gate the READ-SIDE coverage
// backstop (recoverIfDegenerate). They are INTENTIONALLY a SEPARATE pair from the
// write-side auto-heal's segmentCoverageFloor / coverageRatioThreshold
// (bootstrap/client_segment.go:91-94): that pair compares segment-COVERED docs
// against the graph's EMBEDDED-node count (a server-vs-server ratio on the write
// drain edge). THIS pair compares the daemon's RESIDENT in-memory engine doc count
// against the server's SHIPPED doc count (an in-memory-vs-server ratio on the read
// edge) — a different numerator AND denominator, so the two must NOT be unified
// even though the constant values currently coincide. The sibling idiom (floor
// disarms a tiny graph from the ratio; ratio marks a degenerate pool) is shared;
// the operands are not.
const (
	residentBackstopFloor = 64
	residentBackstopRatio = 0.5
)

// recoverIfDegenerate is the read-side durability net: after Search's initial
// load(), it detects an in-memory HNSW engine that is degenerate relative to the
// server's shipped corpus (the read-side degeneracy symptom: a cold process whose
// load floor was poisoned ends up with a near-empty searchable set while the
// server holds the full corpus) and forces a SINGLE-FLIGHTED recovery load() that
// re-imports the corpus. It is best-effort: any probe error logs and returns nil,
// never failing the search.
//
// Cost discipline: a HEALTHY engine (resident >= floor) pays exactly ONE in-memory
// ResidentDocCount() (an atomic load) and ZERO extra RPC — the floor gate returns
// before any List. Only a below-floor engine pays the one List(0) shipped-count
// probe.
//
// STRUCTURE: this is the thin LIVE wrapper — entry floor gate, one shipped-count
// read, then delegate. The degeneracy test and the single-flighted recovery live in
// recoverIfDegenerateWithShipped (manager_reconcile_arms.go), which takes the
// denominator as a parameter so a caller that needs it for its own verdict reads it
// ONCE instead of twice. There is exactly one copy of the recovery logic; this
// wrapper exists because the read-side caller has no denominator of its own.
func (m *distManager[Q, S]) recoverIfDegenerate(ctx context.Context) error {
	// GATE: a healthy engine never pays the probe. resident >= floor → done, zero RPC.
	if m.engine.ResidentDocCount() >= residentBackstopFloor {
		return nil
	}

	// Below the floor: read the server's shipped doc count for THIS graph over the
	// EXISTING source (do NOT build a new segment source) and apply the shared
	// disarm rules (List error, pre-doc_count blob, sub-floor corpus).
	shipped, disarm, err := m.shippedDocCountForRatio(ctx)
	if err != nil {
		slog.Warn("segmentdist: read-side backstop List probe failed (search continues uncorrected)",
			"error", err)
		return nil
	}
	return m.recoverIfDegenerateWithShipped(ctx, shipped, disarm)
}

// shippedDocCountForRatio sums this engine's keepFormat shipped doc count from one
// List(0) over the EXISTING source and reports whether the ratio should be
// DISARMED. It is the shared shipped-denominator probe both the read-side
// recoverIfDegenerate and the startup/periodic ReconcileResidentDegenerate use, so
// the disarm rules cannot drift between the two:
//
//   - disarm=true when ANY kept meta has DocCount==0 (a pre-doc_count blob: the
//     denominator is not trustworthy — conservative-unknown, mirroring
//     segmentPoolDegenerate / ShippedSegmentDocCount's anyUnknown), OR when the
//     summed shipped corpus is below residentBackstopFloor (too small for the ratio
//     to mean anything — a tiny graph never churns).
//   - err is the List error (best-effort: the caller logs and no-ops, never failing
//     its parent operation).
//
// When disarm=false the returned shipped count is a trustworthy denominator (>=
// floor, all kept metas carry a real DocCount) the caller compares resident
// against via residentBackstopRatio.
func (m *distManager[Q, S]) shippedDocCountForRatio(ctx context.Context) (shipped int, disarm bool, err error) {
	metas, err := m.source.List(ctx, 0)
	if err != nil {
		return 0, false, err
	}
	shipped, disarm = m.shippedDocCountForRatioFromSnapshot(metas)
	return shipped, disarm, nil
}

// shippedDocCountForRatioFromSnapshot is shippedDocCountForRatio's body lifted
// VERBATIM onto a passed-in snapshot — the disarm rules (conservative-unknown on a
// DocCount==0 blob; sub-floor disarm) are unchanged. It is the snapshot-consuming
// core the wrapper above feeds with its own List(0); a caller that already holds a
// ShippedManifestSnapshot can call this directly to avoid a redundant List.
func (m *distManager[Q, S]) shippedDocCountForRatioFromSnapshot(
	snapshot []searchengine.SegmentMeta,
) (shipped int, disarm bool) {
	for _, meta := range snapshot {
		if !m.keepFormat(meta.Format) {
			continue
		}
		if meta.DocCount == 0 {
			// Conservative-unknown: an old pre-doc_count blob is present, so the
			// shipped denominator is not trustworthy — disarm rather than churn.
			return 0, true
		}
		shipped += meta.DocCount
	}
	if shipped < residentBackstopFloor {
		// Too small for the ratio to be meaningful — disarm.
		return shipped, true
	}
	return shipped, false
}
