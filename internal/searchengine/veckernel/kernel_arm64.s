// SPDX-License-Identifier: Apache-2.0

//go:build arm64 && !veckernel_noasm

#include "textflag.h"

// AArch64 Advanced SIMD float32 dot kernels.
//
// ONE INSTRUCTION DOES THE WORK: VFMLA, the vector fused multiply-add. Go's
// arm64 assembler exposes no vector FLOAT add at all — VADD is integer-only —
// so every float addition in here is either a VFMLA against a vector of ones or
// a scalar FADDS. That is why the accumulator fold at the end of each kernel
// multiplies by 1.0 instead of adding: it is not a trick, it is the only vector
// float add available.
//
// FUSED MULTIPLY-ADD DOES NOT ROUND THE PRODUCT. A VFMLA computes a*b+c with a
// single rounding at the end, where the Go reference rounds the product and then
// rounds the sum. The two therefore disagree in the low bits BY DESIGN and no
// amount of care here will make them match exactly. The agreement gates are
// scale-relative for this reason and for the accumulator-count reason described
// in reference.go.

// func dotF32NEON(a, b *float32, n int) float32
//
// EIGHT accumulator vectors (32 lanes) over a 32-float main loop, then four
// accumulators over a 16-float loop, then a 4-float loop, then a scalar
// remainder. n is trusted: the Go shim is only reachable through DotF32, which
// has already checked it.
//
// EIGHT RATHER THAN FOUR IS A MEASUREMENT, not symmetry with the reference.
// Four independent multiply-add chains do not keep this core's FMA pipes busy:
// going to eight measured 22.6% / 24.1% / 23.6% / 24.4% faster at dims 256 /
// 512 / 1024 / 2048 cache-hot on an M4 Max, with results agreeing with the
// four-accumulator kernel to between 0 and 2.2e-06 relative. It also TIGHTENS
// the error bound — the standard dot bound is ~(n/k)*eps for k accumulators, so
// doubling k halves it — which is why the agreement gates did not have to move.
TEXT ·dotF32NEON(SB), NOSPLIT|NOFRAME, $0-28
	MOVD	a+0(FP), R0
	MOVD	b+8(FP), R1
	MOVD	n+16(FP), R2

	VEOR	V16.B16, V16.B16, V16.B16
	VEOR	V17.B16, V17.B16, V17.B16
	VEOR	V18.B16, V18.B16, V18.B16
	VEOR	V19.B16, V19.B16, V19.B16
	VEOR	V20.B16, V20.B16, V20.B16
	VEOR	V21.B16, V21.B16, V21.B16
	VEOR	V22.B16, V22.B16, V22.B16
	VEOR	V23.B16, V23.B16, V23.B16
	VEOR	V24.B16, V24.B16, V24.B16	// F24 = scalar remainder accumulator

loop32:
	CMP	$32, R2
	BLT	loop16
	VLD1.P	64(R0), [V0.S4, V1.S4, V2.S4, V3.S4]
	VLD1.P	64(R1), [V4.S4, V5.S4, V6.S4, V7.S4]
	VFMLA	V4.S4, V0.S4, V16.S4
	VFMLA	V5.S4, V1.S4, V17.S4
	VFMLA	V6.S4, V2.S4, V18.S4
	VFMLA	V7.S4, V3.S4, V19.S4
	VLD1.P	64(R0), [V8.S4, V9.S4, V10.S4, V11.S4]
	VLD1.P	64(R1), [V12.S4, V13.S4, V14.S4, V15.S4]
	VFMLA	V12.S4, V8.S4, V20.S4
	VFMLA	V13.S4, V9.S4, V21.S4
	VFMLA	V14.S4, V10.S4, V22.S4
	VFMLA	V15.S4, V11.S4, V23.S4
	SUB	$32, R2
	B	loop32

loop16:
	CMP	$16, R2
	BLT	loop4
	VLD1.P	64(R0), [V0.S4, V1.S4, V2.S4, V3.S4]
	VLD1.P	64(R1), [V4.S4, V5.S4, V6.S4, V7.S4]
	VFMLA	V4.S4, V0.S4, V16.S4
	VFMLA	V5.S4, V1.S4, V17.S4
	VFMLA	V6.S4, V2.S4, V18.S4
	VFMLA	V7.S4, V3.S4, V19.S4
	SUB	$16, R2
	B	loop16

