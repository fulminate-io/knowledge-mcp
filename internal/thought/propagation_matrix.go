// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"fmt"
	"math"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// propagation_matrix.go holds the trust-matrix construction + the DeGroot
// valence/magnitude iteration primitives + the influence-vector power iteration.
// The RunPropagation* orchestration (component closure, scoping, diff writeback)
// lives in propagation.go and drives these primitives per connected component.

// TrustMatrix is a row-stochastic matrix encoding how much each thought
// trusts (listens to) each other thought's valence/magnitude. Stored
// per-row in sparse form (typical nnz/row 4-10 vs N=5066+).
type TrustMatrix struct {
	IDs     []string
	IDIndex map[string]int
	Rows    [][]SparseEntry
}

// SparseEntry is one (column, value) pair in a TrustMatrix row.
type SparseEntry struct {
	Col int
	Val float64
}

const (
	defaultMaxIterations = 100
	// defaultEpsilon is the DeGroot PROPAGATION convergence epsilon (PropagateValence
	// / PropagateMagnitude + the explicit RunPropagationScoped passes). 1e-4 sits
	// below the 2-decimal propagation display precision — iterating to a tighter gap
	// only refines digits no consumer renders, so it is wasted work. Distinct from
	// influenceEpsilon by design: the influence eigenvector is frozen and must NOT
	// ride a loosened propagation epsilon.
	defaultEpsilon = 1e-4
	// influenceEpsilon is the power-iteration termination epsilon for
	// ComputeInfluenceVector ONLY. Held at 1e-6 to keep the influence eigenvector
	// ranking BYTE-UNCHANGED across the propagation-epsilon split — the eigenvector
	// is a frozen invariant (no matrix re-weighting), so it must not move when the
	// propagation epsilon loosens.
	influenceEpsilon = 1e-6
	magnitudeDecay   = 0.7
	// valenceSelfDamping is the lazy-iteration self-loop damping factor d applied to
	// the PropagateValence DeGroot step: vNext = (1-d)*v + d*(M*v). Structurally this
	// iterates the lazy walk (1-d)I + d*M instead of M. With d=0.5 every eigenvalue λ
	// of the row-stochastic M maps to (1-d)+dλ, shifting the spectrum into the right
	// half of the unit disk — so the period-2 mode at λ=-1 (which M leaves undamped,
	// flipping the pair every iteration forever) maps to 0 and is killed outright, and
	// every other mode also lands inside the disk and converges. It does NOT make the
	// path monotone (complex eigenvalues still spiral inward, i.e. can overshoot); the
	// guarantee is convergence without sustained oscillation, not no-overshoot. d=0.5
	// keeps already-converged fixed points exactly in place (a power of two averages
	// without rounding for power-of-two values).
	valenceSelfDamping = 0.5
	// writebackDeadband is the minimum change in a persisted propagated_* value that
	// justifies re-persisting it. Pinned to defaultEpsilon (1e-4): a recomputed value
	// within this band of the persisted value is not written back. This sits below any
	// observable consumer effect — persisted propagated values are never rendered
	// directly; they feed only recall's magnitude sort and the valence/magnitude
	// threshold filters. A sub-deadband-stale value can only reorder genuine near-ties,
	// which the non-stable sort already resolves arbitrarily, and can only sit
	// sub-deadband from a filter threshold — below the granularity any consumer
	// distinguishes. The band suppresses churn writes from values that merely jittered
	// below what any reader can observe.
	writebackDeadband = defaultEpsilon
)

// propagatedValueChangedBand is the per-key band predicate for the propagated_*
// writeback diff: it reports whether want differs from the persisted cur by at least
// band — the drift below which a re-persist is not worthwhile. An unset persisted
// value (cur == "") ALWAYS writes — this first-persist guard is load-bearing: without
// a stored value, consumers fall back to the raw Properties value, so the propagated
// value must be written at least once. Otherwise the write is gated on
// |want - cur| >= band. The band is chosen PER COMPONENT by the caller: a converged
// component floors at writebackDeadband, a non-converged component widens it to the
// component's own oscillation amplitude so residual jitter — below what any consumer
// can observe — stops re-writing every tick.
func propagatedValueChangedBand(cur, want string, band float64) bool {
	if cur == "" {
		return true // first-persist guard: an unset value must always be written.
	}
	return math.Abs(parseFloat(want)-parseFloat(cur)) >= band
}

// propagatedValueChanged is the fixed-band specialization of
// propagatedValueChangedBand at the writebackDeadband floor — the predicate for a
// propagated_* diff with no per-component band, and the exact behavior in effect
// before the per-component widening.
func propagatedValueChanged(cur, want string) bool {
	return propagatedValueChangedBand(cur, want, writebackDeadband)
}

