// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestLoopFirstTickAfterMissedWake pins the catch-up: an admission that landed
// BEFORE the loop's wake waiter was registered must still produce a first tick,
// rather than leaving the loop idle until the hourly ticker.
//
// THE NIL WAKE CHANNEL IS THE POST-RACE STATE, not a simulation of it. WakeFor
// signals only a graph's FIRST admission, and a channel registered after an
// admission does not carry that admission — so once the broadcast has fired into
// nobody, no signal will ever arrive. A nil channel blocks forever in the select,
// which is exactly that steady state, while the admission predicate reports true.
//
// IT COUNTS TICKS, NOT REQUESTS. The boot cluster detection is gated on the SAME
// predicate, so with admitted=true it legitimately drains the corpus and detects
// clusters at boot on the unfixed tree: the recorded request stream is already
// non-empty there, and any stream-based assertion would be green before the fix.
// "Something happened at boot" is not evidence the tick fired.
//
// EXACTLY ONE, not merely at least one: a fix that both drains the pending wake
// and ticks would produce a duplicate first pass, and the equality is what catches
// it. Deliberately NOT parallel — Start's boot detection claims the process-global
// reflection single-flight guard, the same reason its sibling gate tests are serial.
func TestLoopFirstTickAfterMissedWake(t *testing.T) {
	// Same fixture as the working-set gate tests: the recorder serves as both the
	// caller and the corpus scanner, and the nil wake is the missed-wake state.
	p, _ := gatedLoop(func() bool { return true })

	var ticks atomic.Int64
	p.onTick = func() { ticks.Add(1) }

	p.Start()
	t.Cleanup(func() { p.Stop(2 * time.Second) })

	deadline := time.Now().Add(time.Second)
	for ticks.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	// Settle past the catch-up so a second, duplicate tick would be observed here
	// rather than racing the assertion below.
	time.Sleep(100 * time.Millisecond)

	got := ticks.Load()
	t.Logf("FIRST-TICK-COUNT=%d", got)
	assert.Equal(t, int64(1), got,
		"an admission that preceded the wake registration must still produce exactly one "+
			"first tick: 0 means the wake was lost and the loop idles until the hourly ticker, "+
			"more than 1 means the catch-up ran without consuming the wake it subsumes")
}
