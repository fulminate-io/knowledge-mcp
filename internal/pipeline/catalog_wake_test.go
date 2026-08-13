// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/workingset"
)

// drainWake reports whether a token was queued on ch, consuming it if so.
func drainWake(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// TestWakeCatalogCoalesces pins the catalog-discovery trigger to the same
// buffered(1) coalescing contract WakeAll's genPollWake carries: New constructs the
// channel with capacity 1, a wake queues exactly one token, repeated wakes with no
// consumer coalesce onto that single token rather than blocking, and a drained
// channel is empty again.
func TestWakeCatalogCoalesces(t *testing.T) {
	p := New(Config{}, newFakeWireClient(), nil, nil)

	require.NotNil(t, p.catalogWake, "New must construct catalogWake")
	assert.Equal(t, 1, cap(p.catalogWake), "catalogWake must be buffered(1)")

	// The known-positive control for the emptiness assertions below: a fresh
	// pipeline has nothing queued, so a queued token can only come from a wake.
	require.False(t, drainWake(p.catalogWake), "a fresh pipeline must have no queued catalog wake")

	// Many wakes with no consumer must not block, and must coalesce to ONE token.
	for range 5 {
		p.wakeCatalog()
	}
	assert.True(t, drainWake(p.catalogWake), "wakeCatalog must queue a token")
	assert.False(t, drainWake(p.catalogWake), "repeated wakes must coalesce to a single queued token")

	// A drained channel accepts a fresh wake again.
	p.wakeCatalog()
	assert.True(t, drainWake(p.catalogWake), "a drained catalogWake accepts the next wake")
}

// TestRefreshOnceNewGraphWakesGenPoll proves a newly-registered graph triggers a
// bulk gen-poll. Without it the fresh collector has no genSnapshot entry, so its
// discover finds ok==false and falls through to a real PipelineScan on every tick
// until it drains — the per-tick scan the two-phase poll exists to remove.
func TestRefreshOnceNewGraphWakesGenPoll(t *testing.T) {
	fake := newFakeWireClient()
	p := New(Config{}, fake, nil, nil)
	// repoA is WANTED because an interaction admitted it — the working set is the
	// pass's only source of wanted graphs, so seeding the backend's catalog would
	// register nothing.
	ws := workingset.New()
	require.True(t, ws.Admit(kgtypes.GraphCode, "repoA", "collect"))
	p.AttachWorkingSet(ws)
	ctx := context.Background()

	require.False(t, drainWake(p.genPollWake), "nothing queued before the first pass")

	// First pass registers repoA — a NEW graph.
	p.refreshOnce(ctx)
	require.Contains(t, registeredKeys(p), graphKey{GraphType: kgtypes.GraphCode, GraphName: "repoA"})
	assert.True(t, drainWake(p.genPollWake), "registering a NEW graph must wake the gen-poll loop")

	// Second pass over the same working set registers nothing — no wake.
	p.refreshOnce(ctx)
	assert.False(t, drainWake(p.genPollWake), "an unchanged working set must not wake the gen-poll loop")

	require.NoError(t, p.Stop(ctx))
}

// TestGenPollNoCollectorsWakesCatalog proves the zero-collector state is not
// absorbing. With no graphs registered a gen poll can learn nothing — there is
// nothing to sample — so it must look at the CATALOG instead, which is the only
// thing that can move the client out of that state. Once the periodic timers are
// gone, a wake that produced neither an RPC nor a catalog look would park a fresh
// install forever.
func TestGenPollNoCollectorsWakesCatalog(t *testing.T) {
	fake := newFakeWireClient()
	p := genPollTestPipeline(t, fake)
	ctx := context.Background()

	require.Empty(t, registeredKeys(p), "no graphs registered")
	require.False(t, drainWake(p.catalogWake), "nothing queued before the wake")

	// A collect (or any bulk write) wakes the gen-poll loop, which drains the
	// signal and runs one poll pass.
	p.WakeAll()
	require.True(t, drainWake(p.genPollWake), "WakeAll signals the gen-poll loop")
	_, throttled := p.genPollOnce(ctx)
	require.False(t, throttled)

	assert.Zero(t, fake.calls["pipeline_gen_poll"], "a poll with zero collectors must issue ZERO PipelineGenPoll RPCs")
	assert.True(t, drainWake(p.catalogWake), "it must instead wake the catalog loop to enumerate")
	assert.False(t, drainWake(p.catalogWake), "exactly one coalesced catalog enumeration")
}
