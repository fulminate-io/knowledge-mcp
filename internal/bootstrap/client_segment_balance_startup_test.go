// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// client_segment_balance_startup_test.go pins the BOOT report: that it forms a real
// verdict on a deliberately imbalanced pool, that it forms exactly one per graph, and
// that it drives NO repair.

// requireWalked asserts the graph is in the set segmentBearingGraphs walks. Without
// it the boot pass finds nothing and every assertion below would pass on an empty
// walk — an absence-shaped result that looks identical to a healthy one.
func requireWalked(t *testing.T, c *client, gt kgtypes.GraphType, name string) {
	t.Helper()
	walked := c.segmentBearingGraphs()
	require.Contains(t, walked, segmentGraphRef{gt: gt, name: name},
		"FIXTURE CONTROL: the graph must be in the walked set, or the boot pass reports "+
			"on nothing: %v", walked)
}

// TestStartupBalanceReport_ReportsAnImbalancedPoolAndRepairsNothing is the ticket's
// deliberately-imbalanced fixture: the server holds more vectors than the local pool
// has documents, which is the shape a daemon can start into and never notice until
// the first rebuild.
func TestStartupBalanceReport_ReportsAnImbalancedPoolAndRepairsNothing(t *testing.T) {
	// 9 vectors server-side against the fixture's 4 resident documents — a 5-document
	// deficit sitting there at boot.
	const embedded int32 = 9
	gt, name := kgtypes.GraphKnowledge, propagationGraphName

	c, _ := balanceFixtureWithEngine(t, embedded)
	requireWalked(t, c, gt, name)

	// THE REPAIR ARMS ARE WIRED AND COUNTING. Asserting "no rebuild happened" against a
	// client with no rebuild driver would pass on a nil field rather than on the boot
	// pass declining to use one.
	r := &countingReaper{reports: 0}
	c.reaper = r
	rebuilds := 0
	c.rebuild = func(context.Context, kgtypes.GraphType, string) error {
		rebuilds++
		return nil
	}

	c.reportStartupBalance(context.Background())

	verdicts, ran := c.StartupBalanceVerdicts()
	require.True(t, ran, "the pass must record that it RAN, which an empty map cannot say")
	got, found := verdicts[string(gt)+"/"+name]
	require.True(t, found, "the walked graph must carry a recorded verdict: %v", verdicts)
	assert.Contains(t, got, "deficit",
		"the boot report must name the imbalance it found on this pool: %s", got)
	assert.Contains(t, got, "resident 4 < owed 9",
		"and it must name the INEQUALITY with its operands, never the bare word")

	// THE WHOLE POINT OF THE SEPARATION: boot reports, it does not arm.
	assert.Empty(t, r.invocations(),
		"the boot report must not invoke the reap — it knows nothing about pipeline "+
			"quiescence, so it cannot tell an in-flight corpus from a short one")
	assert.Zero(t, rebuilds, "and it must not drive a rebuild either")
}

// TestStartupBalanceReport_EvaluatesEachGraphExactlyOnce pins the "exactly once" half.
// It counts the COVERAGE READS the evaluation issues, because that is the observable
// the verdict is built from — a second evaluation cannot happen without a second read.
func TestStartupBalanceReport_EvaluatesEachGraphExactlyOnce(t *testing.T) {
	gt, name := kgtypes.GraphKnowledge, propagationGraphName
	c, eng := balanceFixtureWithEngine(t, 4)
	requireWalked(t, c, gt, name)

	before := eng.statsCallCount()
	c.reportStartupBalance(context.Background())
	afterOne := eng.statsCallCount()
	require.Equal(t, 1, afterOne-before,
		"one pass over one graph must read the coverage operands exactly once")

	verdicts, _ := c.StartupBalanceVerdicts()
	require.Contains(t, verdicts[string(gt)+"/"+name], "balanced",
		"CONTROL: a matched pool reports balanced, so the count above is of a real "+
			"evaluation rather than of an early return")
}

// TestStartupBalanceReport_DeclinesWithoutASegmentManager pins the headless client:
// it must record NOTHING rather than a verdict formed from an absent engine.
func TestStartupBalanceReport_DeclinesWithoutASegmentManager(t *testing.T) {
	c := &client{}
	c.reportStartupBalance(context.Background())

	verdicts, ran := c.StartupBalanceVerdicts()
	assert.False(t, ran,
		"a client with no segment engine measured nothing, and must not report that it did")
	assert.Empty(t, verdicts)
}
