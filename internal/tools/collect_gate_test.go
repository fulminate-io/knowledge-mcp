// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// collect_gate_test.go covers the two ways the gap-scan gate could get STUCK UP,
// which is the failure mode that matters: a gate nobody lowers stops enriching a
// graph forever, silently. Both tests establish the gate is genuinely RAISED first
// — without that known-positive step, a CollectInFlightForGraph that always
// returned false would satisfy every assertion below.

const gateTestGraph = "gate-repo"

// TestCollectGate_ReleasesAfterFailedCollect pins the gate to the SAME lifetime as
// the in-flight registry entry, so every path that ends a run also lowers the gate.
// A gate on a lifetime that only covered SUCCESS would strand the pipeline on
// exactly the graph whose collect failed — the graph with the freshest gaps.
func TestCollectGate_ReleasesAfterFailedCollect(t *testing.T) {
	cases := []struct {
		name string
		work func() error
	}{
		{"returned error", func() error { return errors.New("collect code: boom") }},
		{"recovered panic", func() error { panic("kaboom") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt := NewCollectRuntime()
			release := make(chan struct{})
			h, started, _ := rt.Start("code\x00/x", "code /x", gateTestGraph, func() (string, error) {
				<-release
				return "", tc.work()
			})
			require.True(t, started)

			// Known-positive control: the gate is UP while the collect runs.
			require.True(t, rt.CollectInFlightForGraph(kgtypes.GraphCode, gateTestGraph),
				"the gate must be raised while a collect into this graph is in flight")

			close(release)
			<-h.Done()
			require.Error(t, h.Err(), "this case is only meaningful for a run that FAILED")

			require.False(t, rt.CollectInFlightForGraph(kgtypes.GraphCode, gateTestGraph),
				"a failed collect must lower the gate — otherwise the pipeline is stranded on the graph that needs it most")
		})
	}
}

// TestCollectGate_StaleEntryDoesNotGateForever covers the one ending no release
// path can reach: a collect that never ends at all. A detached collect has no
// deadline of its own, so without the max-hold bound a run blocked on an
// unreachable server would hold its graph's gate for the daemon's whole lifetime.
func TestCollectGate_StaleEntryDoesNotGateForever(t *testing.T) {
	rt := NewCollectRuntime()

	// Injected clock, so the bound is exercised by advancing time rather than by
	// sleeping through it.
	var nowNanos atomic.Int64
	nowNanos.Store(time.Now().UnixNano())
	rt.clock = func() time.Time { return time.Unix(0, nowNanos.Load()) }

	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	_, started, _ := rt.Start("code\x00/x", "code /x", gateTestGraph, func() (string, error) {
		<-block // never returns during the test — the hung-collect shape
		return "", nil
	})
	require.True(t, started)

	// Known-positive control: a fresh entry DOES gate.
	require.True(t, rt.CollectInFlightForGraph(kgtypes.GraphCode, gateTestGraph),
		"a fresh in-flight entry must gate")

	nowNanos.Add(int64(collectGateMaxHold + time.Minute))

	require.False(t, rt.CollectInFlightForGraph(kgtypes.GraphCode, gateTestGraph),
		"an entry older than the max-hold bound must stop gating, so a hung collect cannot starve the pipeline")
}

// TestCollectGate_IgnoresOtherGraphsAndTypes keeps the gate NARROW. A gate that
// matched too widely would hold the scan off graphs no collect is touching, which
// looks identical to a correctly-working gate from the outside.
func TestCollectGate_IgnoresOtherGraphsAndTypes(t *testing.T) {
	rt := NewCollectRuntime()
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	_, started, _ := rt.Start("code\x00/x", "code /x", gateTestGraph, func() (string, error) {
		<-release
		return "", nil
	})
	require.True(t, started)

	require.True(t, rt.CollectInFlightForGraph(kgtypes.GraphCode, gateTestGraph))
	require.False(t, rt.CollectInFlightForGraph(kgtypes.GraphCode, "some-other-repo"),
		"a collect into one repo must not gate a different repo")
	require.False(t, rt.CollectInFlightForGraph(kgtypes.GraphKnowledge, gateTestGraph),
		"the gate is code-graph only")
	require.False(t, rt.CollectInFlightForGraph(kgtypes.GraphCode, ""),
		"an empty name (every non-code collector records one) must never gate")
}
