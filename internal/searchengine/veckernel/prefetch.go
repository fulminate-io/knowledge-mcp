// SPDX-License-Identifier: Apache-2.0

//go:build (arm64 || amd64) && !veckernel_noasm

package veckernel

// prefetch.go holds the CALLER-SIDE half of the software prefetch: which rows
// each fused gather is told to pull in while it scores the current group.
//
// It is shared by both architectures on purpose. The cap, the row-size gate and
// the no-next-group rule are policy, not instruction selection — the only part
// that differs per tier is which mnemonic issues the hint (PRFM on arm64,
// PREFETCHT0 on amd64) and how many lines one loop iteration covers. Two copies
// of the policy would be two chances for the gate to drift away from the cap the
// kernels were generated with.

// pfMinDim is the row width below which the next-group prefetch is not issued.
//
// GATED ON ROW SIZE, and the gate is arithmetic rather than taste: the kernels
// prefetch a fixed one kilobyte into each upcoming row, so below dim 256 — a
// 1 KiB row — the cursor would run past the row it was aimed at and pull in
// whatever happens to sit after it. That is not merely wasted bandwidth, it is
// bandwidth spent evicting something useful.
//
// It is also the width where prefetching stops being worth anything. Measured on
// an M4 Max: about 8% at dim 256, 3-5% at 512, and nothing distinguishable from
// run-to-run drift at 768 and above. Prefetching an even smaller row would be
// paying the issue cost for a row that is already one or two cache lines.
const pfMinDim = pfCapBytesGo / 4

// pfCapBytesGo mirrors the generator's pfCapBytes. The two must agree: the
// generator sizes the in-kernel countdown from it and this file decides when the
// cap fits inside a row. TestPrefetchCapAgreesWithTheKernel pins that.
const pfCapBytesGo = 1024

// pfSlots is how many prefetch pointers the fused kernels accept. It is fixed by
// the assembly ABI, not a tunable: dotF32x4NEON and its amd64 twins take exactly
// four, and each pulls pfCapBytesGo from the address it is given.
const pfSlots = 4

// pfLineBytes is the cache-line size this schedule is reasoned about in,
// MEASURED first-hand on this machine rather than assumed to be 64.
const pfLineBytes = 128

// pfLinesPerSlot is how many cache lines one prefetch slot actually covers, and
// it is what makes "span" meaningful as a coverage number rather than an opaque
// count: one slot pulls pfCapBytesGo, which at the measured line size is eight
// lines of the row it points at. A span of 2 therefore covers sixteen.
//
// It is DERIVED rather than written down so the two constants above cannot drift
// apart from the number quoted in the reasoning; a test asserts the value.
const pfLinesPerSlot = pfCapBytesGo / pfLineBytes

// pfSchedule decides how the four slots are SPENT: across distinct upcoming
// vectors, or deeper into fewer of them.
//
// WHY BOTH KNOBS EXIST. The reference shape aims one slot at each of the next
// four vectors, covering pfCapBytesGo of each — one kilobyte, eight lines. At
// dim 2048 a row is 8 KiB, so that shape reaches 12.5% of a vector it is about
// to read in full. Published work faults exactly this (a first-cache-line-only
// prefetch) and reports a head-plus-spread schedule doing better, so which of
// the two axes to spend a slot on is a question worth being able to ASK rather
// than one answered by inheritance.
//
// depth counts distinct upcoming vectors; span counts pfCapBytesGo-sized chunks
// within each. depth*span is bounded by pfSlots — the ABI's width is the budget,
// so buying more coverage of one vector necessarily buys fewer vectors.
type pfSchedule struct {
	depth int
	span  int
}

// pfDefaultSchedule IS TODAY'S EFFECTIVE BEHAVIOUR, exactly: four vectors, one
// chunk each. It is spelled out as a schedule rather than left implicit so this
// parameterisation cannot move the pinned cells by accident — a change in the
// numbers below is a deliberate change to what the kernels are told, and the
// pin table is what would catch it.
var pfDefaultSchedule = pfSchedule{depth: pfSlots, span: 1}

