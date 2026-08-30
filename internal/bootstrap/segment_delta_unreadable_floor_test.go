// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// segment_delta_unreadable_floor_test.go — a delta pass that cannot read its own
// retention floor declines, drives ONE recovery rebuild, and keeps declining
// afterwards without rebuilding again.
//
// THE FIXTURE IS THE UNWRITABLE PATH, NOT A CORRUPT FILE, and the choice is the
// point. Against a merely corrupt record the recovery rebuild lands, rewrites the
// record, and the next pass proceeds — nothing to bound. Against an UNWRITABLE path
// the rebuild runs and publishes, its state write fails with a warning and a nil
// error, and the record is STILL unreadable: the rebuild reports success, the heal
// breaker never latches, and without a per-process claim the next pass would drive
// another full-corpus rebuild. Forever. That loop is what these four subtests bound.

// breakRebuildStateRecord makes one graph's rebuild-state record permanently
// unreadable AND unwritable, by replacing the record file with a DIRECTORY at the
// same path. A read of it fails with something other than not-exist — which is what
// the retention helper treats as "no position I can vouch for" — and the driver's
// post-publish write fails too, so the state never repairs itself.
func breakRebuildStateRecord(t *testing.T, c *client, dir string, repo string) {
	t.Helper()
	// Write the record once so the manager creates its directory layout, then find
	// the file it wrote rather than reconstructing a path from unexported naming.
	require.NoError(t, c.segmentMgr.SaveRebuildState(kgtypes.GraphCode, repo, 1_000_000_000, nil))

	var found string
	require.NoError(t, filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && d.Name() == "state.json" && strings.Contains(path, repo) {
			found = path
		}
		return nil
	}))
	require.NotEmpty(t, found, "fixture control: the manager must have written a rebuild-state record to break")

	require.NoError(t, os.Remove(found))
	require.NoError(t, os.Mkdir(found, 0o750))

	// FIXTURE CONTROL: the record must now read as an ERROR rather than as absent.
	// An absent record is a zero position with a nil error and imposes no floor at
	// all, so a fixture that merely deleted the file would exercise nothing.
	_, _, err := c.segmentMgr.LoadRebuildState(kgtypes.GraphCode, repo)
	require.Error(t, err, "fixture control: the broken record must fail to READ, not merely be missing")
}

// TestSegmentDeltaUnreadableFloorDrivesOneRecoveryRebuild is the E5 bootstrap gate.
func TestSegmentDeltaUnreadableFloorDrivesOneRecoveryRebuild(t *testing.T) {
	const repo = "floorrecoveryrepo"
	const horizon = int64(2_000_000_000)

	dir := t.TempDir()
	c, eng := buildSeedRebuildClientAt(t, dir, repo)
	g := segmentGraphRef{gt: kgtypes.GraphCode, name: repo}

	// The delta arm needs a resolvable horizon, and merge.json is the source that
	// resolves it — which is why the arm proceeds far enough to read the floor at all.
	require.NoError(t, c.segmentMgr.SaveMergeWatermark(kgtypes.GraphCode, repo, horizon))
	breakRebuildStateRecord(t, c, dir, repo)

	// A corpus above the horizon, so a rebuild that DOES run is visible as scan
	// traffic rather than as an empty page indistinguishable from no scan at all.
	corpus := make([]stampedNode, 0, 3)
	for i := range 3 {
		corpus = append(corpus, stampedNode{
			id:        repo + "-node-" + string(rune('a'+i)),
			stampedAt: horizon + int64(i) + 1,
			vector:    seedFixtureVector(byte('a' + i)),
		})
	}
	eng.mu.Lock()
	eng.corpus, eng.servedHorizon = corpus, horizon+100
	eng.mu.Unlock()

	// FIXTURE CONTROL: nothing has scanned yet, so every request counted below is
	// attributable to a pass this test drove.
	before, _ := eng.recorded()
	require.Empty(t, before, "fixture control: the fake must start with no recorded scans")

	ctx := context.Background()
	first := c.consumeSegmentDelta(ctx, g, nil)
	afterFirst, unexpected := eng.recorded()
	require.Empty(t, unexpected, "no assertion here may rest on the arm having read a different axis")

	t.Run("recovery_rebuild_is_attempted_once", func(t *testing.T) {
		require.NotEmpty(t, afterFirst,
			"the declined pass must drive a recovery rebuild, and a rebuild scans — zero requests means "+
				"the decline was classified but nothing was done about it")
		for _, r := range afterFirst {
			require.Zero(t, r.AfterStampedAtNanos,
				"and every request it issued belongs to a RESET rebuild, which reports no position — a "+
					"non-zero bound here would mean the declining delta pass scanned after all")
		}
	})

	t.Run("further_passes_drive_no_further_rebuild", func(t *testing.T) {
		// THE CATCHER FOR THE UNWRITABLE-PATH LOOP. The record is still broken, the
		// rebuild reported success, and the breaker never latched — so an arm with no
		// per-process claim rebuilds the whole corpus again here, and again forever.
		c.consumeSegmentDelta(ctx, g, nil)
		c.consumeSegmentDelta(ctx, g, nil)
		afterAll, _ := eng.recorded()
		require.Len(t, afterAll, len(afterFirst),
			"two further passes must add NO scan traffic: the recovery rebuild is claimed once per graph "+
				"per process, or an unwritable state path drives a full-corpus rebuild every pass forever")
	})

	t.Run("further_passes_still_decline", func(t *testing.T) {
		// THE DISCRIMINATOR. Without it the subtest above is satisfied by an arm that
		// stopped classifying the error at all, or that silently began proceeding with
		// the zero floor — both produce zero further rebuilds, and both ARE the defect.
		later := c.consumeSegmentDelta(ctx, g, nil)
		require.False(t, later.Pull,
			"a later pass must still DECLINE and return the no-commit result — an arm that proceeded with "+
				"the zero floor would report a pull and would be losing this window's deletions")
		require.Zero(t, later.Merged)
	})

	t.Run("no_horizon_is_committed", func(t *testing.T) {
		// A declined pass must not advance past a window it never read. Both halves are
		// asserted: the in-memory carry the commit would write, and the durable record.
		require.False(t, first.Pull, "the first pass declined, so it carries nothing to commit")
		c.commitMergeWatermark(g, first)

		c.deltaHorizonMu.Lock()
		carried, present := c.deltaHorizon[g]
		c.deltaHorizonMu.Unlock()
		require.False(t, present, "a declined pass must leave no in-memory horizon: got %d", carried)

		durable, err := c.segmentMgr.LoadMergeWatermark(kgtypes.GraphCode, repo)
		require.NoError(t, err)
		require.Equal(t, horizon, durable,
			"and the durable merge horizon must still be the seeded one — advancing past an unread window "+
				"is what makes its deletions permanently unlearnable")
	})
}
