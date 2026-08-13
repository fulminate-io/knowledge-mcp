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

// These are the behavioral gates for the two wake-driven background loops. They
// drive the REAL loop goroutines (not the once-pass helpers the other tests
// call directly), because what they assert is precisely what the loop body
// decides: whether anything happens when nothing signals it.
//
// loopSettle is how long each test waits while asserting that NOTHING is issued.
// It has to outlast the cadence these loops used to run at (Config.Tick defaults
// to 250ms) so the silence is evidence of the wake-driven shape rather than of a
// window too short for a timer to fire in.
const loopSettle = 400 * time.Millisecond

// TestRefreshLoopSilentWithoutWake is the traffic-collapse gate for the catalog
// loop. Its subject SURVIVED the move to a working-set-derived wanted set — the
// loop still parks and still does exactly one pass per wake — but WHAT A PASS
// COSTS did not: a pass now reads a local map, so the catalog-read counter is
// pinned at ZERO for the whole test rather than climbing once per wake. Both
// properties are asserted here: the permanent zero, and the one-pass-per-wake
// shape measured on an observable that still moves.
//
// The observable is REGISTRATION. To make a wakeCatalog pass do visible work
// without itself signaling anything, the test unregisters an admitted graph's
// collector by hand: the working set still wants it, so the next pass re-registers
// it, and no pass at all means it stays absent. That absence-then-return is the
// known-positive control — a probe wired to nothing could not produce the return.
func TestRefreshLoopSilentWithoutWake(t *testing.T) {
	fake := newFakeWireClient()
	// A catalog the backend WOULD serve. Nothing may ever read it — that is what
	// the permanent zero below asserts.
	fake.seedGraphNames(kgtypes.GraphCode, "repoA")
	p := New(Config{}, fake, nil, nil)
	repoA := graphKey{GraphType: kgtypes.GraphCode, GraphName: "repoA"}
	ws := workingset.New()
	require.True(t, ws.Admit(kgtypes.GraphCode, "repoA", "collect"))
	p.AttachWorkingSet(ws)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.RefreshLoadedGraphs(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		require.NoError(t, p.Stop(context.Background()))
	})

	// The loop's entry pass registers the admitted graph. Everything after this
	// measures what further wakes do.
	require.Eventually(t, func() bool {
		_, ok := registeredKeys(p)[repoA]
		return ok
	}, 5*time.Second, 5*time.Millisecond, "the loop's entry pass registers the admitted graph")

	// Nothing signals the loop: it must be parked, doing no work at all.
	p.UnregisterGraph(repoA.GraphType, repoA.GraphName)
	time.Sleep(loopSettle)
	_, stillGone := registeredKeys(p)[repoA]
	require.False(t, stillGone,
		"a wake-driven loop must run ZERO passes until something signals it — a cadence would have re-registered")
	require.Zero(t, fake.codeGraphNamesReads(),
		"and a pass costs no catalog read at all: the wanted set is a local read")

	// One wake buys exactly one pass, which re-registers the still-wanted graph.
	p.wakeCatalog()
	require.Eventually(t, func() bool {
		_, ok := registeredKeys(p)[repoA]
		return ok
	}, 5*time.Second, 5*time.Millisecond, "one wakeCatalog must drive exactly one pass")

	// A second cycle: the loop is still live, not exited, and still silent between
	// wakes.
	p.UnregisterGraph(repoA.GraphType, repoA.GraphName)
	time.Sleep(loopSettle)
	_, goneAgain := registeredKeys(p)[repoA]
	require.False(t, goneAgain, "still parked between wakes — no cadence underneath")
	p.wakeCatalog()
	require.Eventually(t, func() bool {
		_, ok := registeredKeys(p)[repoA]
		return ok
	}, 5*time.Second, 5*time.Millisecond, "the next wakeCatalog drives exactly one more pass")

	assert.Zero(t, fake.codeGraphNamesReads(),
		"across every pass the loop ran, the catalog was never read once")
}

// TestGenPollLoopSeedThenWakeOnly is the same gate for the bulk gen-poll loop,
// with the one deliberate difference between the two: this loop polls ONCE at
// start before it ever parks. That seed is what populates the shared gen snapshot;
// without it every collector's discover finds its graph unknown and falls through
// to a real PipelineScan on each of its own ticks, which is the fan-out the
// two-phase protocol exists to remove. So the assertion is exactly one poll at
// start, none while nothing signals, and one more per WakeAll.
func TestGenPollLoopSeedThenWakeOnly(t *testing.T) {
	fake := newFakeWireClient()
	p := genPollTestPipeline(t, fake)
	registerStubCollector(p, "repoA")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.RunGenPollLoop(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	// The seed poll: exactly one, unprompted, so the snapshot is populated.
	require.Eventually(t, func() bool {
		return fake.genPollCallCount() == 1
	}, 5*time.Second, 5*time.Millisecond, "the loop must issue exactly ONE seed poll at start")

	// Then silence: no cadence underneath the seed.
	time.Sleep(loopSettle)
	require.Equal(t, 1, fake.genPollCallCount(),
		"after the seed the loop must park — zero further polls until something signals it")

	// Each WakeAll buys exactly one more poll. These legs are also the
	// known-positive control for the silence asserted above: a counter wired to
	// nothing could not move here either.
	for want := 2; want <= 3; want++ {
		p.WakeAll()
		require.Eventually(t, func() bool {
			return fake.genPollCallCount() == want
		}, 5*time.Second, 5*time.Millisecond, "WakeAll must drive exactly one more poll (expected %d)", want)
		time.Sleep(loopSettle)
		require.Equal(t, want, fake.genPollCallCount(),
			"one poll per wake, then park again (expected %d)", want)
	}
}
