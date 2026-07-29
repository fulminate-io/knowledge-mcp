// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestPropagateValence_Residual (FAILS-WHEN-ABSENT) asserts the 4th return value
// is the final residual: a non-zero leftover gap on a non-converged maxIter-capped
// run, and ~0 on a converged run.
func TestPropagateValence_Residual(t *testing.T) {
	// Diameter-limited N-node path: a row-stochastic chain p0..p{N-1} (interior nodes
	// split 0.5/0.5 to their two neighbors, endpoints listen 1.0 to their single
	// neighbor) with opposing endpoint valence (+1 / -1) and uncharged interior.
	// Valence diffuses at most one hop per iteration, so with maxIter < N-1 the two
	// fronts cannot cross the path within the cap: the run exhausts maxIter without
	// converging and the residual is the leftover non-zero gap. (The old fixture here
	// was the period-2 oscillating 2-cycle, which now CONVERGES to [0,0] under the
	// self-loop damping — this path fixture is the replacement that
	// genuinely fails to converge under damping, preserving the residual-at-cap
	// contract this test exists to prove.)
	const n = 12
	pathIDs := make([]string, n)
	pathIndex := make(map[string]int, n)
	pathRows := make([][]SparseEntry, n)
	for i := range n {
		pathIDs[i] = fmt.Sprintf("p%d", i)
		pathIndex[fmt.Sprintf("p%d", i)] = i
	}
	for i := range n {
		switch {
		case i == 0:
			pathRows[i] = []SparseEntry{{Col: 1, Val: 1.0}}
		case i == n-1:
			pathRows[i] = []SparseEntry{{Col: n - 2, Val: 1.0}}
		default:
			pathRows[i] = []SparseEntry{{Col: i - 1, Val: 0.5}, {Col: i + 1, Val: 0.5}}
		}
	}
	path := TrustMatrix{IDs: pathIDs, IDIndex: pathIndex, Rows: pathRows}
	const maxIter = 5 // < N-1 (=11): the diameter cannot be crossed within the cap.
	_, iters, converged, residual := PropagateValence(
		path, map[string]float64{"p0": 1.0, fmt.Sprintf("p%d", n-1): -1.0}, maxIter, defaultEpsilon)
	assert.False(t, converged, "a path longer than the cap cannot equilibrate within maxIter")
	assert.Equal(t, maxIter, iters, "a non-converged run exhausts maxIter")
	assert.Greater(t, residual, 0.0, "the residual is the leftover non-zero gap at cap")

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
