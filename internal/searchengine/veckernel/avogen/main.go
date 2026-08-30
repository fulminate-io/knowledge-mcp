// SPDX-License-Identifier: Apache-2.0

package main

import (
	. "github.com/mmcloughlin/avo/build"   //nolint:staticcheck // avo's documented dot-import style
	. "github.com/mmcloughlin/avo/operand" //nolint:staticcheck // avo's documented dot-import style
	"github.com/mmcloughlin/avo/reg"
)

func main() {
	// The same constraint the hand-written arm64 assembly carries: amd64 only,
	// and removed entirely by the veckernel_noasm compile-time opt-out.
	ConstraintExpr("amd64,!veckernel_noasm")

	dotAVX2()
	dotAVX512()
	gatherAVX2()
	gatherAVX512()

	Generate()
}

// -- widths ------------------------------------------------------------------

const (
	// f32 is the width of one float32 in bytes.
	f32 = 4
	// laneY / laneZ are how many float32 lanes one YMM / ZMM register holds.
	laneY = 8
	laneZ = 16
)

// -- horizontal folds --------------------------------------------------------
//
// THE FOLD IS WHY THE TIERS CANNOT AGREE BIT-FOR-BIT, and it is deliberate
// rather than sloppy. Each accumulator holds 8 (AVX2) or 16 (AVX-512) partial
// sums that the reference holds in four, and float addition is not associative,
// so a different grouping is a different answer in the low bits. Every gate in
// the package is scale-relative for exactly this reason; see reference.go.

// hsumY folds a YMM's eight float32 lanes into lane 0 of an XMM.
//
// Two VHADDPS after one cross-lane VADDPS: the extract brings the upper 128
// bits down so the remaining work is a 128-bit tree. VHADDPS on identical
// operands halves the live lane count each time, so two of them take four lanes
// to one.
func hsumY(y reg.VecVirtual) reg.VecVirtual {
	hi := XMM()
	VEXTRACTF128(U8(1), y, hi)
	s := XMM()
	VADDPS(hi, y.AsX(), s)
	VHADDPS(s, s, s)
	VHADDPS(s, s, s)
	return s
}

// hsumZ folds a ZMM's sixteen float32 lanes into lane 0 of an XMM.
//
// VEXTRACTF64X4 rather than VEXTRACTF32X8: the F32X8 form is AVX512DQ, and this
// tier's advertised requirement — the one its TierStatus reason names — is
// AVX512F alone. Widening the hardware requirement in the fold while the tier
// still calls itself AVX-512 would make the dispatch gate a lie on any
// F-without-DQ part.
func hsumZ(z reg.VecVirtual) reg.VecVirtual {
	hi := YMM()
	VEXTRACTF64X4(U8(1), z, hi)
	s := YMM()
	VADDPS(hi, z.AsY(), s)
	return hsumY(s)
}

// zeroY / zeroZ clear an accumulator.
//
// zeroZ uses VPXORD, NOT VXORPS. VXORPS with ZMM operands is AVX512DQ; VPXORD
// is AVX512F. Same bits, same cost, and only one of them keeps this tier
// runnable on the silicon its name promises.
func zeroY(v reg.VecVirtual) { VXORPS(v, v, v) }
func zeroZ(v reg.VecVirtual) { VPXORD(v, v, v) }

// zeroX clears a scalar accumulator.
func zeroX(v reg.VecVirtual) { VXORPS(v, v, v) }

// -- shared loop pieces ------------------------------------------------------

// advance bumps every pointer in ptrs by n bytes.
func advance(n int, ptrs ...reg.Register) {
	for _, p := range ptrs {
		ADDQ(U32(uint32(n)), p)
	}
}
