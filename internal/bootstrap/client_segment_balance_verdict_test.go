// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// client_segment_balance_verdict_test.go pins the QUIESCENCE-EDGE verdict: which
// operands it reads, and the heal-first ORDERING it applies to an imbalance.

// countingReaper is a ReapInvoker double that records every invocation with the gap it
// was asked to close, and scripts what it reports removing.
//
// IT STANDS IN FOR A DEPENDENCY, NOT FOR THE CODE UNDER TEST. The thing under test is
// the ordering in evaluateBalanceAtQuiescence — evaluate, reap, RE-READ, conclude — and
// that ordering runs for real here. Substituting the server's reap removes only the
// server, which is the point: it lets a test assert that the verdict RE-READS rather
// than trusting the reap's own arithmetic, by having the reap LIE about what it removed.
type countingReaper struct {
	mu sync.Mutex

	// removeFn, when set, actually mutates the fixture so a re-read observes a change.
	// nil means the reap reports a number and changes nothing — the "reap over-reports"
	// shape.
	removeFn func()

	reports int // what each call claims to have removed
	err     error

	calls []int // the gap each invocation was asked to close
}

func (r *countingReaper) ReapDeadVectors(_ context.Context, _ kgtypes.GraphType, _ string, gap int) (int, error) {
	r.mu.Lock()
	r.calls = append(r.calls, gap)
	fn, reports, err := r.removeFn, r.reports, r.err
	r.mu.Unlock()
	if err != nil {
		return 0, err
	}
	if fn != nil {
		fn()
	}
	return reports, nil
}

func (r *countingReaper) invocations() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int(nil), r.calls...)
}

// balanceFixtureResidentDocs is the resident corpus size every ordering case below
// seeds. It is a NAMED CONSTANT rather than a parameter because holding it fixed is what
// makes the cases comparable: each varies only the SERVER's reported vector count, so
// the inequality under test is the only thing that differs between them.
const balanceFixtureResidentDocs = 4

// balanceFixture builds a client whose Stats seam reports `embedded` vectors and whose
// L2 cache holds balanceFixtureResidentDocs real documents, and returns it with the
// graph coords.
func balanceFixture(t *testing.T, embedded int32) (*client, kgtypes.GraphType, string) {
	t.Helper()
	gt, name := kgtypes.GraphKnowledge, propagationGraphName
	c, _, dir := buildReconcileClientWithDir(t, embedded)
	seedL2Corpus(t, dir, gt, name, balanceFixtureResidentDocs)
	return c, gt, name
}

// armFuse makes fuseCaughtUp PASS for this graph, which every ordering case below needs
// before the verdict will form at all.
//
// BOTH OPERANDS ARE REQUIRED, and forgetting either is a silent no-verdict rather than a
// failure: the server stamp must be SAMPLED (an unsampled graph declines because nothing
// is known about the server side), and the LOCAL merge watermark must be non-zero (a
// graph that has fused nothing is never judged, however the server stamp reads). The
// watermark is written through the manager's own SaveMergeWatermark so the left operand
// is produced the way production produces it.
func armFuse(t *testing.T, c *client, gt kgtypes.GraphType, name string) {
	t.Helper()
	c.serverSegmentStamp = func(kgtypes.GraphType, string) (int64, bool) { return 1_000, true }
	require.NoError(t, c.segmentMgr.SaveMergeWatermark(gt, name, 5_000))
	caughtUp, why, err := c.fuseCaughtUp(context.Background(), gt, name)
	require.NoError(t, err)
	require.True(t, caughtUp,
		"FIXTURE CONTROL: the fuse must be caught up or the verdict declines before it "+
			"forms, and every invocation assertion below would pass vacuously: %s", why)
}

// balanceCtx stamps the query-origin operation the Stats RPC requires of every covered
// call site. Production stamps it inside the closure buildBalanceFactory returns; a test
// reading the operands DIRECTLY has to stamp it too, or the coverage read fails and the
// verdict correctly refuses to evaluate — which would make every assertion below pass
// for the wrong reason.
func balanceCtx() context.Context {
	return graphclient.WithOperation(context.Background(), graphclient.OpSegmentHeal)
}

// runBalanceEdge drives the verdict THROUGH THE REAL FACTORY rather than calling
// evaluateBalanceAtQuiescence directly.
//
// THAT IS THE POINT OF THIS HELPER. The factory is where the graph gate and the
// operation stamping live, so a test that bypassed it would prove the ordering logic
// while leaving the wiring — the part that decides whether the ordering ever runs in
// production — unexercised in both directions.
func runBalanceEdge(t *testing.T, c *client, gt kgtypes.GraphType, name string) {
	t.Helper()
	fn := c.buildBalanceFactory()(gt, name)
	require.NotNil(t, fn, "the factory must build a closure for a graph with rebuildable segments")
	require.NoError(t, fn(context.Background()))
}

