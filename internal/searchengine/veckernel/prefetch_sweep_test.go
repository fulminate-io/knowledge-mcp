// SPDX-License-Identifier: Apache-2.0

//go:build (arm64 || amd64) && !veckernel_noasm

package veckernel

import (
	"runtime"
	"testing"
)

// TestPrefetchScheduleSweep measures every schedule the four-slot ABI can express
// and prints the table.
//
// IT IS THE MEASUREMENT THE KNOBS WERE ADDED FOR. Parameterising depth and span
// without ever sweeping them leaves a tunable whose only justification is that it
// could be tuned — and the review rightly flagged that activePrefetchSchedule has
// no production writer, so the default is the only shape that ever runs. This is
// what turns the parameter from a possibility into a result.
//
// A NULL IS A RESULT AND IS RECORDED AS ONE. The research this came from calls
// prefetch magnitude "the number most likely to disappoint", because the
// marginal-bandwidth decomposition (111.3 / 77.3 / 60.6 GB/s across the width
// steps — a monotone slope with no cliff) suggests this machine may already be
// bandwidth-saturated rather than latency-stalled. If no schedule beats the
// default, that is the finding; it is NOT grounds to keep tuning until a number
// appears, and it is NOT grounds to ship a non-default schedule.
//
// Harvest-gated for the same reason the pins are: it is a multi-second timing
// run over a corpus deliberately larger than this host's cache.
func TestPrefetchScheduleSweep(t *testing.T) {
	requirePerfEnabled(t)

	prev := activePrefetchSchedule
	t.Cleanup(func() { activePrefetchSchedule = prev })

	// Every (depth, span) the four slots can hold. depth*span > pfSlots is not
	// expressible — the ABI's width is the budget.
	schedules := []pfSchedule{
		{depth: 4, span: 1}, // the default: four vectors, one chunk each
		{depth: 3, span: 1},
		{depth: 2, span: 2}, // two vectors, twice as deep
		{depth: 2, span: 1},
		{depth: 1, span: 4}, // one vector, fully covered to 4 KiB
		{depth: 1, span: 2},
		{depth: 1, span: 1},
	}

	t.Logf("PREFETCH SWEEP on %s/%s, class %q — ns/distance by schedule, wired-traverse shape",
		runtime.GOOS, runtime.GOARCH, machineClass())

	for _, a := range testArms() {
		if a.name == TierReference {
			continue // the portable kernel issues no prefetch; sweeping it measures noise
		}
		for _, dim := range []int{256, 2048} {
			var base float64
			for _, s := range schedules {
				activePrefetchSchedule = s
				m := measureWiredTraverseNsPerDistance(a, dim)
				if s == pfDefaultSchedule {
					base = m.Min
				}
				rel := 0.0
				if base > 0 {
					rel = m.Min / base
				}
				t.Logf("SWEEP %s dim=%-5d depth=%d span=%d -> %8.1f ns/distance  (x%.3f of default)  spread %.2f",
					a.name, dim, s.depth, s.span, m.Min, rel, m.SpreadRatio())
			}
		}
	}
}
