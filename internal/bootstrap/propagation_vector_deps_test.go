// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// TestPropagationVectorDeps_LazySegmentManager pins BOTH halves of the adapters'
// contract, and MUST be run under -race (its criterion command carries the flag).
//
// The halves are independent and each fails differently:
//
//   - LAZINESS. wirePropagationRuntime constructs and STARTS the loop before
//     ensureSegmentManager assigns c.segmentMgr, so an adapter that captured the
//     manager at construction would be permanently nil — every pass silently
//     falling back to the full server drain while every log line looked healthy.
//     Sub-test 2 builds the adapters while the field is nil and requires the SAME
//     instances to resolve after wiring.
//   - THE MEMORY MODEL. Reading that field from the propagation goroutine is only
//     legal through the pipelineReady publish edge. Sub-test 3 runs a resolve
//     CONCURRENTLY with the wiring write, so an adapter that reads the field
//     without the PipelineReady() gate is reported as a data race rather than
//     passing by luck.
func TestPropagationVectorDeps_LazySegmentManager(t *testing.T) {
	ctx := context.Background()

	newWiredClient := func(t *testing.T) *client {
		t.Helper()
		// NewManager makes no RPC at construction, so a logged-out router pointed at
		// an unreachable URL is enough to satisfy ensureSegmentManager's only guard.
		authState := auth.NewAuthState(newFakeAuthStore(), time.Minute)
		local := graphclient.NewGraphClientForURL("http://local.invalid")
		t.Cleanup(local.CloseIdleConnections)
		router := graphclient.NewRouter(local, "http://local.invalid", staticTokenSource{tok: "tok"}, authState)
		return &client{local: local, router: router, authState: authState}
	}

	t.Run("unwired: both adapters report unavailable", func(t *testing.T) {
		c := newWiredClient(t)
		require.Nil(t, c.segmentMgr, "precondition: the manager is not wired yet")
		require.False(t, c.PipelineReady(), "precondition: the pipeline is not published yet")

		vec, ok, err := (residentVectorAdapter{c: c}).VectorByID(ctx, "any-node")
		assert.Nil(t, vec)
		assert.False(t, ok)
		require.Error(t, err, "an unwired resident resolver must report unavailable, never a silent empty vector")

		trustworthy, reason, err := (coverageGateAdapter{c: c}).HNSWCoverageTrustworthy(ctx)
		require.NoError(t, err, "not-yet-wired is a decline, not an error")
		assert.False(t, trustworthy, "an unwired gate declines so the caller takes the drain")
		assert.NotEmpty(t, reason, "a decline always carries a reason so the fallback is not silent")
	})

	t.Run("the SAME adapter instances resolve after wiring", func(t *testing.T) {
		c := newWiredClient(t)
		// Build the adapters BEFORE the manager exists — exactly what
		// wirePropagationRuntime does. A capturing adapter fails this sub-test.
		resident := residentVectorAdapter{c: c}
		gate := coverageGateAdapter{c: c}
		_, _, err := resident.VectorByID(ctx, "any-node")
		require.Error(t, err, "precondition: unavailable before wiring")

		c.ensureSegmentManager(t.TempDir(), 0)
		c.markPipelineReady()
		require.NotNil(t, c.segmentMgr, "the production wiring path assigned the manager")
		// Only Manager.Close stops the per-engine merger goroutines the Manager spawns.
		t.Cleanup(c.segmentMgr.Close)

		// The resident resolver now REACHES the manager. Against an empty local cache
		// it resolves nothing, but the distinction that matters is the one the loop
		// makes: not-wired is an error, while loaded-fine-but-no-such-id is the
		// ordinary vectorless case (ok=false, err=nil).
		vec, ok, err := resident.VectorByID(ctx, "no-such-node")
		require.NoError(t, err, "a wired resolver over an empty cache is vectorless, NOT unavailable")
		assert.False(t, ok, "no such id")
		assert.Nil(t, vec)

		// The gate now reaches the real probe. Its verdict over an empty pool is
		// whatever the probe says; what this asserts is that it stopped answering
		// "not wired yet" — i.e. the adapter observed the manager it was built without.
		_, reason, _ := gate.HNSWCoverageTrustworthy(ctx)
		assert.NotEqual(t, "segment manager not wired yet", reason,
			"the gate must observe the manager assigned after it was constructed")
	})

	t.Run("resolve concurrent with the wiring write is race-free", func(t *testing.T) {
		c := newWiredClient(t)
		resident := residentVectorAdapter{c: c}
		gate := coverageGateAdapter{c: c}

		var wg sync.WaitGroup
		wg.Add(2)
		// Reader goroutine: stands in for the propagation loop, which is already
		// running by the time the wiring goroutine reaches ensureSegmentManager.
		go func() {
			defer wg.Done()
			for range 200 {
				_, _, _ = resident.VectorByID(ctx, "any-node")
				_, _, _ = gate.HNSWCoverageTrustworthy(ctx)
			}
		}()
		// Writer goroutine: the wiring order the daemon actually performs — assign
		// the handle, THEN publish it.
		go func() {
			defer wg.Done()
			c.ensureSegmentManager(t.TempDir(), 0)
			c.markPipelineReady()
		}()
		wg.Wait()
		// Only Manager.Close stops the per-engine merger goroutines the Manager spawns.
		// Registered after Wait so the write to c.segmentMgr is already ordered.
		t.Cleanup(c.segmentMgr.Close)

		// Under -race, an adapter reading c.segmentMgr without the PipelineReady()
		// gate fails the run above. Reaching here means the reads were ordered by the
		// publish edge.
		require.True(t, c.PipelineReady())
	})
}