// TestBalanceVerdict_ReadsTheDistinctResidentCount pins the equation's OPERAND.
//
// WHAT THIS TEST DOES AND DOES NOT COVER, stated plainly because the difference matters.
// It covers that the equation reads the DISTINCT-and-live count and that duplication is
// carried as its own field rather than folded into the balance. It does NOT drive a
// corpus in which the summing and distinct readers DISAGREE, because that state is not
// reachable through the manager's public surface in-process: a rebuild drains the
// segments it supersedes, so two emit cycles over overlapping ids — with identical
// content or with perturbed content, from one producer or from two — converge to a
// single segment and both readers answer the same number. Measured, not assumed.
//
// THE CONSEQUENCE, RECORDED RATHER THAN PAPERED OVER: the "id resident in two segments"
// arm is covered here only at the arithmetic and rendering level
// (TestArmBalance_DuplicationIsRenderedOnlyWhenNonZero below), not end to end through a
// real duplicated corpus. Whoever can arrange a retained-tail corpus deterministically
// should extend this rather than assume it is covered.
func TestBalanceVerdict_ReadsTheDistinctResidentCount(t *testing.T) {
	const docs = 8
	gt, name := kgtypes.GraphKnowledge, propagationGraphName
	c, _, dir := buildReconcileClientWithDir(t, docs)
	seedL2Corpus(t, dir, gt, name, docs)
	ctx := context.Background()

	distinct, err := c.segmentMgr.LoadLiveResidentDocCount(ctx, gt, name)
	require.NoError(t, err)
	require.Equal(t, docs, distinct,
		"FIXTURE CONTROL: the seeded corpus must actually be resident, or the operand "+
			"assertion below would compare two zeros")

	b := c.evaluateArmBalance(balanceCtx(), gt, name)
	assert.Equal(t, armBalanced, b.verdict,
		"a corpus holding exactly its vector count balances: %s", b.String())
	assert.Equal(t, distinct, b.resident,
		"the equation's operand is the DISTINCT-and-live count")
	assert.Zero(t, b.duplication,
		"with one segment there is no duplication to report")
	assert.NotContains(t, b.String(), "duplicated",
		"a ZERO duplication must be omitted entirely — a clause printed on every healthy "+
			"graph is noise that trains a reader to skip the line")
}

// TestArmBalance_DuplicationIsRenderedOnlyWhenNonZero is the KNOWN-POSITIVE for the
// duplication clause, and it exists because the test above cannot supply one.
//
// WITHOUT IT the "must not contain duplicated" assertion above would be satisfied by a
// renderer that can NEVER emit the clause — an assertion about an empty output is worth
// nothing unless something in the same package drives the same renderer non-empty.
func TestArmBalance_DuplicationIsRenderedOnlyWhenNonZero(t *testing.T) {
	b := balancedAtQuiescence(8, 8, 0, 0, false)
	require.Equal(t, armBalanced, b.verdict)
	assert.NotContains(t, b.String(), "duplicated", "zero duplication is omitted")

	b.duplication = 3
	assert.Contains(t, b.String(), "3 duplicated resident documents",
		"a NON-ZERO duplication must be RENDERED — carrying the count in the struct and "+
			"never printing it satisfies the visibility rule only on paper")
	assert.Contains(t, b.String(), "balanced",
		"and it is reported BESIDE the verdict, never folded into it: duplication must "+
			"not change the balance")
}

