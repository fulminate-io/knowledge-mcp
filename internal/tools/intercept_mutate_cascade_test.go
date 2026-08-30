// SPDX-License-Identifier: Apache-2.0

package tools

// intercept_mutate_cascade_test.go drives the terminal-status cascade end to end
// through InterceptMutate, on BOTH dispatch arms: the local-only one and the
// tracker-backed write-through.
//
// EVERY ASSERTION IS ON OBSERVABLE OUTPUT — the recorded MutationPlans, the
// tracker call count, the result's error flag and its rendered body. None of the
// cascade helpers is named in code here, deliberately: a test that referenced
// them would not compile before they exist, and a red that is a build error is
// not an observation of behaviour.

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/backends"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// statusWrite is one recorded UPDATE mutation reduced to the two facts every
// test here turns on: the status it writes, and the ids it writes it to.
type statusWrite struct {
	status string
	ids    []string
}

// statusWrites projects the fake's recorded MutationPlans, IN ORDER, down to the
// UPDATE mutations that carry a status. Order is load-bearing: several tests
// assert that the descendant write comes AFTER the root's, which is what
// distinguishes a cascade from a single batch that happens to include everything.
func statusWrites(fc *fakeGraphCaller) []statusWrite {
	var out []statusWrite
	for _, m := range fc.execMutations {
		if m.GetKind() != knowledgev1.MutationPlan_MUTATION_KIND_UPDATE {
			continue
		}
		s, ok := m.GetSetFields()["status"]
		if !ok {
			continue
		}
		out = append(out, statusWrite{status: s, ids: m.GetSelection().GetIds()})
	}
	return out
}

// traversalExecutes counts the RETURN_MODE_TRAVERSAL reads the call issued. Zero
// is the observable signature of "no cascade was even attempted", which is what
// the two opt-out tests assert.
func traversalExecutes(fc *fakeGraphCaller) int {
	n := 0
	for _, req := range fc.execRequests {
		if req.GetQuery().GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_TRAVERSAL {
			n++
		}
	}
	return n
}

// writtenIDs is the union of every id any status write touched.
func writtenIDs(writes []statusWrite) []string {
	var out []string
	for _, w := range writes {
		out = append(out, w.ids...)
	}
	return out
}

// findWriteAfter returns the index of the first status write at or after `from`
// whose status matches. Used instead of a status-keyed lookup because the
// completed-family cases legitimately write the same status twice.
func findWriteAfter(writes []statusWrite, from int, status string) int {
	for i := from; i < len(writes); i++ {
		if writes[i].status == status {
			return i
		}
	}
	return -1
}

// localTicketFake seeds a LOCAL ticket (no backend metadata) over one live phase
// and one live step — the smallest fixture that can show a mapped status reaching
// a descendant.
func localTicketFake(t *testing.T) *fakeGraphCaller {
	t.Helper()
	return &fakeGraphCaller{
		queryResponses: map[string]kgtools.ToolResult{
			"tkt-1": nodeResultJSON(t, "tkt-1", string(kgtypes.NodeTicket), map[string]string{}),
		},
		traversalByRoot: map[string][]*knowledgev1.Node{
			"tkt-1": {
				{Id: "phase-1", Type: string(kgtypes.NodePhase), Status: "active"},
				{Id: "step-1", Type: string(kgtypes.NodeStep), Status: "pending"},
			},
		},
	}
}

// backendTicketFake seeds the same shape on a TRACKER-BACKED ticket. mutateResult
// is set because the backend arm forwards the local half through the same fake and
// reads its body.
func backendTicketFake(t *testing.T) *fakeGraphCaller {
	t.Helper()
	fc := localTicketFake(t)
	fc.queryResponses["tkt-1"] = nodeResultJSON(t, "tkt-1", string(kgtypes.NodeTicket), map[string]string{
		"backend": "linear", "linear_id": "uuid-tkt-1",
	})
	fc.mutateResult = kgtools.ToolResult{Content: []kgtools.ContentBlock{{Type: "text", Text: "ok"}}}
	return fc
}

func cascadeCall(t *testing.T, deps interceptTestDeps, args string) (bool, kgtools.ToolResult) {
	t.Helper()
	return InterceptMutate(opCtx(), deps, kgtools.CallToolParams{
		Name: "mutate", Arguments: json.RawMessage(args),
	})
}

