// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCheckLoginFlipRebindsAlone drives CheckLoginFlip DIRECTLY — no refreshOnce,
// no wire loop — and pins everything a flip must rebind: the per-collector
// registry, the CENTRAL two-phase gen caches, and both wake channels.
//
// The central caches are the load-bearing part. lastPokedGen holds the OLD
// backend's high-water and genPollOnce pokes a collector only when a returned gen
// exceeds it, so a flip to a backend whose generations are LOWER leaves every
// (graph,axis) pair permanently un-poked unless the flip clears them.
func TestCheckLoginFlipRebindsAlone(t *testing.T) {
	fb := newFlippableBackend()
	p := New(Config{}, fb, nil, nil)
	ctx := context.Background()

	// Seed the state a live pipeline would be carrying: one registered collector
	// plus central gen caches populated by an earlier poll against the OLD backend.
	registerStubCollector(p, "repoA")
	key := graphKey{GraphType: genPollGT, GraphName: "repoA"}
	p.genMu.Lock()
	p.genSnapshot[key] = axisGens{summary: 9, embed: 9}
	p.lastPokedGen[key] = axisGens{summary: 9, embed: 9}
	p.genMu.Unlock()

	// First observation only SEEDS the login state — not a flip.
	assert.False(t, p.CheckLoginFlip(ctx), "the first observation seeds the state, it is not a flip")
	// An unchanged state is not a flip either.
	assert.False(t, p.CheckLoginFlip(ctx), "an unchanged login state is not a flip")

	// Known-positive control for the emptiness assertions below: nothing has been
	// torn down or signaled yet, so a later empty map / queued token can only be
	// the flip's doing.
	assert.Len(t, registeredKeys(p), 1, "the collector must still be registered before the flip")
	assert.Equal(t, 1, genCacheLen(p, &p.genSnapshot), "genSnapshot must be populated before the flip")
	assert.Equal(t, 1, genCacheLen(p, &p.lastPokedGen), "lastPokedGen must be populated before the flip")
	require.False(t, drainWake(p.catalogWake), "no catalog wake queued before the flip")
	require.False(t, drainWake(p.genPollWake), "no gen-poll wake queued before the flip")

	// The actual transition.
	fb.loggedIn.Store(true)
	require.True(t, p.CheckLoginFlip(ctx), "a login-state transition must report a flip")

	assert.Empty(t, registeredKeys(p), "every collector must be torn down on a flip")
	assert.Zero(t, genCacheLen(p, &p.genSnapshot), "genSnapshot must be cleared on a flip")
	assert.Zero(t, genCacheLen(p, &p.lastPokedGen),
		"lastPokedGen must be cleared on a flip — it holds the OLD backend's high-water")
	assert.True(t, drainWake(p.catalogWake), "a flip must wake the catalog loop so every wanted graph re-registers")
	assert.True(t, drainWake(p.genPollWake), "a flip must wake the gen-poll loop to re-sample")
}

// genCacheLen reads the length of one genMu-guarded gen cache under the lock.
func genCacheLen(p *Pipeline, m *map[graphKey]axisGens) int {
	p.genMu.Lock()
	defer p.genMu.Unlock()
	return len(*m)
}