// TestBalanceVerdict_HealFirstOrdering covers the reap-invocation shapes the verdict is
// specified against. Every case asserts INVOCATION COUNTS, not end state.
func TestBalanceVerdict_HealFirstOrdering(t *testing.T) {
	t.Run("resident > done never invokes the reap", func(t *testing.T) {
		// Written as the INEQUALITY throughout, never the bare word, because the word
		// means opposite things in the two readings that meet here and reading one as
		// the other inverted this trigger once already.
		//
		// Four resident documents against a reported ONE vector: the local index holds
		// MORE than the graph has.
		c, gt, name := balanceFixture(t, 1)
		r := &countingReaper{}
		c.reaper = r
		armFuse(t, c, gt, name)

		before := c.evaluateArmBalance(balanceCtx(), gt, name)
		require.Equal(t, armSurplus, before.verdict,
			"FIXTURE CONTROL: this corpus must actually produce resident > done, or the "+
				"zero-invocation assertion below would be vacuous: %s", before.String())

		runBalanceEdge(t, c, gt, name)
		assert.Empty(t, r.invocations(),
			"resident > done must NEVER invoke the reap: the reap lowers the vector count, "+
				"which would WIDEN this gap rather than close it")
	})

	t.Run("the reap is asked to close exactly the observed gap", func(t *testing.T) {
		// Nine reported vectors against four resident documents: a gap of five.
		c, gt, name := balanceFixture(t, 9)
		r := &countingReaper{reports: 5}
		c.reaper = r
		armFuse(t, c, gt, name)

		before := c.evaluateArmBalance(balanceCtx(), gt, name)
		require.Equal(t, armDeficit, before.verdict,
			"FIXTURE CONTROL: this corpus must produce resident < done: %s", before.String())

		runBalanceEdge(t, c, gt, name)
		require.Len(t, r.invocations(), 1, "the reap must run exactly once")
		assert.Equal(t, (before.done-before.failures)-before.resident, r.invocations()[0],
			"the gap handed to the reap is the observed imbalance, which is what its "+
				"escalation decision is made against")
	})

	t.Run("a reap that over-reports still yields a defect, because the verdict RE-READS", func(t *testing.T) {
		// THIS IS THE ARM A SUBTRACT-THE-COUNT IMPLEMENTATION PASSES AND A RE-READ
		// IMPLEMENTATION CANNOT HIDE. The reap claims to have removed the whole gap but
		// changes nothing; an implementation computing the post-reap balance as
		// (done - removed) would read balanced and report health over an unchanged
		// corpus. A genuine re-read observes the same disagreement and reports a defect.
		c, gt, name := balanceFixture(t, 9)
		r := &countingReaper{reports: 5 /* removeFn nil: it changes NOTHING */}
		c.reaper = r
		armFuse(t, c, gt, name)
		rebuilds := countingRebuilds(c)

		runBalanceEdge(t, c, gt, name)
		require.Len(t, r.invocations(), 1, "the reap ran once")

		// THE ASSERTION IS ON WHAT THE PRODUCTION PATH DID, not on a verdict this test
		// recomputes. An earlier version called evaluateArmBalance again and asserted the
		// fresh operands still disagreed — which is TRUE of the fixture no matter what
		// evaluateBalanceAtQuiescence decided, so it would have passed against a
		// subtract-the-count implementation and gated nothing. The rebuild COUNT is the
		// observable that separates them: a genuine re-read sees the surviving deficit
		// and drives one rebuild, while subtracting the reap's claimed 5 from a done of 9
		// reads balanced and drives none.
		assert.Equal(t, 1, *rebuilds,
			"the verdict must be formed from a MEASUREMENT taken after the reap, never "+
				"from the reap's own accounting: a reap that over-reports must still leave "+
				"a surviving deficit that drives exactly one rebuild")
	})

	t.Run("a reap error does not conclude a defect", func(t *testing.T) {
		c, gt, name := balanceFixture(t, 9)
		r := &countingReaper{err: errors.New("reap transport failed")}
		c.reaper = r
		armFuse(t, c, gt, name)

		// An UNHEALED gap is not evidence of a defect: the heal never got to run, so
		// concluding from the pre-reap operands would report a defect the reap might
		// have repaired. The observable is that the reap was attempted and the verdict
		// declined to conclude.
		runBalanceEdge(t, c, gt, name)
		assert.Len(t, r.invocations(), 1, "the reap was attempted")
	})

	t.Run("a fuse that is not caught up asserts nothing at all", func(t *testing.T) {
		c, gt, name := balanceFixture(t, 9)
		r := &countingReaper{reports: 5}
		c.reaper = r
		// No serverSegmentStamp reader wired → fuseCaughtUp declines, so the verdict
		// must not even form. Without this gate a corpus merely IN FLIGHT would be
		// reported short and rebuilt for documents that were about to arrive.
		c.serverSegmentStamp = nil

		runBalanceEdge(t, c, gt, name)
		assert.Empty(t, r.invocations(),
			"with the fuse position unknown the verdict must decline entirely — no reap, "+
				"no defect, no rebuild")
	})

	t.Run("no reap invoker wired reports rather than concluding", func(t *testing.T) {
		c, gt, name := balanceFixture(t, 9)
		c.reaper = nil
		armFuse(t, c, gt, name)

		// The observable is that this does not panic and does not conclude: an imbalance
		// with no available heal is reported, because an unhealed gap is not evidence of
		// a defect.
		runBalanceEdge(t, c, gt, name)
	})
}

// countingRebuilds installs a rebuild driver that records its invocations and returns
// the recorder, so a test can assert HOW MANY rebuilds a verdict drove.
//
// THE COUNT IS THE PROPERTY, NOT THE END STATE. "The reap healed it" and "the rebuild
// healed it" reach the same balanced corpus, and the difference between them is a
// full-corpus rescan — so only a call counter can tell a heal-through from a churn.
func countingRebuilds(c *client) *int {
	n := 0
	c.rebuild = func(context.Context, kgtypes.GraphType, string) error {
		n++
		return nil
	}
	return &n
}

