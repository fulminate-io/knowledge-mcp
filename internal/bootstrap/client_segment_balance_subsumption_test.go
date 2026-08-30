// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// client_segment_balance_subsumption_test.go is the SUBSUMPTION proof for the
// truncated-drain residual.
//
// THE RESIDUAL, RESTATED: a rebuild drain that serves a SHORT page and then an EMPTY one
// is indistinguishable at the client from a complete drain — the client has no
// server-supplied cardinality to compare against, and the plan took SUBSUMPTION over
// adding one on cost grounds (a cardinality for the segment_rebuild scan is a count over
// the vectored corpus, measured at ~420ms / 55k buffers, and it would ride EVERY page).
//
// SUBSUMPTION IS DEMONSTRATED HERE, NOT ASSERTED: the short drain really does leave the
// corpus short, the next quiescence evaluation really does observe it, and the repair
// really does converge. Nothing in this test inspects the drain's own bookkeeping —
// which is the point, because the whole claim is that the balance verdict catches a
// shortfall the drain itself could not report.

// TestBalanceVerdict_SubsumesTheTruncatedDrainResidual drives the full arc: a
// short-then-empty rebuild drain, a quiescence evaluation that reports a deficit and
// drives EXACTLY ONE rebuild, and a following evaluation that reads balanced.
func TestBalanceVerdict_SubsumesTheTruncatedDrainResidual(t *testing.T) {
	const (
		full  = 24 // what the graph really holds, per the server's vector count
		short = 9  // what the truncated drain manages to ship
	)
	gt, name := kgtypes.GraphKnowledge, propagationGraphName
	c, eng, _ := buildReconcileClientWithDir(t, full)
	ctx := context.Background()

	// THE TRUNCATED DRAIN. The fake engine serves its page ONCE and then empty pages —
	// which IS the short-then-empty shape — so a rebuild against a short page ships a
	// short corpus and terminates cleanly, reporting nothing wrong.
	eng.scanItems[name] = makeReconcileScanPage(name, short)
	require.NoError(t, rebuildDriverFor(c)(ctx, gt, name))

	resident, err := c.segmentMgr.LoadLiveResidentDocCount(ctx, gt, name)
	require.NoError(t, err)
	require.Equal(t, short, resident,
		"FIXTURE CONTROL: the truncated drain must actually leave the corpus SHORT — this "+
			"is the residual under test, and a fixture that shipped the full corpus would "+
			"make every assertion below vacuous")

	armFuse(t, c, gt, name)

	// THE REAP FINDS NOTHING, which is the genuine-shortfall shape: a short drain and
	// dead vectors both present as resident < done, and the reap running FIRST is what
	// separates them.
	r := &countingReaper{reports: 0}
	c.reaper = r

	// The real rebuild driver, wrapped so its invocations are countable. THE REPAIR IS
	// REAL — this is not a stub that reports success — so "the following evaluation
	// reports balanced" is a measurement of the repair's effect rather than of the
	// fixture's arithmetic.
	rebuilds := 0
	driver := rebuildDriverFor(c)
	c.rebuild = func(rctx context.Context, rgt kgtypes.GraphType, rname string) error {
		rebuilds++
		// The next drain is no longer truncated: this is the corpus the graph really
		// holds, and shipping it is what the repair is supposed to accomplish.
		eng.scanItems[rname] = makeReconcileScanPage(rname, full)
		return driver(rctx, rgt, rname)
	}

	before := c.evaluateArmBalance(balanceCtx(), gt, name)
	require.Equal(t, armDeficit, before.verdict,
		"the quiescence evaluation must report a DEFICIT the drain never reported: %s",
		before.String())

	runBalanceEdge(t, c, gt, name)

	require.Len(t, r.invocations(), 1,
		"the reap runs FIRST and finds nothing — a genuine shortfall is not a dead-vector "+
			"inflation, and only running the reap first can tell them apart")
	assert.Equal(t, 1, rebuilds,
		"the surviving deficit drives EXACTLY ONE rebuild")

	// AND THE FOLLOWING EVALUATION READS BALANCED — the convergence half, without which
	// this would prove only that a defect is detected, never that it is repaired.
	after := c.evaluateArmBalance(balanceCtx(), gt, name)
	assert.Equal(t, armBalanced, after.verdict,
		"the following quiescence evaluation must read balanced: the rebuild shipped the "+
			"corpus the truncated drain missed: %s", after.String())
	assert.Equal(t, full, after.resident,
		"and the local index now holds the whole corpus")
}
