// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"strings"

	. "github.com/mmcloughlin/avo/build"   //nolint:staticcheck // avo's documented dot-import style
	. "github.com/mmcloughlin/avo/operand" //nolint:staticcheck // avo's documented dot-import style
	"github.com/mmcloughlin/avo/reg"
)

// gather.go emits the FUSED FOUR-ROW kernel for each amd64 tier: one query
// scored against four candidate rows in a single pass, holding each chunk of
// the query in a register across all four.
//
// THIS FUSION IS THE ARM'S REASON FOR EXISTING. The shoot-out that preceded
// this package measured an unfused batch — one that re-reads the query per row
// — losing 1.5-1.7x to a fused one, a larger effect than the language or
// call-boundary differences the gap was first attributed to.
//
// TWO ACCUMULATORS PER ROW, not one. The main loop consumes two vector widths
// of query per iteration and gives each half its own accumulator, so each row
// carries two independent FMA dependency chains instead of one serialized
// chain. The arm64 kernel is built the same way and for the same reason.
//
// ONE EMITTER, TWO TIERS. AVX2 and AVX-512 differ here only in register width
// and in the fold, so they are generated from the same code with a vecKind
// supplying both. That is not tidiness: it makes "the two tiers run the same
// algorithm at different widths" a structural fact rather than a claim two
// hand-maintained copies would drift away from.

// vecKind is everything that differs between the AVX2 and the AVX-512 emission.
type vecKind struct {
	// name is the tier's short name and the label prefix.
	name string
	// lanes is how many float32s one register of this width holds.
	lanes int
	// alloc allocates a fresh virtual register of this width.
	alloc func() reg.VecVirtual
	// zero clears one, using the narrowest instruction the tier is allowed.
	zero func(reg.VecVirtual)
	// hsum folds one down to lane 0 of an XMM.
	hsum func(reg.VecVirtual) reg.VecVirtual
}

var (
	kindAVX2 = vecKind{
		name: "avx2", lanes: laneY,
		alloc: func() reg.VecVirtual { return YMM() }, zero: zeroY, hsum: hsumY,
	}
	kindAVX512 = vecKind{
		name: "avx512", lanes: laneZ,
		alloc: func() reg.VecVirtual { return ZMM() }, zero: zeroZ, hsum: hsumZ,
	}
)

// gatherAccs are the per-row accumulators: two vector halves plus a scalar
// remainder, for each of the four rows.
type gatherAccs struct {
	lo, hi, scalar []reg.VecVirtual
}

// -- software prefetch ------------------------------------------------------
//
// THE CAP IS THE WHOLE MECHANISM, and it is load-bearing rather than cautious.
// Prefetching a candidate row IN FULL is actively harmful at production widths:
// measured on an M4 Max, issuing the whole row one group ahead cost +12.7% at
// dim 1024 and +35.4% at dim 2048 against no prefetch at all. Prefetching only
// the first kilobyte of each row helped where anything helped and never
// regressed.
//
// WHAT IT IS HONESTLY WORTH, so nobody reads more into this code than the
// measurement supports: about 8% at dim 256, 3-5% at dim 512, and nothing
// distinguishable from run-to-run drift at dim 768 and above. That figure comes
// from a probe whose own two identical baselines differed by 4.3% at dim 256,
// which is why it is quoted as "about 8%" rather than to a decimal.
const (
	// pfCapBytes is how much of each upcoming row is prefetched. One kilobyte —
	// sixteen 64-byte lines on amd64, eight 128-byte lines on arm64.
	pfCapBytes = 1024

	// pfLineBytes is the amd64 cache line the prefetch cursor steps by.
	pfLineBytes = 64
)

// gatherPtrs loads the query pointer, the row pointers, the prefetch pointers
// and the length.
func gatherPtrs(rows int) (q reg.Register, r, pf []reg.Register, n reg.Register) {
	q = Load(Param("q"), GP64())
	for i := range rows {
		r = append(r, Load(Param(fmt.Sprintf("r%d", i)), GP64()))
	}
	for i := range rows {
		pf = append(pf, Load(Param(fmt.Sprintf("p%d", i)), GP64()))
	}
	n = Load(Param("n"), GP64())
	return q, r, pf, n
}

