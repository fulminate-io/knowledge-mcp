// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCollectRuntime_StartCoalesces proves the per-target single-flight guard: a
// second Start for the same key while the first work blocks returns started=false
// with a positive in-flight elapsed and spawns nothing (the work runs exactly once).
func TestCollectRuntime_StartCoalesces(t *testing.T) {
	rt := NewCollectRuntime()

	var ran atomic.Int32
	release := make(chan struct{})
	h1, started1, _ := rt.Start("k", "code /repo", func() error {
		ran.Add(1)
		<-release
		return nil
	})
	require.True(t, started1)
	require.NotNil(t, h1)

	// A small real-time gap so the coalesce path reports a strictly positive elapsed.
	time.Sleep(2 * time.Millisecond)

	h2, started2, elapsed := rt.Start("k", "code /repo", func() error {
		ran.Add(1)
		return nil
	})
	assert.False(t, started2, "second Start for an in-flight key must coalesce")
	assert.Nil(t, h2, "a coalesced Start returns no handle")
	assert.Greater(t, elapsed, time.Duration(0), "coalesce reports the in-flight elapsed")

	close(release)
	<-h1.Done()
	assert.Equal(t, int32(1), ran.Load(), "the work ran exactly once — the second Start spawned nothing")
}

// TestCollectRuntime_SnapshotStates proves Snapshot reflects running, then the
// completed and failed last-outcomes per target.
func TestCollectRuntime_SnapshotStates(t *testing.T) {
	rt := NewCollectRuntime()

	release := make(chan struct{})
	h1, started, _ := rt.Start("k1", "code /a", func() error {
		<-release
		return nil
	})
	require.True(t, started)

	snap := rt.Snapshot()
	require.Len(t, snap, 1)
	assert.Equal(t, "running", snap[0].State)
	assert.Equal(t, "code /a", snap[0].Label)

	close(release)
	<-h1.Done()
	snap = rt.Snapshot()
	require.Len(t, snap, 1)
	assert.Equal(t, "completed", snap[0].State)

	sentinel := errors.New("boom")
	h2, started2, _ := rt.Start("k2", "code /b", func() error { return sentinel })
	require.True(t, started2)
	<-h2.Done()

	snap = rt.Snapshot()
	require.Len(t, snap, 2, "both last-outcomes are retained")
	var k2 CollectRunStatus
	for _, s := range snap {
		if s.Target == "k2" {
			k2 = s
		}
	}
	assert.Equal(t, "failed", k2.State)
	assert.Equal(t, "boom", k2.Err)
}

// TestCollectRuntime_StopDrains proves Stop cancels the runtime baseCtx (which the
// no-arg work closure captures directly, since Start injects no ctx) and drains the
// in-flight run via inFlight.Wait.
func TestCollectRuntime_StopDrains(t *testing.T) {
	rt := NewCollectRuntime()

	started := make(chan struct{})
	h, ok, _ := rt.Start("k", "code /a", func() error {
		close(started)
		// The closure captures the runtime's baseCtx directly — Stop's baseCancel
		// unblocks this.
		<-rt.BaseContext().Done()
		return rt.BaseContext().Err()
	})
	require.True(t, ok)
	<-started

	drained := make(chan struct{})
	go func() {
		rt.Stop(2 * time.Second)
		close(drained)
	}()
	select {
	case <-drained:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not drain the in-flight run within its deadline")
	}
	<-h.Done() // the run fully unwound
}

// TestCollectRuntime_ErrAfterDone proves the store-before-close ordering: after
// <-h.Done(), the lock-free h.Err() observes the run's terminal error.
func TestCollectRuntime_ErrAfterDone(t *testing.T) {
	rt := NewCollectRuntime()
	sentinel := errors.New("sentinel-err")
	h, ok, _ := rt.Start("k", "code /a", func() error { return sentinel })
	require.True(t, ok)
	<-h.Done()
	assert.ErrorIs(t, h.Err(), sentinel)
}

// TestCollectRuntime_DetachedFailureLoudLog proves ONE loud slog.Error
// ("collect: detached run failed") fires on BOTH the normal-error and the
// recovered-panic failure paths (In-scope item 5 / reviewer T2-3).
func TestCollectRuntime_DetachedFailureLoudLog(t *testing.T) {
	t.Run("normal error", func(t *testing.T) {
		rt := NewCollectRuntime()
		out := captureSlog(func() {
			h, ok, _ := rt.Start("k", "code /a", func() error { return errors.New("boom") })
			require.True(t, ok)
			<-h.Done()
		})
		assert.Contains(t, out, "collect: detached run failed")
	})

	t.Run("recovered panic", func(t *testing.T) {
		rt := NewCollectRuntime()
		out := captureSlog(func() {
			h, ok, _ := rt.Start("k", "code /a", func() error { panic("kaboom") })
			require.True(t, ok)
			<-h.Done()
		})
		assert.Contains(t, out, "collect: detached run failed")
	})
}