// activePrefetchSchedule is the schedule prefetchTargets consults. It is a
// package var so a measurement harness can vary it; production never writes it.
var activePrefetchSchedule = pfDefaultSchedule

// prefetchTargets picks the rows and offsets the kernel should prefetch while it
// scores the current group, per the active schedule.
//
// WHEN THERE IS NOTHING USEFUL TO PREFETCH IT RETURNS THE CURRENT ROWS rather
// than nil, and that is deliberate. The kernel would need a branch to skip a nil
// group, and a branch inside the hot loop costs more than the prefetch it
// avoids; aiming the prefetch at lines the kernel is about to read anyway is
// architecturally free — the lines are already in flight or already resident.
// It also keeps every pointer the assembly receives a valid, mapped address,
// which is worth more than the alternative's theoretical tidiness. That same
// rule fills any slot the schedule leaves unspent.
//
// THE CAP IS A FLOOR OF THE DESIGN, NOT A CEILING TO REMOVE: a slot still pulls
// pfCapBytesGo and no more, and span never aims a slot past the end of the row
// it belongs to — which is the same arithmetic pfMinDim enforces for the first
// chunk, applied to the later ones.
// IT MUST STAY INLINABLE, AND THAT IS A MEASURED CONSTRAINT RATHER THAN A
// PREFERENCE. This function runs once per four-row group inside the gather's hot
// loop. An earlier version read the schedule and walked it here, which cost 147
// against the inliner's budget of 80 (`go build -gcflags=-m=2`) — so it stopped
// being inlined and the gather paid a real call per group. A criterion asserts
// the inline decision directly, because the cost is invisible in a diff and a
// benchmark on a loaded machine is too noisy to catch a few percent.
//
// THE SCHEDULE BRANCH IS HOISTED INTO THE GATHERS, not evaluated here. Each
// gather reads pfScheduleIsDefault() ONCE before its loop and branches on that
// loop-invariant bool, so the default path reaches this body with nothing to
// decide. The alternative — an outlined slow path called from here — was
// measured WORSE (cost 159), because the call itself is what the budget counts.
func prefetchTargets(block []float32, dim int, ids []uint32, i int, r0, r1, r2, r3 *float32) (p0, p1, p2, p3 *float32) {
	if dim < pfMinDim || i+8 > len(ids) {
		return r0, r1, r2, r3
	}
	return &block[int(ids[i+4])*dim],
		&block[int(ids[i+5])*dim],
		&block[int(ids[i+6])*dim],
		&block[int(ids[i+7])*dim]
}

// pfScheduleIsDefault reports whether the active schedule is the reference
// shape, so a gather can hoist that question out of its per-group loop.
func pfScheduleIsDefault() bool { return activePrefetchSchedule == pfDefaultSchedule }

// prefetchTargetsScheduled is the general path: it spends the four slots
// according to the active schedule's depth and span.
//
// IT IS DELIBERATELY NOT INLINABLE AND THAT COSTS NOTHING, because it runs only
// when a non-default schedule is installed — a measurement configuration, not
// the shipped one. Keeping the general case out of the fast path is what lets
// the fast path stay under budget.
func prefetchTargetsScheduled(block []float32, dim int, ids []uint32, i int, r0, r1, r2, r3 *float32) (p0, p1, p2, p3 *float32) {
	s := activePrefetchSchedule
	if dim < pfMinDim || i+pfSlots+s.depth > len(ids) {
		return r0, r1, r2, r3
	}

	chunk := pfCapBytesGo / 4 // in float32 elements
	var out [pfSlots]*float32
	slot := 0
	for d := 0; d < s.depth && slot < pfSlots; d++ {
		row := int(ids[i+pfSlots+d]) * dim
		for c := 0; c < s.span && slot < pfSlots; c++ {
			off := c * chunk
			if off >= dim {
				break // this row has no further chunk to aim at
			}
			out[slot] = &block[row+off]
			slot++
		}
	}

	cur := [pfSlots]*float32{r0, r1, r2, r3}
	for ; slot < pfSlots; slot++ {
		out[slot] = cur[slot]
	}
	return out[0], out[1], out[2], out[3]
}
