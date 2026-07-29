// SPDX-License-Identifier: Apache-2.0

package thought

import "context"

// loop_vector_deps.go declares the two OPTIONAL seams leaf attachment resolves
// member vectors through in-process, plus their fluent nil-tolerant setter.
//
// Both are PACKAGE-LOCAL by necessity, not by preference: tools already imports
// thought, so a tools-typed seam here would close an import cycle. thought imports
// neither tools, bootstrap nor segmentdist, and these interfaces keep it that way —
// the bootstrap adapters do the binding.
//
// Both are narrowed to the single (knowledge, "default") graph the propagation loop
// reflects over, so the graph type and name are bound by the ADAPTER rather than
// threaded through the loop.

// VectorResident resolves a node's stored binary vector from the client's RESIDENT
// segment engines — zero RPC.
//
// ok=false with a NIL error means "loaded fine, no such id": the node is not
// embedded yet, or its segment has not shipped. That is the ordinary VECTORLESS
// case, NOT an error — leaf attachment records the node for retry on a later pass
// and moves on. (The mode:"similar" search claim, the other caller of the
// underlying Manager.VectorByID, deliberately does the opposite and turns ok=false
// into a loud error with rebuild guidance; both readings are legitimate, and each
// belongs to its caller.)
type VectorResident interface {
	VectorByID(ctx context.Context, externalID string) ([]byte, bool, error)
}

// SegmentCoverageGate reports whether the graph's HNSW segment pool is trustworthy
// enough to resolve vectors from. ok=false means degenerate, unmeasured, or
// not-yet-wired, and the caller falls back to the server drain; reason carries the
// short explanation for the log line so a fallback is never silent.
//
// Only the HNSW arm is consulted because only that engine carries vectors —
// gating on any-arm degeneracy would let a degenerate BM25 arm veto perfectly good
// vector resolution.
type SegmentCoverageGate interface {
	HNSWCoverageTrustworthy(ctx context.Context) (ok bool, reason string, err error)
}

// WithVectorDeps attaches the OPTIONAL resident vector-resolution seam and its
// coverage gate, returning the loop for fluent construction (the shape of
// WithTopicDeps). Either argument may be nil, and a nil pair is DEGRADED mode: leaf
// attachment takes the existing server drain, byte-identical to the pre-resident
// behavior. Nil-tolerant on p.
func (p *PropagationLoop) WithVectorDeps(resident VectorResident, gate SegmentCoverageGate) *PropagationLoop {
	if p == nil {
		return nil
	}
	p.vectorResident = resident
	p.coverageGate = gate
	return p
}
