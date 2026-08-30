// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestQuiescentBothAxes_StaleAcrossCollectDoesNotFire drives the REAL embed loop
// (runEmbedLoop → runLoop → discover) through the exact sequence that makes a
// per-axis BOOLEAN wrong, and asserts the predicate declines.
//
// THE SEQUENCE, which is the concrete false-unhealthy this keying exists to stop:
//
//  1. Both axes drain at collect epoch N. Both stamp N+1.
//  2. A COLLECT COMPLETES, landing new summarizable nodes. The epoch moves to N+1.
//  3. The EMBED axis reaches its drain edge FIRST — legitimately, because
//     file/package embed eligibility is ifSummaryPresent, so nothing is embeddable
//     until the new summaries land. It re-stamps at the new epoch.
//  4. The SUMMARY axis has NOT iterated. A drained axis sits on the longest idle
//     sleep, and a collect can complete well inside that window, so this is the
//     common case rather than a contrived one.
//
// A plain bool has summary=true (set in step 1, never cleared because that loop has
// not run) and embed=true, so it FIRES — and the verdict then reports a deficit of
// however many nodes the collect added and drives a from-scratch rebuild. With
// epoch-keying the summary axis's stamp is N+1 against a want of N+2, so it does
// not match and the predicate declines.
//
// IT GOES GREEN ONLY WITH EPOCH-KEYING. A bool merely cleared on runLoop's continue
// paths still fails here, because the summary loop never reaches ANY of those paths
// in step 4 — it does not run at all.
func TestQuiescentBothAxes_StaleAcrossCollectDoesNotFire(t *testing.T) {
	cfg := Config{
		EmbedBatchSize:   5,
		EmbedWorkers:     2,
		EmbedChannelSize: 100,
		Tick:             time.Millisecond,
		IdleTickMax:      time.Millisecond,
	}

	// The embed axis has NO work at any point — the already-embedded shape the
	// collect-armed heal exists for, and the shape that lets the embed loop reach
	// its drain edge immediately while the summary axis still has a backlog.
	fake := newStatefulEmbedGapFake(nil, 0)

	// The collect epoch this test advances by hand, standing in for the collect
	// runtime's completion counter.
	var epoch atomic.Uint64
	embedCh := make(chan EmbedWork, cfg.EmbedChannelSize)
	c := newCollector(
		kgtypes.GraphCode, "agent", cfg,
		nil, embedCh, &metricsState{}, fake,
		cfg.Tick, cfg.Tick, nil, nil,
		true, true, // BOTH axes enabled: the predicate must consult both
		nil, // nil genSnapshot → discover always issues a real scan
		nil, // nil collectInFlight → the collect gate is inert
		func() uint64 { return epoch.Load() },
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Only the EMBED loop runs. That is the point of step 4: the summary loop is
	// asleep and does not iterate, so its stamp can only be whatever it last wrote.
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		c.runEmbedLoop(ctx)
	}()

	// STEP 1: both axes drained at epoch 0, so both carry 0+1 = 1. The embed loop
	// reaches this on its own; the summary stamp is written directly because that
	// loop is deliberately not running.
	c.summaryDrainedAtEpoch.Store(1)
	require.Eventually(t, func() bool {
		return c.embedDrainedAtEpoch.Load() == 1
	}, 5*time.Second, time.Millisecond,
		"the embed loop must reach its drain edge and stamp the current epoch")

	require.True(t, c.quiescentBothAxes(),
		"PRECONDITION: with both axes stamped at the current epoch the predicate fires — "+
			"without this the decline below could be true for any reason at all")

	// STEP 2: a collect completes. Nothing else changes; in particular the summary
	// loop still does not run, so its stamp stays at 1.
	epoch.Store(1)

	// STEP 3: the embed loop reaches its drain edge again with zero embed work and
	// re-stamps at the NEW epoch (1+1 = 2).
	require.Eventually(t, func() bool {
		return c.embedDrainedAtEpoch.Load() == 2
	}, 5*time.Second, time.Millisecond,
		"the embed loop must re-stamp at the new epoch after the collect")

	// STEP 4: the summary axis still carries its PRE-COLLECT stamp of 1.
	require.EqualValues(t, 1, c.summaryDrainedAtEpoch.Load(),
		"fixture: the summary axis must still hold its pre-collect stamp, which is what "+
			"makes this the staleness case rather than a fresh agreement")

	require.False(t, c.quiescentBothAxes(),
		"the summary axis's observation predates a collect that has since landed new nodes, "+
			"so it must NOT count as a drain of the corpus that collect added to. A per-axis "+
			"BOOLEAN reports true here — both axes 'drained' — and the verdict then reports a "+
			"deficit of every node the collect added and drives a from-scratch rebuild")

	cancel()
	<-loopDone
}