// TestInterceptMutate_TerminalCascade_LocalTicketCanceled_CascadesSkipped is the
// runtime discriminating control the read-only research could not run: a Canceled
// write on a live container must reach its descendants, and today it reaches
// nothing at all.
func TestInterceptMutate_TerminalCascade_LocalTicketCanceled_CascadesSkipped(t *testing.T) {
	fc := localTicketFake(t)
	handled, res := cascadeCall(t, interceptTestDeps{gc: fc},
		`{"operation":"update","id":"tkt-1","status":"Canceled"}`)

	require.True(t, handled, "a terminal status on a local container is claimed by the cascade arm")
	require.False(t, res.IsError, "the cascade must succeed: %s", toolResultText(res))

	writes := statusWrites(fc)
	require.GreaterOrEqual(t, len(writes), 2, "the root and its descendants take different statuses, so two writes")
	assert.Equal(t, "Canceled", writes[0].status, "the root keeps the status the caller sent")
	assert.Equal(t, []string{"tkt-1"}, writes[0].ids)

	i := findWriteAfter(writes, 1, kgtypes.StatusSkipped)
	require.NotEqual(t, -1, i, "no descendant write at skipped; writes were %+v", writes)
	assert.ElementsMatch(t, []string{"phase-1", "step-1"}, writes[i].ids,
		"every live descendant is moved, and only them")
	assert.NotContains(t, writes[i].ids, "tkt-1", "the root does not ride the descendant batch")
}

// TestInterceptMutate_TerminalCascade_LocalTicketDone_CascadesCompleted proves the
// downward vocabulary is a MAP rather than an echo: the root keeps Done and the
// descendants get completed. An implementation that simply wrote a.Status
// downward passes every other test here and fails this one.
func TestInterceptMutate_TerminalCascade_LocalTicketDone_CascadesCompleted(t *testing.T) {
	fc := localTicketFake(t)
	handled, res := cascadeCall(t, interceptTestDeps{gc: fc},
		`{"operation":"update","id":"tkt-1","status":"Done"}`)

	require.True(t, handled)
	require.False(t, res.IsError, "the cascade must succeed: %s", toolResultText(res))

	writes := statusWrites(fc)
	require.GreaterOrEqual(t, len(writes), 2)
	assert.Equal(t, "Done", writes[0].status, "the root keeps the caller's own spelling")
	assert.Equal(t, []string{"tkt-1"}, writes[0].ids)

	i := findWriteAfter(writes, 1, kgtypes.StatusCompleted)
	require.NotEqual(t, -1, i, "no descendant write at completed; writes were %+v", writes)
	assert.ElementsMatch(t, []string{"phase-1", "step-1"}, writes[i].ids)
}

// TestInterceptMutate_TerminalCascade_BackendTicketCanceled_CascadesSkipped closes
// the half of the defect that is invisible locally: a tracker-backed node never
// reached the cascade at any status, so the tracker write and the local forward
// landed while every descendant stayed live.
func TestInterceptMutate_TerminalCascade_BackendTicketCanceled_CascadesSkipped(t *testing.T) {
	fb := &fakeBackend{}
	fc := backendTicketFake(t)
	handled, res := cascadeCall(t, interceptTestDeps{byName: map[string]backends.Backend{"linear": fb}, gc: fc},
		`{"operation":"update","id":"tkt-1","status":"Canceled"}`)

	require.True(t, handled)
	require.False(t, res.IsError, "the backend cascade must succeed: %s", toolResultText(res))
	assert.Equal(t, 1, fb.updateTicketCalls, "the tracker write still fires exactly once, unchanged")

	writes := statusWrites(fc)
	require.GreaterOrEqual(t, len(writes), 2, "the local forward, then the descendant cascade")
	assert.Equal(t, "Canceled", writes[0].status, "the local forward writes the root's own status")
	assert.Equal(t, []string{"tkt-1"}, writes[0].ids)

	i := findWriteAfter(writes, 1, kgtypes.StatusSkipped)
	require.NotEqual(t, -1, i, "no descendant write at skipped; writes were %+v", writes)
	assert.ElementsMatch(t, []string{"phase-1", "step-1"}, writes[i].ids)
	assert.NotContains(t, writes[i].ids, "tkt-1")
}

