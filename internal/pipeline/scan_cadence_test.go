// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/workingset"
)

// TestScanTickFor_BackendAware proves the self-DDoS fix: a collector
// bound to a REMOTE (logged-in) backend polls at the slow Config.CloudTick,
// while a local-bound collector keeps the fast Config.Tick. The cadence is
// chosen from the resolver's live login state, so a login flip (which
// re-registers every collector) re-derives it.
func TestScanTickFor_BackendAware(t *testing.T) {
	cfg := Config{Tick: 7 * time.Millisecond, CloudTick: 11 * time.Second, IdleTickMax: 90 * time.Second}
	fb := newFlippableBackend()
	p := New(cfg, fb, nil, nil)
	ctx := context.Background()

	fb.loggedIn.Store(false)
	base, idleMax := p.cadenceFor(ctx)
	assert.Equal(t, 7*time.Millisecond, base, "logged-out collector must use the fast local Tick")
	assert.Equal(t, 7*time.Millisecond, idleMax, "local collector must NOT idle-back-off (idleMax == base)")

	fb.loggedIn.Store(true)
	base, idleMax = p.cadenceFor(ctx)
	assert.Equal(t, 11*time.Second, base,
		"logged-in collector must use the slow CloudTick base (keeps remote scan volume under the gateway rate limit)")
	assert.Equal(t, 90*time.Second, idleMax, "logged-in collector must idle-back-off up to IdleTickMax")

	require.NoError(t, p.Stop(ctx))
}

// TestCadenceFor_Defaults confirms the zero-config fallbacks: 250ms local,
// 5s cloud base, 60s cloud idle ceiling — and that a nil resolver (test fakes /
// no login seam) takes the local cadence with idle-backoff disabled.
func TestCadenceFor_Defaults(t *testing.T) {
	assert.Equal(t, 250*time.Millisecond, Config{}.TickOrDefault())
	assert.Equal(t, 5*time.Second, Config{}.CloudTickOrDefault())
	assert.Equal(t, time.Hour, Config{}.IdleTickMaxOrDefault())

	// nil resolver → local cadence, idle-backoff disabled.
	p := &Pipeline{cfg: Config{}}
	base, idleMax := p.cadenceFor(context.Background())
	assert.Equal(t, 250*time.Millisecond, base)
	assert.Equal(t, 250*time.Millisecond, idleMax)
}

// TestSleepForWake_WakesEarly proves a wake signal cuts a long sleep short —
// the mechanism that lets a collect re-scan an hour-idle graph promptly — and
// that the wake arm reports byWake=true (the auto-heal-arm signal).
func TestSleepForWake_WakesEarly(t *testing.T) {
	c := &collector{cfg: Config{}}
	wake := make(chan struct{}, 1)
	type res struct{ alive, byWake bool }
	done := make(chan res, 1)
	go func() {
		alive, byWake := c.sleepForWake(context.Background(), time.Hour, wake)
		done <- res{alive, byWake}
	}()

	wake <- struct{}{}
	select {
	case got := <-done:
		assert.True(t, got.alive, "woken sleep returns alive=true (continue, not ctx-cancel)")
		assert.True(t, got.byWake, "the <-wake arm reports byWake=true")
	case <-time.After(2 * time.Second):
		t.Fatal("sleepForWake did not return early on wake")
	}
}

// TestSleepForWake_TimerReturnsNotByWake proves a plain timer expiry (no wake
// signal) returns alive=true, byWake=false — so the heal arm fires ONLY on a
// collect-wake, never on an idle timer tick.
func TestSleepForWake_TimerReturnsNotByWake(t *testing.T) {
	c := &collector{cfg: Config{}}
	wake := make(chan struct{}, 1)
	alive, byWake := c.sleepForWake(context.Background(), 5*time.Millisecond, wake)
	assert.True(t, alive, "a timer expiry returns alive=true")
	assert.False(t, byWake, "a timer expiry reports byWake=false (not a collect-wake)")
}

// TestWakeAll_RegistersAndClears proves each registered collector gets a wake
// entry, WakeAll is non-blocking + coalescing (safe to call repeatedly), and
// Stop clears the registry.
func TestWakeAll_RegistersAndClears(t *testing.T) {
	fb := newFlippableBackend()
	fb.loggedIn.Store(true)
	// Hour-long cadence so the collectors register then sleep — WakeAll is what
	// would rouse them; here we just assert the wiring, not the timing.
	p := New(Config{CloudTick: time.Hour, IdleTickMax: time.Hour}, fb, nil, nil)
	// Two interacted-with graphs: the pass registers a collector per working-set
	// member, so the members are what produce the wake entries counted below.
	ws := workingset.New()
	require.True(t, ws.Admit(kgtypes.GraphCode, "g1", "collect"))
	require.True(t, ws.Admit(kgtypes.GraphCode, "g2", "collect"))
	p.AttachWorkingSet(ws)
	ctx := context.Background()
	p.refreshOnce(ctx)

	p.collectorMu.Lock()
	nWakes := len(p.collectorWakes)
	nCancels := len(p.collectorCancels)
	p.collectorMu.Unlock()
	// 1:1 invariant: every registered collector carries a wake entry. The count is
	// asserted against the FIXTURE's two admitted graphs rather than against
	// nCancels alone — two counts that lost the same members would still be equal.
	assert.Equal(t, nCancels, nWakes, "each registered collector carries a wake entry")
	assert.Equal(t, 2, nWakes, "one wake entry per admitted code graph")

	p.WakeAll() // must not block / panic
	p.WakeAll() // coalesces — a second call with a signal already queued is a no-op

	require.NoError(t, p.Stop(ctx))

	p.collectorMu.Lock()
	nAfter := len(p.collectorWakes)
	p.collectorMu.Unlock()
	assert.Zero(t, nAfter, "Stop clears the wake registry")
}

// TestNextIdleInterval covers the idle-backoff growth step: geometric doubling
// capped at max, and idleMax == base disabling growth.
func TestNextIdleInterval(t *testing.T) {
	// Doubles toward the cap.
	assert.Equal(t, 10*time.Second, nextIdleInterval(5*time.Second, 60*time.Second))
	assert.Equal(t, 60*time.Second, nextIdleInterval(40*time.Second, 60*time.Second)) // 80 capped to 60
	assert.Equal(t, 60*time.Second, nextIdleInterval(60*time.Second, 60*time.Second)) // already at cap
	// idleMax == base disables growth (local case).
	assert.Equal(t, 250*time.Millisecond, nextIdleInterval(250*time.Millisecond, 250*time.Millisecond))
}
