// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// TestReconcilePassUntombstonesRecreatedWrite is the WIRING gate for the untombstone
// arm. Like the drain it sits beside, that arm has exactly one production caller — the
// reconcile pass — so if the call is missing the record keeps its tombstone, the drain
// filters the re-created document out, and the backlog entry is consumed and gone.
//
// The observable is the PERSISTED RECORD read back through the real Manager, so a
// comment naming the call cannot satisfy it.
func TestReconcilePassUntombstonesRecreatedWrite(t *testing.T) {
	ctx := opCtx()
	const (
		repo     = "untombstoneRepo"
		corpusN  = 128 // clears the resident backstop floor
		embedded = 100 // resident 128 >= 0.5*100, so the graph is HEALTHY
	)
	gt := kgtypes.GraphCode

	c, _, _ := buildReconcileClientWithSeg(t, embedded, repo)

	docs := fastloadVecDocs(repo, corpusN)
	require.NoError(t, c.segmentMgr.AddAndMarkDirty(ctx, gt, repo, docs))
	require.NoError(t, c.segmentMgr.ReEmitDirtyBuckets(ctx, gt, repo))

	victim, bystander := docs[0].ID, docs[1].ID

	// The delete lands the way the delta consumer lands one: the record learns it, the
	// engines are seeded from the record, and the ids are stamped as deleted NOW.
	const seededWatermark = int64(123456789)
	require.NoError(t, c.segmentMgr.SaveRebuildState(gt, repo, seededWatermark,
		[]searchengine.ExternalID{victim, bystander}))
	c.segmentMgr.NoteDeletedIDs(gt, repo, []searchengine.ExternalID{victim, bystander})
	c.segmentMgr.SetGraphTombstones(gt, repo, []searchengine.ExternalID{victim, bystander})

	// The re-creation is issued AFTER that stamp, so its backlog sequence exceeds it and
	// the reporter can see it. A write queued BEFORE would report nothing and this test
	// would pass for the wrong reason.
	require.NoError(t, c.segmentMgr.AddAndMarkDirty(ctx, gt, repo,
		fastloadVecDocs(repo, corpusN)[:1]))

	c.reconcileSegmentCoverage(ctx)

	watermark, retained, err := c.segmentMgr.LoadRebuildState(gt, repo)
	require.NoError(t, err)
	require.Equal(t, seededWatermark, watermark,
		"the untombstone must never advance the rebuild's durability watermark")

	t.Run("recreated_id_leaves_the_record", func(t *testing.T) {
		require.NotContains(t, retained, victim,
			"the id a write re-created must be cleared from the record before the drain reads it")
	})

	t.Run("tombstoned_id_with_no_write_stays", func(t *testing.T) {
		// THE KNOWN-NEGATIVE CONTROL: a wiring that cleared the whole record would pass
		// the leg above and fail here.
		require.Contains(t, retained, bystander,
			"a tombstoned id with no re-creating write must stay in the record")
	})
}