// TestInterceptMutate_TerminalCascade_BackendTicketCompleted_CascadesCompleted
// exists because the unreachability is total rather than terminal-status-specific:
// the completed rollup has never once fired on a tracker-backed ticket.
func TestInterceptMutate_TerminalCascade_BackendTicketCompleted_CascadesCompleted(t *testing.T) {
	fb := &fakeBackend{}
	fc := backendTicketFake(t)
	handled, res := cascadeCall(t, interceptTestDeps{byName: map[string]backends.Backend{"linear": fb}, gc: fc},
		`{"operation":"update","id":"tkt-1","status":"completed"}`)

	require.True(t, handled)
	require.False(t, res.IsError, "the backend cascade must succeed: %s", toolResultText(res))
	assert.Equal(t, 1, fb.updateTicketCalls)

	writes := statusWrites(fc)
	require.GreaterOrEqual(t, len(writes), 2)
	assert.Equal(t, kgtypes.StatusCompleted, writes[0].status)
	assert.Equal(t, []string{"tkt-1"}, writes[0].ids, "the forward moves the named node only")

	i := findWriteAfter(writes, 1, kgtypes.StatusCompleted)
	require.NotEqual(t, -1, i, "no descendant write at completed; writes were %+v", writes)
	assert.ElementsMatch(t, []string{"phase-1", "step-1"}, writes[i].ids)
	assert.NotContains(t, writes[i].ids, "tkt-1")
}

// TestInterceptMutate_TerminalCascade_ExpandFalse_NoCascade pins the FALSE
// semantics of the opt-out on both dispatch arms. BOTH subtests are
// characterization guards — green before the fix and green after it — and that
// is a measured fact rather than an assumption.
//
// A FALSE BOOLEAN IS INVISIBLE TO SUPPLY DETECTION, so neither arm's param
// accounting ever classifies this payload's flag: suppliedMutateParams filters
// every key through isEmptyJSONValue, whose rule is that null, "", 0, false, {}
// and [] all mean "the caller did not supply this". The tracker-backed arm
// therefore does NOT reject expand_to_descendants:false today, and the call
// succeeds down its default path — which is exactly what false asks for. What
// these subtests guard is that the flag keeps meaning that once the arms cascade.
//
// THE ACCOUNTING MOVE'S CATCHER IS THE SIBLING TEST BELOW, not this one. The
// rejection is live only on the value TRUE, so
// TestInterceptMutate_TerminalCascade_BackendExpandTrue_CascadesNotRejected is
// what goes red when the param is left in the tracker-backed arm's rejected set.
//
// The arm-parity harness catches neither: it documents its own blind spot,
// naming per-arm behaviour tests as what catches an over-broad rejection.
func TestInterceptMutate_TerminalCascade_ExpandFalse_NoCascade(t *testing.T) {
	const args = `{"operation":"update","id":"tkt-1","status":"Canceled","expand_to_descendants":false}`

	t.Run("local", func(t *testing.T) {
		fc := localTicketFake(t)
		_, res := cascadeCall(t, interceptTestDeps{gc: fc}, args)
		require.False(t, res.IsError, "the opt-out is a legal shape: %s", toolResultText(res))
		assert.Zero(t, traversalExecutes(fc), "the opt-out declines before any traversal")
		assert.NotContains(t, writtenIDs(statusWrites(fc)), "step-1",
			"an explicit opt-out must leave every descendant untouched")
	})

	t.Run("backend", func(t *testing.T) {
		fb := &fakeBackend{}
		fc := backendTicketFake(t)
		_, res := cascadeCall(t, interceptTestDeps{byName: map[string]backends.Backend{"linear": fb}, gc: fc}, args)
		require.False(t, res.IsError, "the opt-out is a legal shape: %s", toolResultText(res))
		assert.Zero(t, traversalExecutes(fc), "the opt-out declines before any traversal")
		assert.NotContains(t, writtenIDs(statusWrites(fc)), "step-1",
			"an explicit opt-out must leave every descendant untouched")
	})
}

// TestInterceptMutate_TerminalCascade_BackendExpandTrue_CascadesNotRejected is the
// catcher for the tracker-backed arm's accounting move, and it exists because the
// obvious candidate cannot do the job: the arm's rejection of
// expand_to_descendants fires only on a NON-EMPTY value, so the false-valued
// sibling above is silent about it in both directions.
//
// An explicit opt-IN is the value that reaches the accounting. Today the arm
// declares the param rejected and the call fails pre-write with an error naming
// the field, so a caller asking for the cascade the arm is about to gain is
// refused. Leave the param in the rejected set and this test's error assertion
// goes red; move it to consumed without honoring the flag and the cascade
// assertion goes red. One test, both directions, on the one value the gate can
// see.
func TestInterceptMutate_TerminalCascade_BackendExpandTrue_CascadesNotRejected(t *testing.T) {
	fb := &fakeBackend{}
	fc := backendTicketFake(t)
	_, res := cascadeCall(t, interceptTestDeps{byName: map[string]backends.Backend{"linear": fb}, gc: fc},
		`{"operation":"update","id":"tkt-1","status":"Canceled","expand_to_descendants":true}`)

	require.False(t, res.IsError,
		"an explicit opt-in must not be refused by an arm that now cascades: %s", toolResultText(res))
	assert.Equal(t, 1, fb.updateTicketCalls, "the tracker write still fires exactly once")

	writes := statusWrites(fc)
	i := findWriteAfter(writes, 0, kgtypes.StatusSkipped)
	require.NotEqual(t, -1, i, "no descendant write at skipped; writes were %+v", writes)
	assert.ElementsMatch(t, []string{"phase-1", "step-1"}, writes[i].ids,
		"an explicit opt-in cascades exactly as an absent flag does")
}