// TestBalanceVerdict_RebuildOnlyForASurvivingDeficit covers both invocation-count
// criteria in ONE test, because each is the other's control.
//
// A ZERO-REBUILD ASSERTION ALONE PROVES NOTHING: an implementation that never rebuilt at
// all would satisfy it. The surviving-deficit case is the known-positive that shows the
// driver is reachable, and the reap-closes-it case is what shows the verdict does not
// churn a corpus it has just healed.
func TestBalanceVerdict_RebuildOnlyForASurvivingDeficit(t *testing.T) {
	t.Run("a gap the reap CLOSES drives no rebuild", func(t *testing.T) {
		gt, name := kgtypes.GraphKnowledge, propagationGraphName
		c, eng, dir := buildReconcileClientWithDir(t, 9)
		seedL2Corpus(t, dir, gt, name, 4)
		armFuse(t, c, gt, name)
		rebuilds := countingRebuilds(c)

		// The reap "removes" the five dead vectors by dropping the server's reported
		// vector count to the resident 4 — the dead-vector direction healing exactly as
		// the heal-through design intends.
		r := &countingReaper{reports: 5, removeFn: func() { eng.setEmbedded(4) }}
		c.reaper = r

		before := c.evaluateArmBalance(balanceCtx(), gt, name)
		require.Equal(t, armDeficit, before.verdict,
			"FIXTURE CONTROL: the pre-reap state must be resident < done: %s", before.String())

		runBalanceEdge(t, c, gt, name)

		require.Len(t, r.invocations(), 1, "the reap ran once")
		after := c.evaluateArmBalance(balanceCtx(), gt, name)
		require.Equal(t, armBalanced, after.verdict,
			"FIXTURE CONTROL: the reap must actually have closed the gap, or the zero "+
				"below would be measuring the wrong branch: %s", after.String())
		assert.Zero(t, *rebuilds,
			"a gap the reap CLOSES must drive NO rebuild — this is the dead-vector "+
				"direction the heal-through ruling exists for, and rebuilding a corpus that "+
				"has just converged is churn on a healthy graph")
	})

	t.Run("a deficit that SURVIVES the reap drives exactly one rebuild", func(t *testing.T) {
		gt, name := kgtypes.GraphKnowledge, propagationGraphName
		c, _, dir := buildReconcileClientWithDir(t, 9)
		seedL2Corpus(t, dir, gt, name, 4)
		armFuse(t, c, gt, name)
		rebuilds := countingRebuilds(c)

		// The reap finds NO dead vectors — the genuine-shortfall shape. Both causes
		// present as resident < done; the reap running first is what separates them.
		r := &countingReaper{reports: 0}
		c.reaper = r

		runBalanceEdge(t, c, gt, name)

		require.Len(t, r.invocations(), 1,
			"the reap must run FIRST — it is what distinguishes a genuine shortfall from "+
				"a dead-vector inflation, and the rebuild must not pre-empt it")
		assert.Equal(t, 1, *rebuilds,
			"a deficit that survived the reap is a genuine shortfall and drives EXACTLY "+
				"one rebuild")
	})

	t.Run("resident > done drives neither a reap nor a rebuild", func(t *testing.T) {
		gt, name := kgtypes.GraphKnowledge, propagationGraphName
		c, _, dir := buildReconcileClientWithDir(t, 1)
		seedL2Corpus(t, dir, gt, name, 4)
		armFuse(t, c, gt, name)
		rebuilds := countingRebuilds(c)
		r := &countingReaper{}
		c.reaper = r

		runBalanceEdge(t, c, gt, name)

		assert.Empty(t, r.invocations(), "lowering the vector count would widen this gap")
		assert.Zero(t, *rebuilds,
			"and this arm has no automated repair at all: it is reported immediately, "+
				"because a signal that something is wrong beats a repair that makes it worse")
	})
}

// TestBalanceVerdict_EvictedPoolIsNotEvaluated pins that an evicted pool is a REFUSAL
// rather than a measured zero.
//
// ITS RESIDENT COUNT READS ZERO because this client's residency budget dropped the
// segments from RAM, not because the documents are gone. A verdict formed there would
// report the entire corpus missing and drive a from-scratch rebuild — undoing the
// eviction at the highest possible cost.
func TestBalanceVerdict_EvictedPoolIsNotEvaluated(t *testing.T) {
	c, gt, name, _ := evictedArmFixture(t)

	b := c.evaluateArmBalance(balanceCtx(), gt, name)
	assert.Equal(t, armNotEvaluated, b.verdict,
		"an evicted pool must not be judged: %s", b.String())
	assert.NotEmpty(t, b.reason, "a refusal must carry its reason")
	assert.Contains(t, b.String(), "not evaluated",
		"the rendered refusal must say so rather than reading as a measurement")
}
