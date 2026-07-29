// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestDamping_ConvergesPeriod2Oscillator (FAILS-WHEN-ABSENT) is the RED-first
// reproduction for self-loop damping. A raw 2-cycle (a listens only to b, b listens
// only to a — the off-diagonal swap) with opposite initial valences [1,-1] is the
// classic period-2 mode: every UNDAMPED iteration flips the pair, so it never settles
// and the run exhausts maxIter with converged==false. With d=0.5 self-loop damping the
// oscillating mode is killed and both nodes settle at the fixed point 0 within a couple
// of iterations.
func TestDamping_ConvergesPeriod2Oscillator(t *testing.T) {
	oscillating := TrustMatrix{
		IDs:     []string{"a", "b"},
		IDIndex: map[string]int{"a": 0, "b": 1},
		Rows: [][]SparseEntry{
			{{Col: 1, Val: 1.0}}, // a listens only to b
			{{Col: 0, Val: 1.0}}, // b listens only to a
		},
	}
	result, iters, converged, residual := PropagateValence(
		oscillating, map[string]float64{"a": 1.0, "b": -1.0}, defaultMaxIterations, defaultEpsilon)

	assert.True(t, converged, "self-loop damping settles the period-2 oscillator that the undamped step orbits forever")
	assert.Less(t, iters, 5, "damping collapses the oscillation in a couple of iterations, not the full cap")
	assert.Less(t, residual, defaultEpsilon, "a converged run reports a sub-epsilon residual")
	// Whole-map equality (not per-scalar assert.Equal) keeps the byte-exact intent while
	// staying clear of testifylint's float-compare rule — the pair settles at exactly 0.
	assert.Equal(t, map[string]float64{"a": 0.0, "b": 0.0}, result, "the oscillator settles at the fixed point [0,0]")
}

// TestDamping_PreservesFixedPoints (FAILS-WHEN-ABSENT) is the byte-identity
// characterization guard: fixtures that are ALREADY at their fixed point must return
// output exactly equal to their input — undamped today, and still exactly equal after
// the damped update lands (damping must not disturb converged components).
//
// NOTE TO FUTURE EDITORS: the byte-exact equality below holds ONLY because the damping
// factor d=0.5 is a power of two and every fixture value (0.5) self-halves exactly in
// IEEE-754 — 0.5*x + 0.5*(M*x) rounds to x with no error. Do NOT "improve" these
// fixtures with non-power-of-two weights or values (0.3, 1/3, ...): the damped average
// would then round and these exact-equality assertions would legitimately break. Keep
// the values power-of-two.
func TestDamping_PreservesFixedPoints(t *testing.T) {
	// Identity (pure self-trust): every node is trivially a fixed point at any value,
	// so valence never moves and the run converges on the first iteration.
	identity := TrustMatrix{
		IDs:     []string{"a", "b"},
		IDIndex: map[string]int{"a": 0, "b": 1},
		Rows: [][]SparseEntry{
			{{Col: 0, Val: 1.0}},
			{{Col: 1, Val: 1.0}},
		},
	}
	idInit := map[string]float64{"a": 0.5, "b": -0.5}
	idOut, _, idConv, _ := PropagateValence(identity, idInit, defaultMaxIterations, defaultEpsilon)
	assert.True(t, idConv, "an identity (self-trust) matrix converges immediately")
	// Whole-map equality asserts the output is byte-identical to the input fixed point
	// (and dodges testifylint's per-scalar float-compare rule).
	assert.Equal(t, idInit, idOut, "identity fixed point is byte-identical to its input")

	// Uniform 3-ring (a→b→c→a, each listens fully to its single successor): a uniform
	// initial vector is a fixed point because M*v = v whenever every entry of v is equal.
	ring := TrustMatrix{
		IDs:     []string{"a", "b", "c"},
		IDIndex: map[string]int{"a": 0, "b": 1, "c": 2},
		Rows: [][]SparseEntry{
			{{Col: 1, Val: 1.0}}, // a listens to b
			{{Col: 2, Val: 1.0}}, // b listens to c
			{{Col: 0, Val: 1.0}}, // c listens to a
		},
	}
	ringInit := map[string]float64{"a": 0.5, "b": 0.5, "c": 0.5}
	ringOut, _, ringConv, _ := PropagateValence(ring, ringInit, defaultMaxIterations, defaultEpsilon)
	assert.True(t, ringConv, "a uniform-valued ring is a fixed point and converges immediately")
	assert.Equal(t, ringInit, ringOut, "uniform ring fixed point is byte-identical to its input")
}
