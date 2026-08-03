// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestShutdownDrainsSegmentBacklog closes the non-crash half of the silent-drop
// route. Manager.dirty is in-memory and the periodic drain cadence is
// segmentReconcileInterval, so before this every clean daemon stop discarded up to
// one full interval of queued documents with no record.
//
// It drives the REAL shutdown closure (drainOnShutdown), not drainSegmentBacklog in
// isolation: a drain with no production caller would satisfy a direct-call test
// while shipping nothing on an actual SIGTERM, which is the exact failure this step
// exists to prevent.
func TestShutdownDrainsSegmentBacklog(t *testing.T) {
	const (
		repo    = "shutdownRepo"
		corpusN = 128 // clears the resident backstop floor
		batchN  = 20
	)

	// seedPendingBacklog builds a client whose segment manager holds a published
	// corpus plus an UNDRAINED batch — the state a clean stop finds between ticks —
	// and returns it with the backend's publish count at that moment.
	seedPendingBacklog := func(t *testing.T) (*client, *fakeSegBackend, int) {
		t.Helper()
		ctx := opCtx()
		c, _, backend := buildReconcileClientWithSeg(t, 100, repo)

		require.NoError(t, c.segmentMgr.AddAndMarkDirty(ctx, kgtypes.GraphCode, repo, fastloadVecDocs(repo, corpusN)))
		require.NoError(t, c.segmentMgr.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, repo))

		// Queue work and deliberately leave it queued.
		require.NoError(t, c.segmentMgr.AddAndMarkDirty(ctx, kgtypes.GraphCode, repo, fastloadVecDocs("shutdownBatch", batchN)))
		return c, backend, backend.publishCount()
	}

	t.Run("drains_pending_backlog", func(t *testing.T) {
		c, backend, before := seedPendingBacklog(t)
		// The drain is gated on the pipeline readiness flag — the same flag that
		// tells the shutdown closure a segment manager was ever wired.
		c.markPipelineReady()

		c.drainOnShutdown()

		require.Greater(t, backend.publishCount(), before,
			"a clean shutdown must ship the queued backlog; no publish means the drain is "+
				"either absent from drainOnShutdown or placed after the pipeline Stop that takes the producer away")
	})

	// control_undrained_stays_pending is the known-negative. The positive above
	// asserts a publish count went UP, which a fixture whose batch had already
	// shipped during seeding would satisfy without the drain doing anything. This
	// runs the identical fixture with the shutdown closure never invoked and
	// requires the count to sit exactly where seeding left it.
	t.Run("control_undrained_stays_pending", func(t *testing.T) {
		_, backend, before := seedPendingBacklog(t)

		// No drainOnShutdown call — the pre-fix behavior.

		require.Equal(t, before, backend.publishCount(),
			"without the shutdown drain the seeded batch must still be PENDING; if this rises on its "+
				"own, the fixture is not holding a real backlog and the positive case proves nothing")
	})
}
