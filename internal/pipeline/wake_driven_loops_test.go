// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
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
// discovery loop: with no wake it reads the catalog ZERO times, and each
// wakeCatalog signal buys exactly one enumeration. This is the assertion that
// would fail if the periodic timer came back — a cadence would keep the counter
// climbing through the silent window.
//
// The zero has a known-positive control in the same run: the same counter,
// measured the same way, goes to 1 and then 2 as wakes arrive. A counter wired
// to nothing would stay at zero through those legs too.
func TestRefreshLoopSilentWithoutWake(t *testing.T) {
	fake := newFakeWireClient()
	fake.seedGraphNames(kgtypes.GraphCode, "repoA")
	p := New(Config{}, fake, nil, nil)

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

	// No wake has been signaled: the loop must be parked, having issued nothing.
	time.Sleep(loopSettle)
	require.Zero(t, fake.codeGraphNamesReads(),
		"a wake-driven loop must read the catalog ZERO times until something signals it")

	// One wake buys exactly one enumeration — and no more once it settles again.
	p.wakeCatalog()
	require.Eventually(t, func() bool {
		return fake.codeGraphNamesReads() == 1
	}, 5*time.Second, 5*time.Millisecond, "one wakeCatalog must drive exactly one enumeration")
	time.Sleep(loopSettle)
	assert.Equal(t, 1, fake.codeGraphNamesReads(),
		"the loop must park again after the pass rather than re-firing on a cadence")

	// A second wake buys exactly one more: the loop is still live, not exited.
	p.wakeCatalog()
	require.Eventually(t, func() bool {
		return fake.codeGraphNamesReads() == 2
	}, 5*time.Second, 5*time.Millisecond, "the next wakeCatalog must drive exactly one more enumeration")
	time.Sleep(loopSettle)
	assert.Equal(t, 2, fake.codeGraphNamesReads(),
		"still one enumeration per wake, with no cadence underneath")
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
