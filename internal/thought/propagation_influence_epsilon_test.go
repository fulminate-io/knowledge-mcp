// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// referenceInfluenceVector reproduces ComputeInfluenceVector's power iteration but
// with a caller-supplied epsilon, so a test can compute the eigenvector at a
// specific convergence threshold (influenceEpsilon vs defaultEpsilon) and compare.
// It is byte-identical to ComputeInfluenceVector except the termination epsilon is
// a parameter rather than the influenceEpsilon const.
func referenceInfluenceVector(matrix TrustMatrix, epsilon float64) map[string]float64 {
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
		if maxDelta < epsilon {
			break
		}
	}
	out := make(map[string]float64, n)
	for i, id := range matrix.IDs {
		out[id] = s[i]
	}
	return out
}

// asymmetricTrustMatrix builds a small deterministic row-stochastic matrix that
// MIXES SLOWLY (a near-reducible lazy chain: heavy self-trust with a tiny
// asymmetric leak), so its power iteration is still measurably moving at the 1e-4
// threshold and only settles by 1e-6 — the discriminating fixture. The asymmetric
// leak gives it a NON-uniform stationary distribution so the slow mixing is
// observable.
func asymmetricTrustMatrix() TrustMatrix {
	ids := []string{"a", "b", "c"}
	idIndex := map[string]int{"a": 0, "b": 1, "c": 2}
	// NOT doubly-stochastic (so uniform is NOT the fixed point — the start vector
	// must actually move) and slow-mixing (heavy self-trust → second eigenvalue
	// near 1). 'a' is a near-sink: b and c leak most of their tiny off-self mass
	// toward a, giving a a higher stationary mass, reached only slowly.
	// Self-trust 0.93 is tuned so the power iteration reaches the 1e-4 threshold
	// well within the 100-iteration cap but is STILL moving at 1e-6 (which would
	// need ~130 iterations) — so the loose-epsilon reference settles while the
	// tight-epsilon reference rides the cap, making the two vectors differ.
	rows := [][]SparseEntry{
		{{Col: 0, Val: 0.93}, {Col: 1, Val: 0.07}},
		{{Col: 0, Val: 0.07}, {Col: 1, Val: 0.93}},
		{{Col: 0, Val: 0.07}, {Col: 2, Val: 0.93}},
	}
	return TrustMatrix{IDs: ids, IDIndex: idIndex, Rows: rows}
}

// TestComputeInfluenceVector_EpsilonUnchanged (FAILS-WHEN-ABSENT) proves the
// frozen-eigenvector invariant survives the epsilon split: ComputeInfluenceVector
// equals the reference vector computed at influenceEpsilon (1e-6), and is NOT the
// vector the loosened propagation epsilon (defaultEpsilon=1e-4) would produce on a
// slow-mixing matrix. Goes red if ComputeInfluenceVector accidentally rides
// defaultEpsilon.
func TestComputeInfluenceVector_EpsilonUnchanged(t *testing.T) {
	m := asymmetricTrustMatrix()

	got := ComputeInfluenceVector(m)
	refTight := referenceInfluenceVector(m, influenceEpsilon) // 1e-6 — the frozen target.
	refLoose := referenceInfluenceVector(m, defaultEpsilon)   // 1e-4 — what we must NOT be.

	// ComputeInfluenceVector must match the tight (1e-6) reference exactly.
	for id, want := range refTight {
		assert.InDelta(t, want, got[id], 1e-12,
			"ComputeInfluenceVector must equal the frozen 1e-6 eigenvector for %q", id)
	}

	// Guard the discriminator: the fixture must actually distinguish the two
	// thresholds, else the test would pass trivially even if the epsilon split broke.
	maxDiff := 0.0
	for id := range refTight {
		if d := math.Abs(refTight[id] - refLoose[id]); d > maxDiff {
			maxDiff = d
		}
	}
	require.Greater(t, maxDiff, 1e-9,
		"fixture must distinguish 1e-4 from 1e-6 convergence (else the invariant is untested)")
}
