// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"

	. "github.com/mmcloughlin/avo/build"   //nolint:staticcheck // avo's documented dot-import style
	. "github.com/mmcloughlin/avo/operand" //nolint:staticcheck // avo's documented dot-import style
	"github.com/mmcloughlin/avo/reg"
)

// dot.go emits the SCALAR entry point for each amd64 tier: one vector against
// one, the shape DotF32 dispatches to.
//
// LOOP LADDER: N vector accumulators over a wide main loop, then a narrower
// loop, then a scalar remainder. Independent FMA dependency chains are what let
// the multiply-add latency overlap, and N is the number of them.
//
// N IS PER-TIER, AND THAT ASYMMETRY IS THE POINT. AVX2 gets EIGHT; AVX-512 keeps
// FOUR. The constraint is LOAD PORTS, not register count. A dot consumes two
// vectors per FMA, and with the second operand folded into the instruction each
// FMA still needs one explicit load plus one folded one — so sustained
// throughput is bounded by how many vector loads per cycle the core retires, not
// by how many chains are nominally in flight. On the AVX2-class cores this
// targets, three load ports leave four chains issuing about 1.0 FMA/cycle where
// roughly 1.5 is reachable, so more chains convert directly into throughput. The
// AVX-512-class cores have two load ports and a ZMM load moves twice the bytes,
// so four chains already saturate them and adding more would only lengthen the
// fold and widen register pressure for nothing.
//
// THE arm64 KERNEL WAS MEASURED, NOT ASSUMED, and it agrees: going from four to
// eight accumulators there is 22.6-24.4% faster cache-hot at every production
// width on an M4 Max. The amd64 split above is an ARCHITECTURAL ARGUMENT from
// port counts rather than a measurement on those parts — see README.md, which
// says so where the numbers land.
//
// THE LOAD IS FOLDED INTO THE FMA. VFMADD231PS takes its third operand from
// memory, so `b` never occupies a register at all. That is not a micro-tweak:
// it is what lets the four-row gather in gather.go hold two query registers and
// eight accumulators inside sixteen YMM registers. AArch64 has no load-op form,
// which is why the NEON kernel loads both operands explicitly.

// accsAVX2 and accsAVX512 are the per-tier accumulator counts. See the load-port
// argument in the file comment above for why they differ.
//
// AVX2 IS SIX, NOT EIGHT, AND THE REASON IS A CORRECTNESS BOUND RATHER THAN A
// PERFORMANCE ONE. Eight accumulators plus eight load temps occupies all sixteen
// legacy vector registers, so the scalar-remainder accumulator had nowhere to go
// and avo allocated it to X16 — a register that EXISTS ONLY WITH AVX-512. The
// emitted function would have carried `VADDSS X16, X0, X0` while its own
// Requires line still read "AVX, FMA3, SSE", and it would have raised an illegal
// instruction on exactly the AVX2-only silicon the tier is for. avo computes
// Requires from the instruction mnemonics, not from the registers its allocator
// chose, so it did not warn.
//
// Six accumulators plus six temps plus the scalar accumulator is thirteen of
// sixteen, which leaves the allocator no reason to reach past Y15.
// TestAVX2KernelsUseNoExtendedRegisters is the standing gate on that, because
// the next person to raise this number will not know the constraint exists.
const (
	accsAVX2   = 6
	accsAVX512 = 4
)

func dotAVX2() {
	emitDot("dotF32AVX2", kindAVX2, accsAVX2,
		"dotF32AVX2 returns the dot product of the first n float32s at a and b,",
		"using AVX2 with FMA: six YMM accumulators over a 48-float main loop,",
		"then an 8-float loop, then a scalar remainder.",
		"",
		"SIX accumulators, where the AVX-512 kernel keeps four. A dot is load-",
		"bound rather than chain-bound once the second operand is folded into the",
		"FMA, and this tier's cores have three load ports against AVX-512's two,",
		"so four chains leave issue slots unused here that they would not there.",
		"Six rather than eight is a register-budget bound, not a throughput one:",
		"eight forces the scalar accumulator into an AVX-512-only register. See",
		"accsAVX2 in avogen/dot.go.",
		"",
		"n is TRUSTED. The only caller is avx2Dot, reached through DotF32, which",
		"has already rejected mismatched lengths and short-circuited the empty",
		"case — this kernel bounds-checks nothing.",
	)
}

func dotAVX512() {
	emitDot("dotF32AVX512", kindAVX512, accsAVX512,
		"dotF32AVX512 returns the dot product of the first n float32s at a and b,",
		"using AVX-512F: four ZMM accumulators over a 64-float main loop, then a",
		"16-float loop, then a scalar remainder.",
		"",
		"FOUR accumulators, not the six the AVX2 kernel uses. Two load ports and",
		"a ZMM load that moves twice the bytes mean four chains already saturate",
		"issue; more would only lengthen the fold and add register pressure.",
		"",
		"NO AVX-512 EXTENSION BEYOND F IS NEEDED. The fold uses VEXTRACTF64X4",
		"rather than the AVX512DQ VEXTRACTF32X8, and the accumulators are zeroed",
		"with VPXORD rather than the AVX512DQ VXORPS-on-ZMM, so nothing here needs",
		"DQ, BW or VL. Read the exact requirement off the Requires: line avo",
		"emits above this function in the .s file — it also names FMA3, because",
		"the scalar remainder uses the VEX-encoded VFMADD231SS.",
		"",
		"n is trusted; see dotF32AVX2.",
	)
}

