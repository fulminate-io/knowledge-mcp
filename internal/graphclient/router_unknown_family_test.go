// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// retiredFamilyNumber is the wire number a peer built before the transformers
// family was removed still puts on a selector. The proto reserves it, so no
// future family can ever be handed this number and it stays permanently
// unrecognized.
const retiredFamilyNumber = 9

// routerCountingDispatches is routerWithRecorder plus an ATOMIC DISPATCH
// COUNTER on the backend.
//
// The counter is the whole placement claim. Refusing an unrecognized family
// AFTER the wire call satisfies every other assertion these tests make — the
// error is returned, it names the value, nothing is admitted — while doing work
// whose response is then discarded. The zero is the only observable that
// separates boundary validation from post-hoc refusal.
func routerCountingDispatches(t *testing.T) (*Router, *admissionRecorder, *atomic.Int32) {
	t.Helper()
	var dispatches atomic.Int32
	gc := newEngineHarness(t, func(*knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
		dispatches.Add(1)
		return &knowledgev1.ExecuteResponse{}, nil
	})
	r := &Router{local: gc}
	rec := &admissionRecorder{}
	r.AttachWorkingSet(rec.admit)
	return r, rec, &dispatches
}

// unknownFamilyRequest addresses a family this binary does not recognize, while
// ALSO carrying the legacy `graph` string for it. The string is deliberately
// populated: a reader that fell back to it would resolve the request happily,
// so its presence is what makes "the refusal did not read the string" a real
// assertion rather than a vacuous one.
func unknownFamilyRequest() *knowledgev1.ExecuteRequest {
	return &knowledgev1.ExecuteRequest{
		Target: &knowledgev1.GraphSelector{
			Graph:  "transformers",
			Name:   "recipes",
			Family: knowledgev1.GraphFamily(retiredFamilyNumber),
		},
		Plan: &knowledgev1.ExecuteRequest_Mutation{Mutation: &knowledgev1.MutationPlan{}},
	}
}

// TestWorkingSet_UnknownFamilyErrorsBeforeDispatch drives the boundary refusal
// and its control through one Router.
//
// The defect class it detects is a working-set member SILENTLY ABSENT for the
// life of a process: the enum's only production consumer used to answer an
// unrecognized family with a bare return, so the graph that should have been
// admitted was never enrolled — unembedded, uncollected, and green on every
// assertion in the suite. Bad input errors, and it errors at the boundary.
func TestWorkingSet_UnknownFamilyErrorsBeforeDispatch(t *testing.T) {
	t.Parallel()

	r, rec, dispatches := routerCountingDispatches(t)

	_, err := r.Execute(opCtx(), unknownFamilyRequest())

	require.Error(t, err, "a family this binary cannot honor is bad input, not a skipped admission")
	assert.Contains(t, err.Error(), "9",
		"the refusal names the NUMERIC value: an operator cannot act on \"some family\"")
	assert.NotContains(t, strings.ToLower(err.Error()), "transformers",
		"the refusal must NOT quote the legacy graph string — reading it for an unknown family is the defect the enum exists to make loud")
	assert.Empty(t, rec.recorded(), "a refused selector admits nothing")
	assert.Zero(t, dispatches.Load(),
		"THE PLACEMENT LEG: the refusal must fire BEFORE the dispatch, so the backend does no work whose response is then discarded")

	// THE CONTROL — same Router, same recorder, same backend, a RECOGNIZED
	// family. It is the known-positive for BOTH absence readings above: an empty
	// recorder and a zero counter each mean something only because the same
	// instruments move on the next call.
	resp, err := r.Execute(opCtx(), codeMutation("control-repo"))
	require.NoError(t, err, "a recognized family still dispatches")
	require.NotNil(t, resp, "and still returns its response")
	assert.Equal(t, []string{"code/control-repo"}, rec.recorded(),
		"the recorder moves off empty, so the emptiness above was a decision rather than a dead wire")
	assert.Equal(t, int32(1), dispatches.Load(),
		"the counter moves off zero, so the zero above was a decision rather than a counter nobody wired")
}

// TestWorkingSet_NilTargetIsNotAnUnknownFamily is the THIRD INPUT: the
// wrong-but-reasonable implementation an honest engineer writes.
//
// graphsel.InstanceKeyOf reports not-ok for TWO reasons — a nil selector and a
// set-but-unrecognized family — so folding them into one error is the natural
// mistake. A nil Target is the knowledge default (engine.mutateTarget returns
// nil for the all-empty case), which is how almost every knowledge write
// addresses its graph. With the check ahead of the dispatch, that conflation
// would turn ordinary traffic into a hard error BEFORE the wire call — failing
// the common path rather than the defect.
//
// It requires the backend was REACHED as well as that no error came back:
// without the dispatch leg, an implementation that refused the request without
// erroring would still pass.
func TestWorkingSet_NilTargetIsNotAnUnknownFamily(t *testing.T) {
	t.Parallel()

	r, rec, dispatches := routerCountingDispatches(t)

	req := &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Mutation{Mutation: &knowledgev1.MutationPlan{}},
	}
	require.Nil(t, req.GetTarget(), "the fixture is the all-empty knowledge default")

	resp, err := r.Execute(opCtx(), req)

	require.NoError(t, err, "a nil target is the knowledge default, not an unrecognized family")
	require.NotNil(t, resp, "and it is answered by the backend")
	assert.Equal(t, int32(1), dispatches.Load(),
		"the request REACHED the backend: a refusal ahead of the wire would read as a zero here")
	assert.Empty(t, rec.recorded(),
		"a target-less request names no instance to enroll, so it admits nothing without erroring")

	// The empty graph type is what resolveAdmissionTarget returns for this
	// shape, and it is NOT an error — pinned directly so the distinction
	// survives a refactor of Execute.
	gt, name, resolveErr := resolveAdmissionTarget(req)
	require.NoError(t, resolveErr)
	assert.Equal(t, kgtypes.GraphType(""), gt)
	assert.Empty(t, name)
}