func newGatherAccs(k vecKind, rows int) gatherAccs {
	a := gatherAccs{
		lo:     make([]reg.VecVirtual, rows),
		hi:     make([]reg.VecVirtual, rows),
		scalar: make([]reg.VecVirtual, rows),
	}
	for i := range rows {
		a.lo[i], a.hi[i], a.scalar[i] = k.alloc(), k.alloc(), XMM()
		k.zero(a.lo[i])
		k.zero(a.hi[i])
		zeroX(a.scalar[i])
	}
	return a
}

// advanceAll bumps the query pointer and every row pointer by n bytes.
func advanceAll(n int, q reg.Register, rows []reg.Register) {
	advance(n, append([]reg.Register{q}, rows...)...)
}

// gatherLabel builds a label unique to one tier's kernel. The loop labels carry
// the FLOAT COUNT they consume per iteration rather than the register count, so
// the assembly reads the same way the tail-exhaustion table in tail_test.go
// describes it.
func gatherLabel(k vecKind, rows int, suffix string) string {
	return fmt.Sprintf("gather%d_%s_%s", rows, k.name, suffix)
}

func gatherAVX2() {
	emitGather("dotF32x4AVX2", kindAVX2, 4,
		"dotF32x4AVX2 scores one query against FOUR rows in a single pass using",
		"AVX2 with FMA, returning the four dot products.",
		"",
		"Each 16-float chunk of the query is loaded ONCE into two YMM registers",
		"and multiply-added against the same chunk of all four rows, so the query",
		"costs two loads per four distances instead of eight. The four rows are",
		"never loaded into registers at all: VFMADD231PS reads them straight from",
		"memory, which is what keeps two query registers plus eight accumulators",
		"inside the sixteen YMM registers amd64 has.",
		"",
		"p0..p3 are the NEXT group's rows, software-prefetched one kilobyte deep",
		"while this group is scored. Pass the CURRENT rows when there is no next",
		"group, or when dim is too small for the cap to fall inside a row — the",
		"prefetch then targets lines already being read and costs nothing.",
		"",
		"n is TRUSTED. avx2Gather has already been validated by DotF32Gather.",
	)
}

func gatherAVX512() {
	emitGather("dotF32x4AVX512", kindAVX512, 4,
		"dotF32x4AVX512 scores one query against FOUR rows in a single pass using",
		"AVX-512F, returning the four dot products.",
		"",
		"Same fusion as dotF32x4AVX2 at twice the width: each 32-float chunk of",
		"the query is held in two ZMM registers across all four rows, and the rows",
		"are read as memory operands of VFMADD231PS.",
		"",
		"p0..p3 are the next group's rows; see dotF32x4AVX2.",
		"",
		"Needs no AVX-512 extension beyond F, for the reasons dotF32AVX512 gives;",
		"the Requires: line avo emits above this function in the .s file is the",
		"authority on the exact feature set. n is trusted.",
	)
}

func emitGather(fn string, k vecKind, rows int, doc ...string) {
	var sb strings.Builder
	sb.WriteString("func(q")
	for i := range rows {
		fmt.Fprintf(&sb, ", r%d", i)
	}
	for i := range rows {
		fmt.Fprintf(&sb, ", p%d", i)
	}
	sb.WriteString(" *float32, n int) (")
	for i := range rows {
		if i > 0 {
			sb.WriteString(", ")
		}
		fmt.Fprintf(&sb, "d%d", i)
	}
	sb.WriteString(" float32)")
	sig := sb.String()

	TEXT(fn, NOSPLIT, sig)
	Doc(doc...)
	Pragma("noescape")

	q, r, pf, n := gatherPtrs(rows)
	acc := newGatherAccs(k, rows)

	// pfLeft counts down the iterations that still prefetch. Initialized to the
	// cap in whole main-loop strides, so the cursor never runs past one kilobyte
	// into a row it was not asked to touch.
	pfLeft := GP64()
	MOVQ(U32(uint32(pfCapBytes/(2*k.lanes*f32))), pfLeft)

	gatherWideLoop(k, rows, q, r, pf, n, acc, pfLeft)
	gatherNarrowLoop(k, rows, q, r, n, acc)
	gatherScalarLoop(k, rows, q, r, n, acc)
	gatherReduce(k, rows, acc)
}

