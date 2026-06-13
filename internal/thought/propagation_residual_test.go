// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestPropagateValence_Residual (FAILS-WHEN-ABSENT) asserts the 4th return value
// is the final residual: a non-zero leftover gap on a non-converged maxIter-capped
// run, and ~0 on a converged run.
func TestPropagateValence_Residual(t *testing.T) {
	// Oscillating 2-cycle: each thought fully trusts the OTHER (off-diagonal swap),
	// so opposite initial valences flip every iteration and never converge — the
	// residual stays at the full swing, capped at maxIter.
	oscillating := TrustMatrix{
		IDs:     []string{"a", "b"},
		IDIndex: map[string]int{"a": 0, "b": 1},
		Rows: [][]SparseEntry{
			{{Col: 1, Val: 1.0}}, // a listens only to b
			{{Col: 0, Val: 1.0}}, // b listens only to a
		},
	}
	_, iters, converged, residual := PropagateValence(
		oscillating, map[string]float64{"a": 1.0, "b": -1.0}, 50, influenceEpsilon)
	assert.False(t, converged, "an oscillating 2-cycle never converges")
	assert.Equal(t, 50, iters, "a non-converged run exhausts maxIter")
	assert.Greater(t, residual, 0.5, "the residual is the leftover non-zero gap at cap")

	// Converged: pure self-trust (identity) — valence never moves, so the first
	// iteration's residual is 0 and the run converges immediately.
	identity := TrustMatrix{
		IDs:     []string{"a", "b"},
		IDIndex: map[string]int{"a": 0, "b": 1},
		Rows: [][]SparseEntry{
			{{Col: 0, Val: 1.0}},
			{{Col: 1, Val: 1.0}},
		},
	}
	_, _, conv2, residual2 := PropagateValence(
		identity, map[string]float64{"a": 0.5, "b": -0.5}, 50, influenceEpsilon)
	assert.True(t, conv2, "a pure self-trust matrix converges")
	assert.Less(t, residual2, influenceEpsilon, "a converged run's residual is ~0")
}

// TestPropagateMagnitude_Residual (FAILS-WHEN-ABSENT) mirrors the residual contract
// for the magnitude propagation 4th return value on a converged run.
func TestPropagateMagnitude_Residual(t *testing.T) {
	identity := TrustMatrix{
		IDs:     []string{"a"},
		IDIndex: map[string]int{"a": 0},
		Rows:    [][]SparseEntry{{{Col: 0, Val: 1.0}}},
	}
	_, _, conv, residual := PropagateMagnitude(identity, map[string]float64{"a": 1.0}, 50, influenceEpsilon)
	assert.True(t, conv, "a single self-trust node converges")
	assert.Less(t, residual, influenceEpsilon, "a converged magnitude run's residual is ~0")
}