// TestInterceptMutate_TerminalCascade_NonTerminalStatus_NoCascade is a
// characterization guard, green before and after. It is what stops a mapping that
// returns a descendant status for every input.
func TestInterceptMutate_TerminalCascade_NonTerminalStatus_NoCascade(t *testing.T) {
	fc := &fakeGraphCaller{
		queryResponses: map[string]kgtools.ToolResult{
			"plan-1": nodeResultJSON(t, "plan-1", string(kgtypes.NodePlan), map[string]string{}),
		},
		traversalByRoot: map[string][]*knowledgev1.Node{
			"plan-1": {{Id: "step-1", Type: string(kgtypes.NodeStep), Status: "pending"}},
		},
	}
	handled, _ := cascadeCall(t, interceptTestDeps{gc: fc},
		`{"operation":"update","id":"plan-1","status":"active"}`)

	assert.False(t, handled, "a live status claims no cascade arm")
	assert.Zero(t, traversalExecutes(fc), "no cascade means no traversal")
}

// TestInterceptMutate_TerminalCascade_EvidenceNodesStayHeld pins In Scope (e)'s
// third clause. The no-Selection half alone would be vacuous against the unfixed
// tree — with no cascade there is no Selection for anything to be absent from —
// so the NAMING half is what makes this discriminate, and it must not be dropped.
func TestInterceptMutate_TerminalCascade_EvidenceNodesStayHeld(t *testing.T) {
	fc := &fakeGraphCaller{
		queryResponses: map[string]kgtools.ToolResult{
			"tkt-1": nodeResultJSON(t, "tkt-1", string(kgtypes.NodeTicket), map[string]string{}),
		},
		traversalByRoot: map[string][]*knowledgev1.Node{
			"tkt-1": {
				{Id: "crit-1", Type: string(kgtypes.NodeCriterion), Status: "pending"},
				{Id: "q-1", Type: string(kgtypes.NodeQuestion), Status: "open"},
				{Id: "find-1", Type: string(kgtypes.NodeFinding), Status: "active"},
			},
		},
	}
	_, res := cascadeCall(t, interceptTestDeps{gc: fc},
		`{"operation":"update","id":"tkt-1","status":"Canceled"}`)
	require.False(t, res.IsError, "the cascade must succeed: %s", toolResultText(res))

	written := writtenIDs(statusWrites(fc))
	body := toolResultText(res)
	for _, id := range []string{"crit-1", "q-1", "find-1"} {
		assert.NotContains(t, written, id, "an evidence node never takes a cascaded status")
		assert.Contains(t, body, id, "a held node the caller is not told about is a silent hold")
	}
}

// TestInterceptMutate_TerminalCascade_SettledDescendantNotOverwritten asserts BOTH
// halves or it proves nothing. The settled half alone passes against the unfixed
// tree, because a tree with no cascade writes no Selection for the settled step to
// be absent from. Paired with the live half it is also the only thing that goes
// red against a fix that widens the claim guard but leaves the partitioner
// skipping on the narrow terminal set — "cancelled" is not a member of that set.
func TestInterceptMutate_TerminalCascade_SettledDescendantNotOverwritten(t *testing.T) {
	fc := &fakeGraphCaller{
		queryResponses: map[string]kgtools.ToolResult{
			"tkt-1": nodeResultJSON(t, "tkt-1", string(kgtypes.NodeTicket), map[string]string{}),
		},
		traversalByRoot: map[string][]*knowledgev1.Node{
			"tkt-1": {
				{Id: "step-live", Type: string(kgtypes.NodeStep), Status: "pending"},
				{Id: "step-dead", Type: string(kgtypes.NodeStep), Status: "cancelled"},
			},
		},
	}
	_, res := cascadeCall(t, interceptTestDeps{gc: fc},
		`{"operation":"update","id":"tkt-1","status":"Canceled"}`)
	require.False(t, res.IsError, "the cascade must succeed: %s", toolResultText(res))

	writes := statusWrites(fc)
	i := findWriteAfter(writes, 0, kgtypes.StatusSkipped)
	require.NotEqual(t, -1, i, "no descendant write at skipped; writes were %+v", writes)
	assert.Contains(t, writes[i].ids, "step-live", "a live descendant is moved")
	assert.NotContains(t, writtenIDs(writes), "step-dead",
		"a status a human set deliberately is never overwritten by a machine one")
}

