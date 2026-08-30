// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// collector_balance_edge_test.go drives the QUIESCENCE-EDGE BALANCE VERDICT through the
// REAL embed loop, on the ZERO-EMBED-WORK collect path specifically.
//
// THAT PATH IS THE ONE THE ASYMMETRY LIVES ON, and it is also the common one. The
// quiescence flush is WORK-gated — it fires only when embed work happened since the last
// one — while the heal and this verdict are COLLECT-gated. So a collect carrying no
// embed work (the already-embedded shape) reaches the verdict with the flush unfired,
// and the resident count reads the SEALED set only. Evaluating there would read short by
// the whole unsealed sub-threshold tail and report a deficit against a corpus that is
// merely still buffered — a false-unhealthy arriving through the fix.

// balanceEdgeCollector builds a collector wired for the balance edge, returning it
// alongside the epoch handle, a flush recorder and a verdict recorder.
func balanceEdgeCollector(t *testing.T, flushErr error) (
	c *collector, epoch *atomic.Uint64, flushes, verdicts *atomic.Int64,
) {
	t.Helper()
	cfg := Config{
		EmbedBatchSize:   5,
		EmbedWorkers:     2,
		EmbedChannelSize: 100,
		Tick:             time.Millisecond,
		IdleTickMax:      time.Millisecond,
	}
	// ZERO EMBED WORK at every point — the already-embedded shape this path exists for.
	fake := newStatefulEmbedGapFake(nil, 0)

	epoch = &atomic.Uint64{}
	flushes = &atomic.Int64{}
	verdicts = &atomic.Int64{}

	flush := func(context.Context) error {
		flushes.Add(1)
		return flushErr
	}
	embedCh := make(chan EmbedWork, cfg.EmbedChannelSize)
	c = newCollector(
		kgtypes.GraphCode, "agent", cfg,
		nil, embedCh, &metricsState{}, fake,
		cfg.Tick, cfg.Tick, flush, nil,
		false, true, // summary axis DISABLED so it is vacuously drained; embed runs
		nil, nil,
		func() uint64 { return epoch.Load() },
	)
	c.balanceAtQuiescence = func(context.Context) error {
		verdicts.Add(1)
		return nil
	}
	return c, epoch, flushes, verdicts
}

// TestBalanceEdge_ZeroEmbedWork_SealsBeforeEvaluating covers the work-gated flush
// asymmetry the way the step requires: DRIVEN THROUGH THE LOOP, not by calling the
// predicate.
//
// TWO PROPERTIES, and the ORDER between them is the whole point:
//   - the verdict runs on this path at all (the collect-gated arm fires where the
//     work-gated flush does not), and
//   - a FLUSH happens before it, so the operand it reads describes a sealed corpus.
func TestBalanceEdge_ZeroEmbedWork_SealsBeforeEvaluating(t *testing.T) {
	c, _, flushes, verdicts := balanceEdgeCollector(t, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		c.runEmbedLoop(ctx)
	}()

	require.Eventually(t, func() bool { return verdicts.Load() >= 1 },
		5*time.Second, time.Millisecond,
		"the verdict must fire on the ZERO-EMBED-WORK path — this is the collect-gated arm, "+
			"and it is exactly the case the work-gated quiescence flush does not cover")

	assert.GreaterOrEqual(t, flushes.Load(), int64(1),
		"a force-seal must precede the verdict: the resident count reads the SEALED set only, "+
			"so evaluating against an unsealed sub-threshold tail would report a deficit "+
			"against a corpus that is merely still buffered")

	cancel()
	<-loopDone
}

// TestBalanceEdge_FiresOncePerCollect pins the epoch gate.
//
// WITHOUT IT THE VERDICT WOULD RE-FIRE ON EVERY IDLE TICK. quiescentBothAxes stays TRUE
// for as long as both axes remain drained at the current epoch — indefinitely on a quiet
// daemon — so an ungated edge would pay a Stats RPC, a resident read and a force-seal on
// every pass around the idle loop. This asserts the COUNT stops moving while the epoch
// is still, and moves again when a collect advances it.
func TestBalanceEdge_FiresOncePerCollect(t *testing.T) {
	c, epoch, _, verdicts := balanceEdgeCollector(t, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		c.runEmbedLoop(ctx)
	}()

	require.Eventually(t, func() bool { return verdicts.Load() >= 1 },
		5*time.Second, time.Millisecond, "the verdict fires for the first collect epoch")

	// The loop keeps spinning at a 1ms tick. If the edge were ungated the count would
	// climb continuously; the epoch gate must hold it at exactly one.
	settled := verdicts.Load()
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, settled, verdicts.Load(),
		"the verdict must fire ONCE PER COLLECT, not once per tick — the loop turned many "+
			"times during this sleep and the epoch has not moved")

	// A COLLECT MOVES THE EPOCH: the edge re-arms for free. This is the known-positive
	// that stops the assertion above being satisfied by a verdict that can never fire
	// twice at all.
	epoch.Store(1)
	require.Eventually(t, func() bool { return verdicts.Load() > settled },
		5*time.Second, time.Millisecond,
		"a completed collect must re-arm the edge, or a long-lived daemon would evaluate "+
			"the balance exactly once and never again")

	cancel()
	<-loopDone
}

// TestBalanceEdge_FailedSealDeclinesTheVerdict pins that a failed force-seal DECLINES
// rather than evaluating anyway.
//
// AN UNSEALED TAIL IS PRECISELY THE STATE THAT MANUFACTURES A FALSE DEFICIT, so
// proceeding on a failed seal would produce the wrong answer with confidence — and the
// verdict's whole purpose is to be believed.
func TestBalanceEdge_FailedSealDeclinesTheVerdict(t *testing.T) {
	c, _, flushes, verdicts := balanceEdgeCollector(t, errors.New("seal failed"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		c.runEmbedLoop(ctx)
	}()

	// FIXTURE CONTROL: the seal must actually be ATTEMPTED, or the zero below would be
	// measuring a path that never reached the flush at all.
	require.Eventually(t, func() bool { return flushes.Load() >= 1 },
		5*time.Second, time.Millisecond,
		"FIXTURE CONTROL: the balance edge must reach the force-seal")

	time.Sleep(50 * time.Millisecond)
	assert.Zero(t, verdicts.Load(),
		"a failed seal must DECLINE the verdict: an unsealed tail reads as a deficit that "+
			"is not there, and reporting it would be a false-unhealthy produced by the check "+
			"itself")

	cancel()
	<-loopDone
}
