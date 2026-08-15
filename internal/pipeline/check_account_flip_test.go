// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCheckAccountFlipTearsDownCollectors proves the second half of the backend
// identity is live: changing ONLY the selected account — the login boolean never
// moves, because an account switch is cloud->cloud — tears down every collector,
// clears the central gen caches, and signals both wake channels.
//
// Serving account A's collectors to a session the user has told the client is
// account B is a correctness bug, not a staleness annoyance, which is why this
// case must behave exactly like a login flip.
func TestCheckAccountFlipTearsDownCollectors(t *testing.T) {
	fb := newFlippableBackend()
	fb.loggedIn.Store(true)
	fb.setAccount("acct_01AAA")
	p := New(Config{}, fb, nil, nil)
	ctx := context.Background()

	registerStubCollector(p, "repoA")
	key := graphKey{GraphType: genPollGT, GraphName: "repoA"}
	p.genMu.Lock()
	p.genSnapshot[key] = axisGens{summary: 9, embed: 9}
	p.lastPokedGen[key] = axisGens{summary: 9, embed: 9}
	p.genMu.Unlock()

	// Seed, then confirm an unchanged identity is not a flip.
	assert.False(t, p.CheckLoginFlip(ctx), "the first observation seeds the identity, it is not a flip")
	assert.False(t, p.CheckLoginFlip(ctx), "an unchanged identity is not a flip")

	// Known-positive control: nothing is torn down or signaled yet, so anything
	// empty below can only be the flip's doing.
	assert.Len(t, registeredKeys(p), 1, "the collector must still be registered before the flip")
	assert.Equal(t, 1, genCacheLen(p, &p.genSnapshot), "genSnapshot must be populated before the flip")
	assert.Equal(t, 1, genCacheLen(p, &p.lastPokedGen), "lastPokedGen must be populated before the flip")
	require.False(t, drainWake(p.catalogWake), "no catalog wake queued before the flip")
	require.False(t, drainWake(p.genPollWake), "no gen-poll wake queued before the flip")

	// ONLY the account moves — the login state is untouched.
	fb.setAccount("acct_01BBB")
	require.True(t, fb.LoggedIn(ctx), "the login state must be unchanged, or this proves nothing about the account")
	require.True(t, p.CheckLoginFlip(ctx), "an account switch must report a flip")

	assert.Empty(t, registeredKeys(p), "every collector must be torn down on an account switch")
	assert.Zero(t, genCacheLen(p, &p.genSnapshot), "genSnapshot must be cleared on an account switch")
	assert.Zero(t, genCacheLen(p, &p.lastPokedGen), "lastPokedGen must be cleared on an account switch")
	assert.True(t, drainWake(p.catalogWake), "an account switch must wake the catalog loop")
	assert.True(t, drainWake(p.genPollWake), "an account switch must wake the gen-poll loop")

	// The new identity is now the baseline: the same value is no longer a flip.
	assert.False(t, p.CheckLoginFlip(ctx), "the post-flip identity must be adopted as the new baseline")
}

// TestNoFlipWhenIdentityUnchanged is the negative control for the widened
// identity: with BOTH halves unchanged, handleLoginFlip reports false and tears
// nothing down — including when a selection is set and stays set.
func TestNoFlipWhenIdentityUnchanged(t *testing.T) {
	fb := newFlippableBackend()
	fb.loggedIn.Store(true)
	fb.setAccount("acct_01STABLE")
	p := New(Config{}, fb, nil, nil)
	ctx := context.Background()

	registerStubCollector(p, "repoA")

	require.False(t, p.handleLoginFlip(ctx), "the first observation only seeds")
	for i := range 3 {
		assert.Falsef(t, p.handleLoginFlip(ctx), "check %d: an unchanged identity must not flip", i)
	}
	assert.Len(t, registeredKeys(p), 1, "an unchanged identity must tear nothing down")

	// Known-positive control: the SAME pipeline DOES flip once the account
	// moves, so the false results above are a real distinction rather than a
	// detector that never fires.
	fb.setAccount("acct_01MOVED")
	require.True(t, p.handleLoginFlip(ctx), "control: an account change must still be detected")
	assert.Empty(t, registeredKeys(p), "control: the flip must tear the collector down")
}