// TestInterceptMutate_TerminalCascade_UnevaluatedHoldAppliesOnlyToCompleted pins
// the composition of this cascade with the unevaluated-criterion hold. Under a
// completed-family cascade the hold is correct: closing a step above a criterion
// nobody ran claims work that was never done. Under an abandoned-family cascade it
// inverts — the held step stays LIVE under a dead ticket, which is the phantom
// this whole change exists to remove.
//
// The two subtests use disjoint fixture ids so neither can pass by accident of the
// other's setup, and both seed real contains edges so the hold predicate can
// attribute the criterion to the step that owns it.
func TestInterceptMutate_TerminalCascade_UnevaluatedHoldAppliesOnlyToCompleted(t *testing.T) {
	fake := func(root, step, crit string) *fakeGraphCaller {
		return &fakeGraphCaller{
			queryResponses: map[string]kgtools.ToolResult{
				root: nodeResultJSON(t, root, string(kgtypes.NodeTicket), map[string]string{}),
			},
			traversalByRoot: map[string][]*knowledgev1.Node{
				root: {
					{Id: step, Type: string(kgtypes.NodeStep), Status: "pending"},
					{Id: crit, Type: string(kgtypes.NodeCriterion), Status: "pending"},
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

	t.Run("a completed-family cascade holds the step", func(t *testing.T) {
		fc := fake("tkt-done", "step-done", "crit-done")
		_, res := cascadeCall(t, interceptTestDeps{gc: fc},
			`{"operation":"update","id":"tkt-done","status":"Done"}`)
		require.False(t, res.IsError, "the cascade must succeed: %s", toolResultText(res))
		assert.NotContains(t, writtenIDs(statusWrites(fc)), "step-done",
			"completing a step above a criterion nobody ran claims work that was never done")
		assert.Contains(t, toolResultText(res), "step-done", "a hold nobody is told about is a silent hold")
	})

	t.Run("an abandoned-family cascade does not hold the step", func(t *testing.T) {
		fc := fake("tkt-cancel", "step-cancel", "crit-cancel")
		_, res := cascadeCall(t, interceptTestDeps{gc: fc},
			`{"operation":"update","id":"tkt-cancel","status":"Canceled"}`)
		require.False(t, res.IsError, "the cascade must succeed: %s", toolResultText(res))
		writes := statusWrites(fc)
		i := findWriteAfter(writes, 0, kgtypes.StatusSkipped)
		require.NotEqual(t, -1, i, "no descendant write at skipped; writes were %+v", writes)
		assert.Contains(t, writes[i].ids, "step-cancel",
			"skipped claims the work was abandoned, which is true of a live step under a canceled ticket")
	})
}

// TestInterceptMutate_TerminalCascade_RootWrittenDescendantsFailed_NamesBoth covers
// the partial-write state the two-call branch creates: the root write lands and the
// descendant batch fails, so a message that named neither would tell the caller the
// root had not moved when it had.
//
// The ordinal is 2 because the by-id root lookup is a QUERY, not a mutation:
// mutation one is the root write at Done, mutation two is the descendant batch at
// completed. The knob is reused rather than added — it exists precisely because
// "fail the second of two writes" cannot be expressed by the coarser ones.
func TestInterceptMutate_TerminalCascade_RootWrittenDescendantsFailed_NamesBoth(t *testing.T) {
	fc := localTicketFake(t)
	fc.mutateErrOnNth = map[int]error{2: errors.New("descendant write failed")}

	_, res := cascadeCall(t, interceptTestDeps{gc: fc},
		`{"operation":"update","id":"tkt-1","status":"Done"}`)

	require.True(t, res.IsError, "a failed descendant write is a failure, not a silent partial")
	body := toolResultText(res)
	assert.Contains(t, body, "tkt-1", "a refusal the caller cannot attribute to a node is not actionable")
	assert.Contains(t, body, "but not its descendants",
		"the caller must be told the root moved and the descendants did not")
}