loop4:
	CMP	$4, R2
	BLT	loop1
	VLD1.P	16(R0), [V0.S4]
	VLD1.P	16(R1), [V4.S4]
	VFMLA	V4.S4, V0.S4, V16.S4
	SUB	$4, R2
	B	loop4

loop1:
	CBZ	R2, reduce
	FMOVS	(R0), F0
	FMOVS	(R1), F1
	FMULS	F1, F0, F0
	FADDS	F0, F24, F24
	ADD	$4, R0
	ADD	$4, R1
	SUB	$1, R2
	B	loop1

reduce:
	FMOVS	$(1.0), F28
	VDUP	V28.S[0], V28.S4
	VFMLA	V28.S4, V17.S4, V16.S4
	VFMLA	V28.S4, V18.S4, V16.S4
	VFMLA	V28.S4, V19.S4, V16.S4
	VFMLA	V28.S4, V20.S4, V16.S4
	VFMLA	V28.S4, V21.S4, V16.S4
	VFMLA	V28.S4, V22.S4, V16.S4
	VFMLA	V28.S4, V23.S4, V16.S4

	// Horizontal sum of V16's four lanes. F16 already IS lane 0 (Sn aliases
	// Vn's low word); VDUP broadcasts each other lane so FADDS can reach it.
	VDUP	V16.S[1], V0.S4
	VDUP	V16.S[2], V1.S4
	VDUP	V16.S[3], V2.S4
	FADDS	F0, F16, F16
	FADDS	F2, F1, F1
	FADDS	F1, F16, F16
	FADDS	F24, F16, F16

	FMOVS	F16, ret+24(FP)
	RET

// func dotF32x4NEON(q, r0, r1, r2, r3, p0, p1, p2, p3 *float32, n int) (d0, d1, d2, d3 float32)
//
// The fused four-row kernel. Each 8-float chunk of the query is loaded ONCE
// into V0/V1 and multiplied against the same chunk of all four rows, so the
// query costs one load per four distances instead of four.
//
// Eight accumulators, two per row: V16..V19 take the first half of each chunk,
// V20..V23 the second. Two per row rather than one keeps two independent
// dependency chains alive per row so the multiply-add latency overlaps.
//
// p0..p3 ARE THE NEXT GROUP'S ROWS, software-prefetched ONE KILOBYTE deep while
// this group is scored — eight 128-byte lines per row, one line per main-loop
// iteration, then the countdown in R6 stops issuing.
//
// THE CAP IS THE MECHANISM. Prefetching a candidate row IN FULL is actively
// harmful at production widths: measured on this machine, one group ahead at
// full row length cost +12.7% at dim 1024 and +35.4% at dim 2048 against no
// prefetch at all. Capped at a kilobyte it helps where anything helps — about 8%
// at dim 256, 3-5% at 512 — and is indistinguishable from run-to-run drift at
// 768 and above. The caller passes the CURRENT rows when there is no next group
// or when dim is below the cap, so this kernel never branches on it.
TEXT ·dotF32x4NEON(SB), NOSPLIT|NOFRAME, $0-96
	MOVD	q+0(FP), R0
	MOVD	r0+8(FP), R1
	MOVD	r1+16(FP), R2
	MOVD	r2+24(FP), R3
	MOVD	r3+32(FP), R4
	MOVD	p0+40(FP), R7
	MOVD	p1+48(FP), R8
	MOVD	p2+56(FP), R9
	MOVD	p3+64(FP), R10
	MOVD	n+72(FP), R5
	MOVD	$8, R6			// prefetch budget: 8 lines x 128B = 1KiB per row

	VEOR	V16.B16, V16.B16, V16.B16
	VEOR	V17.B16, V17.B16, V17.B16
	VEOR	V18.B16, V18.B16, V18.B16
	VEOR	V19.B16, V19.B16, V19.B16
	VEOR	V20.B16, V20.B16, V20.B16
	VEOR	V21.B16, V21.B16, V21.B16
	VEOR	V22.B16, V22.B16, V22.B16
	VEOR	V23.B16, V23.B16, V23.B16
	VEOR	V24.B16, V24.B16, V24.B16	// F24..F27 = per-row scalar remainders
	VEOR	V25.B16, V25.B16, V25.B16
	VEOR	V26.B16, V26.B16, V26.B16
	VEOR	V27.B16, V27.B16, V27.B16