// BuildTrustMatrix constructs a row-stochastic trust matrix for the
// given thoughts. Thin wrapper over BuildTrustMatrixWithCharges that drops the
// charge map — its (TrustMatrix, error) signature is the contract every caller
// that does not need the charge map relies on (BuildTrustMatrixWithPersonality,
// BlindSpotInfluenceVector, the propagation drivers).
func BuildTrustMatrix(ctx context.Context, gc Caller, thoughtIDs []string, now time.Time, src CorpusSource) (TrustMatrix, error) {
	matrix, _, err := BuildTrustMatrixWithCharges(ctx, gc, thoughtIDs, now, src)
	return matrix, err
}

// BuildTrustMatrixWithCharges constructs the trust matrix AND surfaces the
// full-corpus charge map it already fetched, so a caller that needs per-thought
// charge data (e.g. ReflectInfluence's evidence-aware partition) reuses it
// instead of issuing a second full-corpus charge read. It builds the cross-edge
// fanout via fetchAdjacency(ctx, gc, "all", thoughtIDs, src) — the reduced
// Execute-seam adjacency composition (a paged type-browse + one bulk edges read,
// no legacy adjacency tool op) — and the self-trust charge map via
// chargeMapForThoughts (→ fetchChargesFor, the bulk-charge Execute seam).
// src is the resident corpus seam: a warm CorpusSource serves the thought-node
// arm of that adjacency read from the in-process cache, so a warm caller pays no
// paged browse at all; nil (an on-demand handler with no loop in hand, or a test)
// keeps the full drain.
// fillSparseRows operates entirely from those two prebuilt maps; the charge map
// is returned rather than discarded.
func BuildTrustMatrixWithCharges(ctx context.Context, gc Caller, thoughtIDs []string, now time.Time, src CorpusSource) (TrustMatrix, map[string][]*knowledgev1.Node, error) {
	n := len(thoughtIDs)
	if n == 0 {
		return TrustMatrix{}, nil, nil
	}
	idIndex := make(map[string]int, n)
	for i, id := range thoughtIDs {
		idIndex[id] = i
	}
	_, adj, err := fetchAdjacency(ctx, gc, "all", thoughtIDs, src) // resident cache when warm; else drain.
	if err != nil {
		return TrustMatrix{}, nil, fmt.Errorf("thought: BuildTrustMatrix: adjacency: %w", err)
	}
	chargeMap := chargeMapForThoughts(ctx, gc, thoughtIDs, src)

	rows := fillSparseRows(thoughtIDs, idIndex, adj, chargeMap, now)
	normalizeSparseRows(rows)
	return TrustMatrix{IDs: thoughtIDs, IDIndex: idIndex, Rows: rows}, chargeMap, nil
}

// fillSparseRows builds the sparse trust-matrix rows from the prebuilt
// adjacency map + charge map. No wire calls.
func fillSparseRows(thoughtIDs []string, idIndex map[string]int, adj map[string][]string, chargeMap map[string][]*knowledgev1.Node, now time.Time) [][]SparseEntry {
	n := len(thoughtIDs)
	rows := make([][]SparseEntry, n)
	seen := make([]bool, n)
	for i, id := range thoughtIDs {
		for k := range seen {
			seen[k] = false
		}
		var row []SparseEntry
		for _, other := range adj[id] {
			j, ok := idIndex[other]
			if !ok || j == i || seen[j] {
				continue
			}
			seen[j] = true
			row = append(row, SparseEntry{Col: j, Val: 1.0})
		}
		row = append(row, SparseEntry{Col: i, Val: computePropertiesFromCharges(chargeMap[id], now).SelfTrust})
		sortRowByCol(row)
		rows[i] = row
	}
	return rows
}

func sortRowByCol(row []SparseEntry) {
	for i := 1; i < len(row); i++ {
		for j := i; j > 0 && row[j-1].Col > row[j].Col; j-- {
			row[j-1], row[j] = row[j], row[j-1]
		}
	}
}

func normalizeSparseRows(rows [][]SparseEntry) {
	for i, row := range rows {
		rowSum := 0.0
		for _, e := range row {
			rowSum += e.Val
		}
		if rowSum > 0 {
			for j := range row {
				row[j].Val /= rowSum
			}
			continue
		}
		rows[i] = []SparseEntry{{Col: i, Val: 1.0}}
	}
}

// PropagateValence runs DeGroot iterations on valence until convergence. The 4th
// return value is the final residual — the maxDelta at loop exit, ~0 on a
// converged run and the leftover gap on a maxIter-capped non-converged one.
func PropagateValence(matrix TrustMatrix, initialValence map[string]float64, maxIter int, epsilon float64) (map[string]float64, int, bool, float64) {
	n := len(matrix.IDs)
	if n == 0 {
		return nil, 0, true, 0
	}
	if maxIter <= 0 {
		maxIter = defaultMaxIterations
	}
	if epsilon <= 0 {
		epsilon = defaultEpsilon
	}
	v := make([]float64, n)
	for i, id := range matrix.IDs {
		v[i] = initialValence[id]
	}
	vNext := make([]float64, n)
	var iterations int
	var residual float64
	converged := false
	for iter := range maxIter {
		maxDelta := 0.0
		for i := range n {
			sum := 0.0
			for _, e := range matrix.Rows[i] {
				sum += e.Val * v[e.Col]
			}
			// Lazy-iteration self-loop damping: blend the previous value with the
			// neighbor consensus instead of jumping fully to it, which kills the
			// period-2 oscillating mode (λ=-1) that the undamped step orbits forever.
			vNext[i] = (1-valenceSelfDamping)*v[i] + valenceSelfDamping*sum
			delta := math.Abs(vNext[i] - v[i])
			if delta > maxDelta {
				maxDelta = delta
			}
		}
		copy(v, vNext)
		iterations = iter + 1
		residual = maxDelta
		if maxDelta < epsilon {
			converged = true
			break
		}
	}
	result := make(map[string]float64, n)
	for i, id := range matrix.IDs {
		result[id] = v[i]
	}
	return result, iterations, converged, residual
}

