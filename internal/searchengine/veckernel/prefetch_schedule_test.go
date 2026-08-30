// SPDX-License-Identifier: Apache-2.0

//go:build (arm64 || amd64) && !veckernel_noasm

package veckernel

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// withSchedule installs s for the duration of fn and restores the previous one.
func withSchedule(t *testing.T, s pfSchedule, fn func()) {
	t.Helper()
	prev := activePrefetchSchedule
	t.Cleanup(func() { activePrefetchSchedule = prev })
	activePrefetchSchedule = s
	fn()
}

// targetsFor runs prefetchTargets over a fixed block at group 0 and returns the
// four chosen addresses as ELEMENT OFFSETS into the block, which is what makes
// two schedules comparable without depending on where the allocator put the
// slice.
func targetsFor(t *testing.T, block []float32, dim int, ids []uint32) [4]int {
	t.Helper()
	cur := [4]*float32{
		&block[int(ids[0])*dim],
		&block[int(ids[1])*dim],
		&block[int(ids[2])*dim],
		&block[int(ids[3])*dim],
	}
	// MIRRORS THE GATHERS' OWN DISPATCH, deliberately. Each gather hoists
	// pfScheduleIsDefault() out of its loop and branches on it, so testing
	// prefetchTargets alone would test the fast path against a schedule the fast
	// path does not read — and would pass no matter what the general path did.
	var p0, p1, p2, p3 *float32
	if pfScheduleIsDefault() {
		p0, p1, p2, p3 = prefetchTargets(block, dim, ids, 0, cur[0], cur[1], cur[2], cur[3])
	} else {
		p0, p1, p2, p3 = prefetchTargetsScheduled(block, dim, ids, 0, cur[0], cur[1], cur[2], cur[3])
	}
	var out [4]int
	for k, p := range [4]*float32{p0, p1, p2, p3} {
		out[k] = offsetOf(p, block)
	}
	return out
}

// offsetOf resolves p to its element index within block by identity search. A
// linear scan is fine here — the fixtures are small — and it avoids doing
// pointer arithmetic in a test, which is the thing under test.
func offsetOf(p *float32, block []float32) int {
	for i := range block {
		if &block[i] == p {
			return i
		}
	}
	return -1
}

// TestPrefetchDepthAndSpanAreHonored proves both knobs REACH THE SCHEDULE rather
// than merely existing.
//
// A TEST THAT ONLY ASSERTED THE FIELDS EXIST WOULD PASS AGAINST AN
// IMPLEMENTATION THAT IGNORED THEM, which is the vacuous shape this criterion is
// written against. What is checked instead is that two distinct settings produce
// DIFFERENT ADDRESS SETS, that each set is the one its schedule specifies, and
// that the default set is byte-for-byte the pre-parameterisation behaviour.
func TestPrefetchDepthAndSpanAreHonored(t *testing.T) {
	// Not parallel: the schedule is package state.

	// dim 512 is two pfCapBytesGo chunks wide (1024 float32 elements would be
	// four; 512 elements = 2048 bytes = 2 chunks), so a span of 2 has a second
	// chunk to aim at and the span knob is genuinely exercisable.
	const dim = 512
	const rows = 16
	block := make([]float32, rows*dim)
	ids := make([]uint32, rows)
	for i := range ids {
		ids[i] = uint32(i)
	}

	// The coverage arithmetic the span knob is reasoned about, pinned so the two
	// constants it derives from cannot drift away from the figure the doc quotes.
	t.Run("one slot covers eight cache lines", func(t *testing.T) {
		require.Equal(t, 8, pfLinesPerSlot,
			"one prefetch slot pulls pfCapBytesGo, which is eight lines at the measured %d-byte line", pfLineBytes)
	})

	t.Run("default reproduces the pre-parameterisation schedule", func(t *testing.T) {
		withSchedule(t, pfDefaultSchedule, func() {
			got := targetsFor(t, block, dim, ids)
			// The reference shape: the next four vectors, each at offset 0.
			want := [4]int{4 * dim, 5 * dim, 6 * dim, 7 * dim}
			require.Equal(t, want, got,
				"the default must aim one slot at each of the next four rows — anything else moves the pinned cells")
		})
	})

	var wide, deep [4]int

	t.Run("depth 4 span 1 spreads across four vectors", func(t *testing.T) {
		withSchedule(t, pfSchedule{depth: 4, span: 1}, func() {
			wide = targetsFor(t, block, dim, ids)
			require.Equal(t, [4]int{4 * dim, 5 * dim, 6 * dim, 7 * dim}, wide)
		})
	})

	t.Run("depth 2 span 2 covers two vectors twice as deep", func(t *testing.T) {
		withSchedule(t, pfSchedule{depth: 2, span: 2}, func() {
			deep = targetsFor(t, block, dim, ids)
			chunk := pfCapBytesGo / 4
			// Two rows, each aimed at twice: offset 0 and one chunk in.
			require.Equal(t, [4]int{4 * dim, 4*dim + chunk, 5 * dim, 5*dim + chunk}, deep,
				"span must aim later slots DEEPER INTO THE SAME ROW, not at further rows")
		})
	})

	// THE DISCRIMINATING ASSERTION: the two settings must actually differ. If
	// prefetchTargets ignored its schedule both would be the reference shape and
	// this would fail — which is exactly the implementation the criterion targets.
	t.Run("two distinct settings produce different schedules", func(t *testing.T) {
		require.NotEqual(t, wide, deep,
			"depth/span are declared but not honored — both settings produced the same addresses")
	})

	// SPAN NEVER RUNS PAST THE ROW IT BELONGS TO. At dim 256 a row is exactly one
	// chunk, so a span of 2 has no second chunk to aim at and the surplus slots
	// fall back to the current rows rather than pointing into the NEXT vector,
	// which would prefetch a row the kernel is not about to read.
	t.Run("span does not overrun a row", func(t *testing.T) {
		const narrow = pfMinDim // one chunk wide by construction
		nblock := make([]float32, rows*narrow)
		withSchedule(t, pfSchedule{depth: 1, span: 4}, func() {
			got := targetsFor(t, nblock, narrow, ids)
			require.Equal(t, 4*narrow, got[0], "the one available chunk is aimed at")
			for k := 1; k < 4; k++ {
				require.Equal(t, int(ids[k])*narrow, got[k],
					"slot %d must fall back to the CURRENT row, not spill into the next vector", k)
			}
		})
	})
}
