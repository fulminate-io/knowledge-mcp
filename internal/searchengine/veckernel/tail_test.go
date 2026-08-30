// SPDX-License-Identifier: Apache-2.0

package veckernel

import (
	"fmt"
	"testing"
)

// tail_test.go is TEST CLASS (b): TAIL EXHAUSTION.
//
// Every dim from 1 to 300, plus every production width. The point is that each
// kernel is a stack of loops and a dim only exercises the paths its own residues
// reach. The strides differ per tier, which is exactly why the sweep is over
// every width rather than over a chosen few:
//
//	tier            dot ladder      gather ladder
//	arm64-neon      16 / 4 / 1      8 / 4 / 1
//	amd64-avx2      32 / 8 / 1      16 / 8 / 1
//	amd64-avx512    64 / 16 / 1     32 / 16 / 1
//	go-unroll4      4 / 1           (per-row, via the dot)
//
// Dim 1024 is a multiple of every one of those strides, so it never executes a
// single remainder instruction on ANY tier: a suite that tested only the
// production widths would leave every remainder path in the package unexecuted
// while reporting green.
//
// 1..300 covers every residue mod 64 — the widest stride any tier uses — more
// than four times over, and covers the below-one-vector-wide cases (dim < 4)
// where the main loop never runs at all and the whole answer comes out of the
// scalar remainder.

func TestTailExhaustionEveryDimTo300(t *testing.T) {
	arms := testArms()
	for _, a := range arms {
		t.Run(a.name, func(t *testing.T) {
			for dim := 1; dim <= 300; dim++ {
				x, y := seededPair(uint64(dim)*2654435761, dim)
				if err := gradeAgainstOracle(a.name, a.dot, x, y); err != nil {
					t.Errorf("dim=%d: %v", dim, err)
				}
			}
		})
	}
}

func TestTailExhaustionProductionDims(t *testing.T) {
	arms := testArms()
	// The production widths plus their immediate neighbors: a width one short
	// of a multiple of 16 is the shape that exercises the longest remainder run.
	dims := []int{}
	for _, d := range prodDims {
		dims = append(dims, d-1, d, d+1)
	}
	for _, a := range arms {
		t.Run(a.name, func(t *testing.T) {
			for _, dim := range dims {
				x, y := seededPair(uint64(dim)*40503+11, dim)
				if err := gradeAgainstOracle(a.name, a.dot, x, y); err != nil {
					t.Errorf("dim=%d: %v", dim, err)
				}
			}
		})
	}
}

// TestTailExhaustionGather sweeps the SECOND kernel's tails: the gather steps
// 8 floats per row, then 4, then 1, and separately groups ids four at a time.
// Two independent remainder dimensions, so both are swept together.
func TestTailExhaustionGather(t *testing.T) {
	const rows = 64
	arms := testArms()

	for _, a := range arms {
		t.Run(a.name, func(t *testing.T) {
			for dim := 1; dim <= 140; dim++ {
				block, query := seededBlock(uint64(dim)*99991, rows, dim)
				for n := 1; n <= 9; n++ {
					ids := scatteredIDs(uint64(dim*100+n), n, rows)
					got := make([]float32, n)
					a.gather(got, query, block, dim, ids)
					for i, id := range ids {
						row := block[int(id)*dim : int(id)*dim+dim]
						label := fmt.Sprintf("%s dim=%d n=%d slot=%d", a.name, dim, n, i)
						if err := gradeScalarAgainstOracle(label, float64(got[i]), query, row); err != nil {
							t.Error(err)
						}
					}
				}
			}
		})
	}
}

// TestEmptyInput pins the two degenerate entry points. DotF32 of nothing is
// zero, and a gather of no ids writes nothing — stated as behavior rather than
// left to whatever the loops happen to do.
func TestEmptyInput(t *testing.T) {
	if got := DotF32(nil, nil); got != 0 {
		t.Errorf("DotF32(nil, nil) = %v, want 0", got)
	}
	if got := DotF32([]float32{}, []float32{}); got != 0 {
		t.Errorf("DotF32(empty, empty) = %v, want 0", got)
	}

	dst := []float32{99, 99}
	DotF32Gather(dst, []float32{1, 2}, []float32{1, 2, 3, 4}, 2, nil)
	if dst[0] != 99 || dst[1] != 99 {
		t.Errorf("DotF32Gather with no ids wrote to dst: %v", dst)
	}
}
