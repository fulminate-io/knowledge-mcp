// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"log/slog"
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
// Single-flight: load() takes no package lock (shipMu guards ship state only), so
// K concurrent degenerate searches would each reset the floor and re-import the
// corpus. The recovering atomic.Bool CAS elects ONE recovering goroutine; the rest
// skip (the winner's load() makes the corpus resident shortly). The flag is
// released on EVERY exit path (incl. the load() error path) via defer. The Phase-1
// Import dedup already makes a concurrent re-import merely wasteful (not
// corrupting), so single-flight is a cost bound, not a correctness requirement —
// kept belt-and-suspenders.
func (m *distManager[Q, S]) recoverIfDegenerate(ctx context.Context) error {
	// GATE: a healthy engine never pays the probe. resident >= floor → done, zero RPC.
	resident := m.engine.ResidentDocCount()
	if resident >= residentBackstopFloor {
		return nil
	}

	// Below the floor: read the server's shipped doc count for THIS graph over the
	// EXISTING source (do NOT build a new rpcSegmentSource) and apply the shared
	// disarm rules (List error, pre-doc_count blob, sub-floor corpus).
	shipped, disarm, err := m.shippedDocCountForRatio(ctx)
	if err != nil {
		slog.Warn("segmentdist: read-side backstop List probe failed (search continues uncorrected)",
			"error", err)
		return nil
	}
	if disarm {
		return nil
	}

	// Degeneracy: the server holds a real corpus (>= floor) but the in-memory engine
	// covers far less than the ratio of it.
	if float64(resident) >= residentBackstopRatio*float64(shipped) {
		return nil
	}

	// SINGLE-FLIGHT the recovery: only the CAS winner resets the floor + re-imports.
	if !m.recovering.CompareAndSwap(false, true) {
		return nil // another goroutine is already recovering this engine — skip.
	}
	defer m.recovering.Store(false)

	// Reset the load floor to 0 so the next load() Lists(0) and re-imports the full
	// corpus (Import dedup drops any already-resident segment), then force that load.
	m.importedGen.Store(0)
	return m.load(ctx)
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
	for _, meta := range metas {
		if !m.keepFormat(meta.Format) {
			continue
		}
		if meta.DocCount == 0 {
			// Conservative-unknown: an old pre-doc_count blob is present, so the
			// shipped denominator is not trustworthy — disarm rather than churn.
			return 0, true, nil
		}
		shipped += meta.DocCount
	}
	if shipped < residentBackstopFloor {
		// Too small for the ratio to be meaningful — disarm.
		return shipped, true, nil
	}
	return shipped, false, nil
}
