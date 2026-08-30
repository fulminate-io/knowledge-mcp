// SPDX-License-Identifier: Apache-2.0

package tools

// intercept_mutate_cascade_guard_test.go holds the cascade guards added after the
// changeset review: the batch helper's applied-count refusal, the agreement
// between the unevaluated-criterion hold and the announcer, and the
// evaluated-pass vocabulary class.
//
// They live beside intercept_mutate_cascade_test.go rather than inside it because
// that file sits against the repo's per-file length ceiling. Both drive the same
// helpers and fixtures declared there.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestInterceptMutate_TerminalCascade_ShortBatchWriteIsRefused pins the batch
// helper's count check at the level the caller actually experiences it: a cascade
// whose store reports fewer applied nodes than it was asked to write must FAIL,
// rather than returning a success line enumerating ids it never confirmed.
//
// The two subtests are the two directions of one assertion, and the second is the
// one that makes the first mean anything. Driving the count short must produce an
// error; leaving the count alone must NOT — otherwise a check that refused every
// write would satisfy the first subtest while breaking the feature outright.
//
// The short count is seeded with the fake's existing mutateAffected override
// rather than a new knob, and 1 is deliberately below the three ids a completed
// cascade over this fixture writes (root plus two descendants).
func TestInterceptMutate_TerminalCascade_ShortBatchWriteIsRefused(t *testing.T) {
	t.Run("a short applied count is refused and names the delta", func(t *testing.T) {
		fc := localTicketFake(t)
		fc.mutateAffected = 1

		_, res := cascadeCall(t, interceptTestDeps{gc: fc},
			`{"operation":"update","id":"tkt-1","status":"completed"}`)

		require.True(t, res.IsError,
			"a batch that wrote fewer nodes than it names must not report success")
		body := toolResultText(res)
		assert.Contains(t, body, "3 node(s)", "the message names how many were asked for")
		assert.Contains(t, body, "1 applied", "the message names how many the store reported")
		assert.Contains(t, body, "2 unaccounted for", "the message names the delta")
	})

	t.Run("a matching applied count succeeds", func(t *testing.T) {
		fc := localTicketFake(t)

		_, res := cascadeCall(t, interceptTestDeps{gc: fc},
			`{"operation":"update","id":"tkt-1","status":"completed"}`)

		require.False(t, res.IsError,
			"the check must not fire on a store that applied everything asked: %s", toolResultText(res))
		assert.Contains(t, writtenIDs(statusWrites(fc)), "step-1", "the cascade still writes")
	})
}

// TestInterceptMutate_TerminalCascade_SettledCriterionNeitherHoldsNorHides pins the
// agreement between the hold predicate and the announcer, which is the property
// that keeps a hold followable.
//
// The two subtests are the two polarities of one rule, over the SAME fixture shape
// with only the criterion's status changed:
//   - a criterion a human marked "cancelled" has been dispositioned, so the step
//     above it COMPLETES and the criterion is NOT named — there is nothing to act
//     on, so saying nothing is correct;
//   - a "pending" criterion has not, so the step is HELD and the criterion IS
//     named — the caller is told exactly which one to run and mark.
//
// The pending subtest is the known-positive: without it the cancelled subtest is
// satisfied by a build in which the hold never fires at all, which would be the
// opposite defect. What must never occur is the third combination — held AND not
// named — because the remedy the response prints then cannot be followed and
// re-issuing the completion reproduces the identical hold.
func TestInterceptMutate_TerminalCascade_SettledCriterionNeitherHoldsNorHides(t *testing.T) {
	fake := criterionHoldFake(t)

	t.Run("a cancelled criterion neither holds its container nor is announced", func(t *testing.T) {
		fc := fake("tkt-set", "step-set", "crit-set", "cancelled")
		_, res := cascadeCall(t, interceptTestDeps{gc: fc},
			`{"operation":"update","id":"tkt-set","status":"completed"}`)
		require.False(t, res.IsError, "the cascade must succeed: %s", toolResultText(res))

		writes := statusWrites(fc)
		i := findWriteAfter(writes, 0, kgtypes.StatusCompleted)
		require.NotEqual(t, -1, i, "no cascade write at completed; writes were %+v", writes)
		assert.Contains(t, writes[i].ids, "step-set",
			"a criterion a human dispositioned does not block its container")
		assert.NotContains(t, toolResultText(res), "crit-set",
			"a criterion whose work is over is not news, so it is not announced")
	})

	t.Run("a pending criterion both holds its container and is announced", func(t *testing.T) {
		fc := fake("tkt-open", "step-open", "crit-open", "pending")
		_, res := cascadeCall(t, interceptTestDeps{gc: fc},
			`{"operation":"update","id":"tkt-open","status":"completed"}`)
		require.False(t, res.IsError, "the cascade must succeed: %s", toolResultText(res))

		body := toolResultText(res)
		assert.NotContains(t, writtenIDs(statusWrites(fc)), "step-open",
			"a container owning an unevaluated criterion is held")
		assert.Contains(t, body, "step-open", "the held container is named")
		assert.Contains(t, body, "crit-open",
			"the criterion to run and mark is named — a hold nobody can act on is a stuck state")
	})
}

