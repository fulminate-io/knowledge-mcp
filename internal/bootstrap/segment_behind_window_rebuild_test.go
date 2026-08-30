// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// segment_behind_window_rebuild_test.go gates the behind-window recovery: a typed
// refusal routes to a FULL rebuild, the refused window's horizon is never
// committed, and a plain scan error does NOT trigger a rebuild.
//
// THE MISCLASSIFICATION IN EACH DIRECTION IS EXPENSIVE, WHICH IS WHY BOTH ARE
// ASSERTED. Treating a refusal as transient re-reads the same window forever — a
// lane firing endlessly on one cause, whose consequence is a deleted node that
// stays searchable on this machine with nothing left able to name it. Treating a
// transient blip as a refusal puts every network hiccup onto the full-rebuild path,
// which is the most expensive possible response to a retryable error.
//
// NO ASSERTION HERE REFERENCES A SERVER-SIDE SENTINEL or the received error's
// chain. Only the CODE and the MESSAGE cross the wire, so connect.CodeOf is the
// only discriminator that exists across the process boundary — and this harness
// round-trips the refusal through a real connect server, so what arrives is the
// rebuilt wire error production sees.

// TestBehindWindowRefusalRebuildsAndAdoptsRebuildHorizon pins all four claims.
func TestBehindWindowRefusalRebuildsAndAdoptsRebuildHorizon(t *testing.T) {
	ctx := opCtx()
	const (
		repo     = "behindWindowRepo"
		corpusN  = 128
		embedded = 100
		// The position this client holds. It must survive the refused pass intact.
		seededHorizon = int64(1_600_000_000_000_000_000)
		// What the server would have served the window up to, had it not refused.
		// Nothing may ever commit this.
		refusedHorizon = int64(1_900_000_000_000_000_000)
	)

	c, eng := buildReconcileClientWith(t, embedded, repo)
	require.NoError(t, c.segmentMgr.SaveMergeWatermark(kgtypes.GraphCode, repo, seededHorizon))
	eng.setServedHorizon(refusedHorizon)

	docs := fastloadVecDocs(repo, corpusN)
	require.NoError(t, c.segmentMgr.AddAndMarkDirty(ctx, kgtypes.GraphCode, repo, docs))
	require.NoError(t, c.segmentMgr.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, repo))

	// (1) A PLAIN SCAN ERROR MUST NOT REBUILD — the known-negative, run FIRST so a
	// rebuild counted later cannot be attributed to it.
	eng.setScanErr(errors.New("transient: connection reset"))
	scansBeforeTransient := eng.scanCallCount(repo)
	c.reconcileSegmentCoverage(ctx)
	require.Equal(t, scansBeforeTransient, eng.scanCallCount(repo),
		"a PLAIN scan error must NOT trigger a from-scratch rebuild — routing every transient blip to a rebuild is the most expensive possible misclassification")

	// The transient path also leaves the position exactly where it was.
	afterTransient, err := c.segmentMgr.LoadMergeWatermark(kgtypes.GraphCode, repo)
	require.NoError(t, err)
	require.Equal(t, seededHorizon, afterTransient,
		"a transient failure must leave the position untouched so the same window is re-read next pass")

	// (2) THE REFUSAL ROUTES TO A REBUILD.
	eng.setScanErr(connect.NewError(connect.CodeOutOfRange,
		errors.New("erasure journal trimmed past the caller's position")))
	scansBeforeRefusal := eng.scanCallCount(repo)

	c.reconcileSegmentCoverage(ctx)

	require.Greater(t, eng.scanCallCount(repo), scansBeforeRefusal,
		"a CodeOutOfRange refusal must trigger the from-scratch rebuild — re-reading the refused window forever is a lane that fires endlessly on one cause, and the reaped rows are gone from the journal so no wider window can recover them")

	// (3) THE REFUSED WINDOW'S HORIZON IS NEVER COMMITTED. This is the assertion
	// that distinguishes "recovered" from "skipped ahead": adopting the horizon of a
	// window the server refused to serve would move this client PAST deletions it
	// never received.
	afterRefusal, err := c.segmentMgr.LoadMergeWatermark(kgtypes.GraphCode, repo)
	require.NoError(t, err)
	require.NotEqual(t, refusedHorizon, afterRefusal,
		"the REFUSED window's horizon must never become this client's position — committing it would skip past deletions that were never delivered")
}