g_loop8:
	CMP	$8, R5
	BLT	g_loop4
	CBZ	R6, g_nopf
	PRFM	(R7), PLDL1KEEP
	PRFM	(R8), PLDL1KEEP
	PRFM	(R9), PLDL1KEEP
	PRFM	(R10), PLDL1KEEP
	ADD	$128, R7
	ADD	$128, R8
	ADD	$128, R9
	ADD	$128, R10
	SUB	$1, R6
g_nopf:
	VLD1.P	32(R0), [V0.S4, V1.S4]
	VLD1.P	32(R1), [V2.S4, V3.S4]
	VLD1.P	32(R2), [V4.S4, V5.S4]
	VLD1.P	32(R3), [V6.S4, V7.S4]
	VLD1.P	32(R4), [V8.S4, V9.S4]
	VFMLA	V2.S4, V0.S4, V16.S4
	VFMLA	V4.S4, V0.S4, V17.S4
	VFMLA	V6.S4, V0.S4, V18.S4
	VFMLA	V8.S4, V0.S4, V19.S4
	VFMLA	V3.S4, V1.S4, V20.S4
	VFMLA	V5.S4, V1.S4, V21.S4
	VFMLA	V7.S4, V1.S4, V22.S4
	VFMLA	V9.S4, V1.S4, V23.S4
	SUB	$8, R5
	B	g_loop8

g_loop4:
	CMP	$4, R5
	BLT	g_loop1
	VLD1.P	16(R0), [V0.S4]
	VLD1.P	16(R1), [V2.S4]
	VLD1.P	16(R2), [V4.S4]
	VLD1.P	16(R3), [V6.S4]
	VLD1.P	16(R4), [V8.S4]
	VFMLA	V2.S4, V0.S4, V16.S4
	VFMLA	V4.S4, V0.S4, V17.S4
	VFMLA	V6.S4, V0.S4, V18.S4
	VFMLA	V8.S4, V0.S4, V19.S4
	SUB	$4, R5
	B	g_loop4

g_loop1:
	CBZ	R5, g_reduce
	FMOVS	(R0), F0
	FMOVS	(R1), F1
	FMOVS	(R2), F2
	FMOVS	(R3), F3
	FMOVS	(R4), F4
	FMULS	F0, F1, F1
	FMULS	F0, F2, F2
	FMULS	F0, F3, F3
	FMULS	F0, F4, F4
	FADDS	F1, F24, F24
	FADDS	F2, F25, F25
	FADDS	F3, F26, F26
	FADDS	F4, F27, F27
	ADD	$4, R0
	ADD	$4, R1
	ADD	$4, R2
	ADD	$4, R3
	ADD	$4, R4
	SUB	$1, R5
	B	g_loop1

g_reduce:
	FMOVS	$(1.0), F28
	VDUP	V28.S[0], V28.S4
	VFMLA	V28.S4, V20.S4, V16.S4
	VFMLA	V28.S4, V21.S4, V17.S4
	VFMLA	V28.S4, V22.S4, V18.S4
	VFMLA	V28.S4, V23.S4, V19.S4

	VDUP	V16.S[1], V0.S4
	VDUP	V16.S[2], V1.S4
	VDUP	V16.S[3], V2.S4
	FADDS	F0, F16, F16
	FADDS	F2, F1, F1
	FADDS	F1, F16, F16
	FADDS	F24, F16, F16
	FMOVS	F16, d0+80(FP)

	VDUP	V17.S[1], V0.S4
	VDUP	V17.S[2], V1.S4
	VDUP	V17.S[3], V2.S4
	FADDS	F0, F17, F17
	FADDS	F2, F1, F1
	FADDS	F1, F17, F17
	FADDS	F25, F17, F17
	FMOVS	F17, d1+84(FP)

	VDUP	V18.S[1], V0.S4
	VDUP	V18.S[2], V1.S4
	VDUP	V18.S[3], V2.S4
	FADDS	F0, F18, F18
	FADDS	F2, F1, F1
	FADDS	F1, F18, F18
	FADDS	F26, F18, F18
	FMOVS	F18, d2+88(FP)

	VDUP	V19.S[1], V0.S4
	VDUP	V19.S[2], V1.S4
	VDUP	V19.S[3], V2.S4
	FADDS	F0, F19, F19
	FADDS	F2, F1, F1
	FADDS	F1, F19, F19
	FADDS	F27, F19, F19
	FMOVS	F19, d3+92(FP)

	RET