// criterionHoldFake returns a builder for the one fixture shape both the settled
// guard and the evaluated-pass guard drive: a ticket containing a pending step
// containing a single criterion whose status is the variable under test.
//
// Hoisted out of the settled guard so the two tests provably run against the SAME
// tree with only the criterion's status differing. A second hand-written copy
// would let the control and the positive drift onto different shapes, at which
// point neither is a control for the other.
func criterionHoldFake(t *testing.T) func(root, step, crit, critStatus string) *fakeGraphCaller {
	t.Helper()
	return func(root, step, crit, critStatus string) *fakeGraphCaller {
		return &fakeGraphCaller{
			queryResponses: map[string]kgtools.ToolResult{
				root: nodeResultJSON(t, root, string(kgtypes.NodeTicket), map[string]string{}),
			},
			traversalByRoot: map[string][]*knowledgev1.Node{
				root: {
					{Id: step, Type: string(kgtypes.NodeStep), Status: "pending"},
					{Id: crit, Type: string(kgtypes.NodeCriterion), Status: critStatus},
				},
			},
			traversalEdgesByRoot: map[string][]knowledgev1.Edge{
				root: {
					{FromId: root, ToId: step, Type: string(kgtypes.EdgeKGContains)},
					{FromId: step, ToId: crit, Type: string(kgtypes.EdgeKGContains)},
				},
			},
		}
	}
}

// evaluatedPassSpellings is the class as the corpus actually spells it, declared
// once so the classification leg and the behavior leg below cannot drift onto
// different member lists.
var evaluatedPassSpellings = []string{"pass", "passed", "verified", "satisfied", "met"}

// TestInterceptMutate_TerminalCascade_EvaluatedPassClassIsSettled pins the
// evaluated-pass vocabulary ruling at BOTH levels it has to hold, because either
// one alone is satisfiable by a build that does not work.
//
//   - The CLASSIFICATION leg proves the predicate answers correctly for all five
//     spellings and, in the same run, that it does NOT answer true for statuses
//     outside the class. Without those controls a predicate hard-wired to return
//     true would pass the positive leg outright.
//   - The BEHAVIOR leg proves the classification is actually WIRED to the
//     consumer that motivated it: a criterion spelled "pass" must stop holding
//     its container AND stop being announced. A predicate nothing reads would
//     satisfy the classification leg and change nothing a caller experiences.
//
// The "retired" subtest is the behavioral control and it is deliberately a
// spelling the corpus really carries: retired sits OUTSIDE the ruling, so it must
// still hold and still be announced. If the widening were written too broadly —
// settling every unrecognized spelling rather than the five — that subtest is the
// one that goes red.
func TestInterceptMutate_TerminalCascade_EvaluatedPassClassIsSettled(t *testing.T) {
	t.Run("the canonical spelling is a member of the class it names", func(t *testing.T) {
		assert.Equal(t, "pass", criterionPassStatus,
			"the canonical spelling is the one the shipped status vocabulary declares")
		assert.Contains(t, evaluatedPassSpellings, criterionPassStatus,
			"a canonical spelling the class itself does not accept would settle nothing")
	})

	t.Run("all five spellings classify as an evaluated pass", func(t *testing.T) {
		for _, status := range evaluatedPassSpellings {
			assert.True(t, isEvaluatedPass(status),
				"%q records a check that was run and passed", status)
			assert.True(t, isSettledForCascade(status),
				"%q must reach the settled authority, not just the class predicate", status)
		}
	})

	t.Run("statuses outside the class keep their current classification", func(t *testing.T) {
		// retired and the empty string are the two the ruling names; pending,
		// blocked and pending-manual are live spellings the corpus carries and
		// are here so the control is not satisfiable by a single special case.
		for _, status := range []string{"retired", "", "pending", "blocked", "pending-manual"} {
			assert.False(t, isEvaluatedPass(status),
				"%q does not record a check that was run and passed", status)
			assert.False(t, isSettledForCascade(status),
				"%q is not settled, so it must still hold its container", status)
		}
	})

	fake := criterionHoldFake(t)

	for _, status := range evaluatedPassSpellings {
		t.Run("a "+status+" criterion neither holds its container nor is announced", func(t *testing.T) {
			fc := fake("tkt-"+status, "step-"+status, "crit-"+status, status)
			_, res := cascadeCall(t, interceptTestDeps{gc: fc},
				`{"operation":"update","id":"tkt-`+status+`","status":"completed"}`)
			require.False(t, res.IsError, "the cascade must succeed: %s", toolResultText(res))

			writes := statusWrites(fc)
			i := findWriteAfter(writes, 0, kgtypes.StatusCompleted)
			require.NotEqual(t, -1, i, "no cascade write at completed; writes were %+v", writes)
			assert.Contains(t, writes[i].ids, "step-"+status,
				"a criterion that was run and passed does not block its container")
			assert.NotContains(t, toolResultText(res), "crit-"+status,
				`telling the caller to "run and mark" a criterion already run and marked is a remedy already satisfied`)
		})
	}

	t.Run("a retired criterion still holds its container and is announced", func(t *testing.T) {
		fc := fake("tkt-retired", "step-retired", "crit-retired", "retired")
		_, res := cascadeCall(t, interceptTestDeps{gc: fc},
			`{"operation":"update","id":"tkt-retired","status":"completed"}`)
		require.False(t, res.IsError, "the cascade must succeed: %s", toolResultText(res))

		body := toolResultText(res)
		assert.NotContains(t, writtenIDs(statusWrites(fc)), "step-retired",
			"retired sits outside the ruling, so it is still an unevaluated criterion")
		assert.Contains(t, body, "crit-retired",
			"a criterion outside the class is still announced")
	})
}