// gatherWideLoop consumes TWO register widths of query per iteration, giving
// each half its own per-row accumulator so every row keeps two independent FMA
// chains in flight, and issues the capped prefetch for the next group.
func gatherWideLoop(k vecKind, rows int, q reg.Register, r, pf []reg.Register, n reg.Register, acc gatherAccs, pfLeft reg.Register) {
	wide := 2 * k.lanes
	body := gatherLabel(k, rows, fmt.Sprintf("loop%d", wide))
	next := gatherLabel(k, rows, fmt.Sprintf("loop%d", k.lanes))
	skip := gatherLabel(k, rows, "pfdone")

	Label(body)
	CMPQ(n, U32(uint32(wide)))
	JL(LabelRef(next))

	// ONE PREDICTABLE BRANCH, taken for the first pfCapBytes of each row and not
	// taken thereafter. Cheaper than the alternatives: peeling the prefetching
	// iterations would duplicate the whole body, and prefetching unconditionally
	// would walk off the end of the cap into rows nobody asked for.
	TESTQ(pfLeft, pfLeft)
	JZ(LabelRef(skip))
	for i := range rows {
		for off := 0; off < wide*f32; off += pfLineBytes {
			PREFETCHT0(Mem{Base: pf[i], Disp: off})
		}
	}
	advance(wide*f32, pf...)
	DECQ(pfLeft)
	Label(skip)

	q0, q1 := k.alloc(), k.alloc()
	VMOVUPS(Mem{Base: q}, q0)
	VMOVUPS(Mem{Base: q, Disp: k.lanes * f32}, q1)
	for i := range rows {
		VFMADD231PS(Mem{Base: r[i]}, q0, acc.lo[i])
		VFMADD231PS(Mem{Base: r[i], Disp: k.lanes * f32}, q1, acc.hi[i])
	}
	advanceAll(wide*f32, q, r)
	SUBQ(U32(uint32(wide)), n)
	JMP(LabelRef(body))
}

// gatherNarrowLoop drains one register width at a time into the lo accumulators.
func gatherNarrowLoop(k vecKind, rows int, q reg.Register, r []reg.Register, n reg.Register, acc gatherAccs) {
	body := gatherLabel(k, rows, fmt.Sprintf("loop%d", k.lanes))
	next := gatherLabel(k, rows, "loop1")

	Label(body)
	CMPQ(n, U32(uint32(k.lanes)))
	JL(LabelRef(next))

	qv := k.alloc()
	VMOVUPS(Mem{Base: q}, qv)
	for i := range rows {
		VFMADD231PS(Mem{Base: r[i]}, qv, acc.lo[i])
	}
	advanceAll(k.lanes*f32, q, r)
	SUBQ(U32(uint32(k.lanes)), n)
	JMP(LabelRef(body))
}

// gatherScalarLoop is the per-row remainder, reached by any dim the tier's
// vector width does not divide. EVERY PRODUCTION WIDTH IS such a multiple, so on
// a production embedding this loop executes zero times; it is graded by the
// tail-exhaustion sweep over dims 1..300 rather than by any production traffic.
func gatherScalarLoop(k vecKind, rows int, q reg.Register, r []reg.Register, n reg.Register, acc gatherAccs) {
	body := gatherLabel(k, rows, "loop1")
	next := gatherLabel(k, rows, "reduce")

	Label(body)
	TESTQ(n, n)
	JE(LabelRef(next))

	qs := XMM()
	VMOVSS(Mem{Base: q}, qs)
	for i := range rows {
		VFMADD231SS(Mem{Base: r[i]}, qs, acc.scalar[i])
	}
	advanceAll(f32, q, r)
	DECQ(n)
	JMP(LabelRef(body))
}

func gatherReduce(k vecKind, rows int, acc gatherAccs) {
	Label(gatherLabel(k, rows, "reduce"))
	out := make([]reg.VecVirtual, rows)
	for i := range rows {
		VADDPS(acc.hi[i], acc.lo[i], acc.lo[i])
		out[i] = k.hsum(acc.lo[i])
		VADDSS(acc.scalar[i], out[i], out[i])
	}
	VZEROUPPER()
	for i := range rows {
		Store(out[i], ReturnIndex(i))
	}
	RET()
}
