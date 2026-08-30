// SPDX-License-Identifier: Apache-2.0

package veckernel

import (
	"fmt"
	"runtime"
	"testing"
)

// perarm_controls_test.go proves the agreement gates fire AGAINST EACH TIER'S
// OWN KERNEL, on the machine where that tier actually executes.
//
// WHY THIS IS NOT ALREADY COVERED BY controls_test.go. Those controls drive the
// graders with wrong kernels built out of dotF32Unroll4 — portable Go that runs
// identically everywhere. They prove the GRADER works. They do not prove the
// grader would catch a defect in a particular ASSEMBLY tier, because the
// assembly never enters the measurement: a tier whose kernel returned a constant
// would sail through every one of them.
//
// The controls here take the tier's REAL kernel, break it in a way that
// corresponds to a real assembly defect, and require the gate to reject it. A
// tier that cannot run on this host is skipped LOUDLY by name, because a control
// that silently did not run is the same as no control at all — and these tiers
// are exactly the ones a laptop cannot execute.
//
// EACH CONTROL'S DEFECT SIZE IS KNOWN BEFORE THE RUN rather than sampled, on the
// same principle as controls_test.go: a corpus whose defect happens to land
// below the tolerance would report a blind gate as a working one.

// rowValueCorpus builds a block whose row r is filled with the constant r+1, and
// an all-ones query.
//
// It makes the GATHER controls arithmetic rather than probabilistic: the dot of
// the query with row r is exactly dim*(r+1) against a scale of exactly dim*(r+1),
// so reading row s instead of row r is an error of |s-r|/(r+1) — a number known
// in advance and, for the ids used below, far above the 1e-4 gate. A
// signed-random corpus would work most of the time and produce an unfalsifiable
// flake the rest of it.
func rowValueCorpus(rows, dim int) (block, query []float32) {
	block = make([]float32, rows*dim)
	for r := range rows {
		for i := range dim {
			block[r*dim+i] = float32(r + 1)
		}
	}
	return block, rep(1, dim)
}

// TestPerArmOracleGateFiresOnThatArmsOwnKernel is the RED PROOF for each tier's
// scalar dot: break the tier's own kernel, and the oracle gate must reject it.
func TestPerArmOracleGateFiresOnThatArmsOwnKernel(t *testing.T) {
	reportTierCoverage(t)

	// Ones corpus: dropping the final term shifts the result by exactly 1
	// against a scale of exactly dim, so the error is 1/dim — 4.9e-4 at the
	// widest width here, comfortably above the 1e-4 gate, and larger at every
	// narrower one.
	for _, a := range testArms() {
		t.Run(a.name, func(t *testing.T) {
			dropLast := func(x, y []float32) float32 {
				if len(x) == 0 {
					return 0
				}
				return a.dot(x[:len(x)-1], y[:len(y)-1])
			}
			for _, dim := range allDims {
				x, y := onesCorpus(dim)
				err := gradeAgainstOracle("wrong/"+a.name, dropLast, x, y)
				if err == nil {
					t.Fatalf("GATE IS BLIND FOR TIER %s at dim=%d: a kernel built from this "+
						"tier's own assembly with its last term dropped was ACCEPTED. Every "+
						"agreement result reported for %s on this host is therefore unproven.",
						a.name, dim, a.name)
				}
				if dim == allDims[len(allDims)-1] {
					t.Logf("gate fired as required at dim=%d: %v", dim, err)
				}
			}
		})
	}
}