// PropagateMagnitude uses decay-attenuated propagation. The 4th return value is
// the final residual — the maxDelta at loop exit, ~0 on a converged run and the
// leftover gap on a maxIter-capped non-converged one.
func PropagateMagnitude(matrix TrustMatrix, localMagnitude map[string]float64, maxIter int, epsilon float64) (map[string]float64, int, bool, float64) {
	n := len(matrix.IDs)
	if n == 0 {
		return nil, 0, true, 0
	}
	if maxIter <= 0 {
		maxIter = defaultMaxIterations
	}
	if epsilon <= 0 {
		epsilon = defaultEpsilon
	}
	m := make([]float64, n)
	local := make([]float64, n)
	for i, id := range matrix.IDs {
		m[i] = localMagnitude[id]
		local[i] = localMagnitude[id]
	}
	var iterations int
	var residual float64
	converged := false
	for iter := range maxIter {
		maxDelta := 0.0
		for i := range n {
			maxNeighbor := 0.0
			for _, e := range matrix.Rows[i] {
				if e.Col == i {
					continue
				}
				if m[e.Col] > maxNeighbor {
					maxNeighbor = m[e.Col]
				}
			}
			newM := math.Max(local[i], magnitudeDecay*maxNeighbor)
			delta := newM - m[i]
			if delta > maxDelta {
				maxDelta = delta
			}
			m[i] = newM
		}
		iterations = iter + 1
		residual = maxDelta
		if maxDelta < epsilon {
			converged = true
			break
		}
	}
	result := make(map[string]float64, n)
	for i, id := range matrix.IDs {
		result[id] = m[i]
	}
	return result, iterations, converged, residual
}

// ComputeInfluenceVector computes the left eigenvector of T via power
// iteration. Pure local computation.
func ComputeInfluenceVector(matrix TrustMatrix) map[string]float64 {
	n := len(matrix.IDs)
	if n == 0 {
		return nil
	}
	s := make([]float64, n)
	for i := range s {
		s[i] = 1.0 / float64(n)
	}
	sNext := make([]float64, n)
	for range defaultMaxIterations {
		for j := range sNext {
			sNext[j] = 0
		}
		for i := range n {
			si := s[i]
			for _, e := range matrix.Rows[i] {
				sNext[e.Col] += si * e.Val
			}
		}
		total := 0.0
		for _, v := range sNext {
			total += v
		}
		if total > 0 {
			for j := range sNext {
				sNext[j] /= total
			}
		}
		maxDelta := 0.0
		for i := range n {
			d := math.Abs(sNext[i] - s[i])
			if d > maxDelta {
				maxDelta = d
			}
		}
		copy(s, sNext)
		// influenceEpsilon (NOT defaultEpsilon): the eigenvector ranking is frozen,
		// so its convergence threshold stays at 1e-6 independent of the loosened
		// propagation epsilon.
		if maxDelta < influenceEpsilon {
			break
		}
	}
	result := make(map[string]float64, n)
	for i, id := range matrix.IDs {
		result[id] = s[i]
	}
	return result
}

// buildComponentMatrix builds the TrustMatrix for one connected
// component, reusing the prebuilt adj subset and chargeMap.
// Personality scalars are applied if profile != nil.
func buildComponentMatrix(component []string, adj map[string][]string, chargeMap map[string][]*knowledgev1.Node, profile *PersonalityProfile, nodeByID map[string]*knowledgev1.Node, now time.Time) TrustMatrix {
	idIndex := make(map[string]int, len(component))
	for i, id := range component {
		idIndex[id] = i
	}
	rows := fillSparseRows(component, idIndex, adj, chargeMap, now)
	normalizeSparseRows(rows)
	matrix := TrustMatrix{IDs: component, IDIndex: idIndex, Rows: rows}
	if profile != nil {
		thoughtToCluster := make(map[string]string, len(component))
		for _, id := range component {
			if n, ok := nodeByID[id]; ok {
				if cid := kgtypes.Value(n, "cluster_id"); cid != "" {
					thoughtToCluster[id] = cid
				}
			}
		}
		n := len(matrix.IDs)
		for i := range n {
			applyPersonalityScalarsToRow(matrix, i, thoughtToCluster, *profile)
			renormalizeSparseRow(matrix.Rows[i])
		}
	}
	return matrix
}
