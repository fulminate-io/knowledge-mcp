// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"
)

// loop_vector_source.go owns the choice of WHERE leaf attachment's member vectors
// come from: the client's resident segment engines (zero RPC) or the server drain.
// Split from loop_detection.go so the source decision — the part with a gate, a
// fallback and a log contract — reads as one unit.

// vectorSourceResident and vectorSourceDrain label which arm served a pass; they
// ride the leaf-attachment log line so a fallback is never silent and an operator
// can tell a resident pass from a drained one without reading code.
const (
	vectorSourceResident = "resident"
	vectorSourceDrain    = "drain"
)

// resolveMemberVectors returns nodeID → stored 256-bit binary vector for the ids
// this pass can actually consult, preferring RESIDENT resolution and falling back
// to the server drain. It also returns the source label and the gate's reason.
//
// THE RESOLVE SET IS SCOPED, AND THE SCOPING IS EQUIVALENCE-PRESERVING BY
// CONSTRUCTION, not an approximation: the candidates themselves, plus every member
// of each non-singleton community reachable from a candidate through adj. That is
// EXACTLY the set attachLeaves can consult — a leaf is scored only against
// centroidByCluster[target] for targets reachable via adj[leaf], and target
// centroids are built only for communities of size > 1. Whole reachable communities
// are pulled in, never partial membership, because a centroid is the bit-majority
// over the FULL member set and a partial one would be silently wrong.
//
// The resident arm is taken only when BOTH seams are wired AND the coverage gate
// reports the graph's HNSW pool measured and non-degenerate. Every other case —
// no seams, a declining gate, a probe error, or a mid-resolution failure — falls
// back to the UNCHANGED drainVectorIndex, which is why that drain keeps its exact
// signature and behavior. (The drain has no id parameter: it returns the whole
// index, a superset of the scoped set, so the fallback is a strict cost regression
// and never a correctness one.)
//
// A resident id that resolves (ok=false, err=nil) is VECTORLESS, not an error: the
// node is not embedded yet or its segment has not shipped. It is simply absent from
// the returned index, exactly as it would be absent from a drained index,
// attachLeaves records it as vectorlessSkipped, and the loop retries it next pass.
func (p *PropagationLoop) resolveMemberVectors(
	ctx context.Context,
	candidates []string,
	communityOf map[string]string,
	commSize map[string]int,
	adj map[string][]string,
) (index map[string][]byte, source, reason string, err error) {
	if p.vectorResident == nil || p.coverageGate == nil {
		idx, derr := p.drainMemberVectors(ctx)
		return idx, vectorSourceDrain, "resident vector seam not wired", derr
	}
	trustworthy, gateReason, gateErr := p.coverageGate.HNSWCoverageTrustworthy(ctx)
	if gateErr != nil || !trustworthy {
		if gateErr != nil {
			slog.Warn("thought: segment coverage probe failed — falling back to the member-vector drain",
				"error", gateErr, "reason", gateReason)
		}
		idx, derr := p.drainMemberVectors(ctx)
		return idx, vectorSourceDrain, gateReason, derr
	}

	ids := residentResolveSet(candidates, communityOf, commSize, adj)
	out := make(map[string][]byte, len(ids))
	for _, id := range ids {
		vec, ok, rerr := p.vectorResident.VectorByID(ctx, id)
		if rerr != nil {
			// A resident read that FAILED (not one that found nothing) means the
			// engine is unusable this pass; take the drain rather than attach over a
			// partially-resolved index, which would silently veto real candidates.
			slog.Warn("thought: resident vector resolution failed — falling back to the member-vector drain",
				"error", rerr, "node_id", id, "resolved_before_failure", len(out))
			idx, derr := p.drainMemberVectors(ctx)
			return idx, vectorSourceDrain, "resident resolution failed", derr
		}
		if !ok {
			continue // vectorless: not embedded / not shipped yet.
		}
		out[id] = vec
	}
	return out, vectorSourceResident, gateReason, nil
}

// residentResolveSet is the id set the resident arm resolves: every candidate leaf,
// plus every member of each non-singleton community REACHABLE from a candidate
// through adj. Whole communities, never partial ones — a target centroid is the
// bit-majority over its full member set.
func residentResolveSet(
	candidates []string,
	communityOf map[string]string,
	commSize map[string]int,
	adj map[string][]string,
) []string {
	want := make(map[string]bool, len(candidates))
	targetComms := map[string]bool{}
	for _, leaf := range candidates {
		want[leaf] = true
		for _, nb := range adj[leaf] {
			comm := communityOf[nb]
			if commSize[comm] > 1 {
				targetComms[comm] = true
			}
		}
	}
	if len(targetComms) > 0 {
		for id, comm := range communityOf {
			if targetComms[comm] {
				want[id] = true
			}
		}
	}
	ids := make([]string, 0, len(want))
	for id := range want {
		ids = append(ids, id)
	}
	sort.Strings(ids) // deterministic resolution order for logs and tests.
	return ids
}

// drainMemberVectors is the fallback arm: the existing full segment_rebuild drain,
// called with its unchanged two-argument signature. A nil scanner here is genuinely
// degraded — there is no other source left — so it reports an error the caller logs
// before skipping attachment for the pass.
func (p *PropagationLoop) drainMemberVectors(ctx context.Context) (map[string][]byte, error) {
	if p.scanner == nil {
		return nil, fmt.Errorf("thought: no member-vector scanner wired and resident resolution unavailable — no vector source left")
	}
	start := time.Now()
	idx, err := drainVectorIndex(ctx, p.scanner)
	if err != nil {
		return nil, err
	}
	slog.Debug("thought: member-vector drain complete",
		"drain_ms", time.Since(start).Milliseconds(), "vectors", len(idx))
	return idx, nil
}