// TestPerArmGatherGateFiresOnThatArmsOwnKernel is the same red proof for the
// FUSED BATCH kernel, which on every assembly tier is a different kernel from
// the scalar dot — four rows at once, its own tail handling, its own fold.
//
// The simulated defect is a row-addressing error: the tier's real gather is
// asked for a rotated id list, so every slot carries a correct dot product of
// the WRONG row. That is what a bad row-pointer computation produces, and it is
// invisible to any check that only asks whether the numbers look plausible.
func TestPerArmGatherGateFiresOnThatArmsOwnKernel(t *testing.T) {
	reportTierCoverage(t)

	const rows = 64
	// Dims spanning each tier's ladder: below one vector, past a group with a
	// remainder, and both production extremes.
	dims := []int{3, 17, 63, 256, 2048}
	// Id-list lengths across every residue of the four-row grouping.
	lens := []int{1, 2, 3, 4, 5, 7, 8, 9}

	for _, a := range testArms() {
		t.Run(a.name, func(t *testing.T) {
			for _, dim := range dims {
				block, query := rowValueCorpus(rows, dim)
				for _, n := range lens {
					ids := make([]uint32, n)
					for i := range ids {
						ids[i] = uint32(i)
					}
					// Rotate by one: slot i now receives row i+1's dot.
					rotated := make([]uint32, n)
					for i := range rotated {
						rotated[i] = ids[(i+1)%n]
					}

					got := make([]float32, n)
					a.gather(got, query, block, dim, rotated)

					rejected := 0
					for i, id := range ids {
						row := block[int(id)*dim : int(id)*dim+dim]
						label := fmt.Sprintf("wrong/%s dim=%d n=%d slot=%d", a.name, dim, n, i)
						if err := gradeScalarAgainstOracle(label, float64(got[i]), query, row); err != nil {
							rejected++
						}
					}
					// n==1 rotates onto itself, so no slot is wrong and nothing
					// should be rejected — the control is genuinely absent there
					// and demanding a rejection would demand a false positive.
					if n == 1 {
						if rejected != 0 {
							t.Errorf("dim=%d n=1: a rotation by one over a single id is the "+
								"identity, so nothing should have been rejected; %d were", dim, rejected)
						}
						continue
					}
					if rejected == 0 {
						t.Fatalf("GATE IS BLIND FOR TIER %s at dim=%d n=%d: every slot of a "+
							"row-rotated gather was accepted. A row-addressing defect in this "+
							"tier's fused kernel would pass the suite.", a.name, dim, n)
					}
				}
			}
			t.Logf("gather gate fired on %s at every dim in %v for every id-list length past 1",
				a.name, dims)
		})
	}
}

// TestPerArmGatesAcceptTheRealKernel is the negative control on both of the
// above: the same graders, the same corpora, the tier's UNBROKEN kernel, and
// they must all be accepted.
//
// Without it, a grader that rejected everything would satisfy both red proofs
// while making the whole agreement suite meaningless.
func TestPerArmGatesAcceptTheRealKernel(t *testing.T) {
	const rows = 64

	for _, a := range testArms() {
		t.Run(a.name, func(t *testing.T) {
			for _, dim := range allDims {
				x, y := onesCorpus(dim)
				if err := gradeAgainstOracle(a.name, a.dot, x, y); err != nil {
					t.Errorf("dot: grader rejected the CORRECT kernel at dim=%d: %v", dim, err)
				}
			}
			for _, dim := range []int{3, 17, 63, 256, 2048} {
				block, query := rowValueCorpus(rows, dim)
				ids := []uint32{0, 1, 2, 3, 4, 5, 6}
				got := make([]float32, len(ids))
				a.gather(got, query, block, dim, ids)
				for i, id := range ids {
					row := block[int(id)*dim : int(id)*dim+dim]
					label := fmt.Sprintf("%s dim=%d slot=%d", a.name, dim, i)
					if err := gradeScalarAgainstOracle(label, float64(got[i]), query, row); err != nil {
						t.Errorf("gather: grader rejected the CORRECT kernel: %v", err)
					}
				}
			}
		})
	}
}

// reportTierCoverage names every compiled tier and states whether the red proofs
// in this file could reach it.
//
// A tier this silicon cannot execute is absent from testArms, so the controls
// above never see it. That absence has to be VISIBLE: "the amd64 AVX-512 gate
// was proven to fire" and "the amd64 AVX-512 gate was never exercised because
// this host has no AVX-512" produce the same green suite otherwise, and the
// difference between them is the whole reason these kernels are benchmarked on
// hardware rather than graded on a laptop.
func reportTierCoverage(t *testing.T) {
	t.Helper()
	for _, ts := range Tiers() {
		if ts.Supported {
			t.Logf("RED PROOF COVERS %-14s on %s/%s", ts.Name, runtime.GOOS, runtime.GOARCH)
			continue
		}
		t.Logf("RED PROOF DOES NOT COVER %-14s on %s/%s — NOT EXECUTABLE HERE: %s",
			ts.Name, runtime.GOOS, runtime.GOARCH, ts.Reason)
	}
}