// emitDot writes one tier's scalar dot: `accs` accumulators over an
// accs*lanes-float main loop, then a single-register loop, then a scalar
// remainder.
//
// ONE EMITTER FOR BOTH TIERS, differing only in vecKind and accs, so "the two
// tiers run the same algorithm at different widths with different chain counts"
// is a structural fact rather than a claim two hand-maintained copies would
// drift away from.
func emitDot(fn string, k vecKind, accs int, doc ...string) {
	TEXT(fn, NOSPLIT, "func(a, b *float32, n int) float32")
	Doc(doc...)
	Pragma("noescape")

	ap := Load(Param("a"), GP64())
	bp := Load(Param("b"), GP64())
	n := Load(Param("n"), GP64())

	acc := make([]reg.VecVirtual, accs)
	for i := range acc {
		acc[i] = k.alloc()
		k.zero(acc[i])
	}
	// THE SCALAR ACCUMULATOR IS ZEROED HERE, AT THE TOP, WITH NO LABEL BETWEEN
	// IT AND THE FIRST INSTRUCTION. That placement is a correctness requirement,
	// not a style choice, and getting it wrong shipped a real bug.
	//
	// An earlier version deferred this to just before the scalar loop, to free a
	// register. But every loop above exits by JUMPING TO THE NEXT LOOP'S LABEL —
	// so instructions sitting between a loop's backward jump and the label it
	// exits to are UNREACHABLE ON THE EXIT PATH. The zeroing was emitted in
	// exactly that dead zone: the narrow loop's `JL scalarL` jumped straight past
	// it, and the scalar tail then accumulated into whatever the register
	// happened to hold. FuzzDotAgreement's seed #7 caught it on an AMD Milan box
	// — dim 23, every product -0.9375, reference -21.5625 and AVX2 -28.125,
	// which is 30 terms summed instead of 23.
	//
	// It costs one register through the loops, which is why accsAVX2 is six: six
	// accumulators plus six load temps plus this is thirteen of sixteen.
	sacc := XMM()
	zeroX(sacc)

	wide := accs * k.lanes
	wideL := dotLabel(k, fmt.Sprintf("loop%d", wide))
	narrowL := dotLabel(k, fmt.Sprintf("loop%d", k.lanes))
	scalarL := dotLabel(k, "loop1")
	reduceL := dotLabel(k, "reduce")

	Label(wideL)
	CMPQ(n, U32(uint32(wide)))
	JL(LabelRef(narrowL))
	// ALL THE LOADS FIRST, THEN ALL THE FMAs. Emitting load/FMA pairs instead
	// lets the register allocator notice each temp dies at its own FMA and
	// coalesce all of them onto ONE register — which it did, and which puts a
	// write-after-read hazard between every pair, serializing exactly the loads
	// that eight accumulators exist to overlap. Keeping every temp live until
	// its FMA forces distinct registers and lets the loads be in flight
	// together.
	temps := make([]reg.VecVirtual, accs)
	for i := range acc {
		temps[i] = k.alloc()
		VMOVUPS(Mem{Base: ap, Disp: k.lanes * f32 * i}, temps[i])
	}
	for i := range acc {
		VFMADD231PS(Mem{Base: bp, Disp: k.lanes * f32 * i}, temps[i], acc[i])
	}
	advance(wide*f32, ap, bp)
	SUBQ(U32(uint32(wide)), n)
	JMP(LabelRef(wideL))

	Label(narrowL)
	CMPQ(n, U32(uint32(k.lanes)))
	JL(LabelRef(scalarL))
	tn := k.alloc()
	VMOVUPS(Mem{Base: ap}, tn)
	VFMADD231PS(Mem{Base: bp}, tn, acc[0])
	advance(k.lanes*f32, ap, bp)
	SUBQ(U32(uint32(k.lanes)), n)
	JMP(LabelRef(narrowL))

	// The scalar remainder, reached by any dim that is not a multiple of this
	// tier's vector width. EVERY PRODUCTION WIDTH IS such a multiple — 256, 512,
	// 768, 1024, 1536, 2048 and 3072 are all multiples of 16 — so on a
	// production embedding this loop executes ZERO times. It exists because the
	// package accepts any dim, and it is graded by the tail-exhaustion sweep
	// over dims 1..300 rather than by any production traffic.
	Label(scalarL)
	TESTQ(n, n)
	JE(LabelRef(reduceL))
	t1 := XMM()
	VMOVSS(Mem{Base: ap}, t1)
	VFMADD231SS(Mem{Base: bp}, t1, sacc)
	advance(f32, ap, bp)
	DECQ(n)
	JMP(LabelRef(scalarL))

	Label(reduceL)
	emitDotReduce(k, acc, sacc)
}

// emitDotReduce folds the accumulators down to one float32 and returns it.
//
// PAIRWISE RATHER THAN A CHAIN INTO acc[0]: with six accumulators a serial fold
// is five dependent adds on the exit path where a tree is three levels.
func emitDotReduce(k vecKind, acc []reg.VecVirtual, sacc reg.VecVirtual) {
	for stride := 1; stride < len(acc); stride *= 2 {
		for i := 0; i+stride < len(acc); i += 2 * stride {
			VADDPS(acc[i+stride], acc[i], acc[i])
		}
	}
	out := k.hsum(acc[0])
	VADDSS(sacc, out, out)

	// VZEROUPPER before returning to Go. Leaving the upper halves dirty costs
	// the CALLER a transition penalty on its next SSE instruction, which is a
	// cost this kernel would impose on code that never asked for AVX.
	VZEROUPPER()
	Store(out, ReturnIndex(0))
	RET()
}

func dotLabel(k vecKind, suffix string) string {
	return fmt.Sprintf("dot_%s_%s", k.name, suffix)
}
