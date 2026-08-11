// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestFreshnessTriggerCoolOff pins the activity hook's wake rules: what counts
// as movement, and how often movement is allowed to wake the pipeline.
//
// Each case is a sequence of (watermark, clock) observations against ONE
// trigger, asserting the wake decision each observation earns. The clock is
// injected so the post-cool-off case costs no wall time.
func TestFreshnessTriggerCoolOff(t *testing.T) {
	t.Parallel()

	type observation struct {
		gen  uint64
		at   time.Duration // offset from the case's base time
		wake bool
	}
	base := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		obs  []observation
	}{
		{
			name: "first movement wakes",
			obs:  []observation{{gen: 7, at: 0, wake: true}},
		},
		{
			name: "an unchanged watermark does not wake",
			obs: []observation{
				{gen: 7, at: 0, wake: true},
				// Well past the cool-off: it is the lack of MOVEMENT, not the
				// window, that must suppress this one.
				{gen: 7, at: 10 * time.Minute, wake: false},
			},
		},
		{
			name: "a backward move counts as movement",
			obs: []observation{
				{gen: 7, at: 0, wake: true},
				// The served value is a per-replica sample and may move
				// backward; any change is movement.
				{gen: 4, at: 10 * time.Minute, wake: true},
			},
		},
		{
			name: "two moves inside one cool-off yield one wake",
			obs: []observation{
				{gen: 7, at: 0, wake: true},
				{gen: 8, at: 1 * time.Second, wake: false},
				{gen: 9, at: 2 * time.Second, wake: false},
			},
		},
		{
			name: "a suppressed move fires after the window",
			obs: []observation{
				{gen: 7, at: 0, wake: true},
				{gen: 8, at: 1 * time.Second, wake: false},
				// No further movement — the wake is owed for the move that was
				// suppressed, which is what the pending flag carries.
				{gen: 8, at: freshnessWakeCoolOff, wake: true},
				{gen: 8, at: 2 * freshnessWakeCoolOff, wake: false},
			},
		},
		{
			name: "zero never wakes and never becomes the last-seen value",
			obs: []observation{
				{gen: 0, at: 0, wake: false},
				{gen: 0, at: 10 * time.Minute, wake: false},
				// A real reading after the non-values still reads as first
				// movement: the zeros recorded nothing.
				{gen: 7, at: 20 * time.Minute, wake: true},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var trigger freshnessTrigger
			for i, o := range tc.obs {
				got := trigger.evaluate(o.gen, base.Add(o.at))
				assert.Equal(t, o.wake, got,
					"observation %d (gen=%d, +%s)", i, o.gen, o.at)
			}
		})
	}
}

// TestFreshnessTriggerResetClearsWindow proves the login-flip reset makes the
// next observation a first sighting: the pre-flip watermark is forgotten (so a
// repeat of it counts as movement) and the cool-off window does not suppress it.
func TestFreshnessTriggerResetClearsWindow(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	var trigger freshnessTrigger
	assert.True(t, trigger.evaluate(7, base), "first movement wakes")
	assert.False(t, trigger.evaluate(7, base.Add(time.Second)), "unchanged inside the window")

	trigger.reset()

	assert.True(t, trigger.evaluate(7, base.Add(2*time.Second)),
		"after a flip the same value is a different account's counter — first sighting, and no window to serve out")
}
