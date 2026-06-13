// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	clientthought "github.com/fulminate-io/knowledge-mcp/internal/thought"
)

// recordingPropagateCaller is a minimal GraphCaller that flags whether Execute
// was reached. handlePropagateClient → clientthought.RunPropagation drains the
// corpus through Execute, so executed==false proves RunPropagation never ran.
type recordingPropagateCaller struct {
	executed atomic.Bool
}

func (r *recordingPropagateCaller) Execute(_ context.Context, _ *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	r.executed.Store(true)
	return &knowledgev1.ExecuteResponse{}, nil
}

// TestPropagateCoalesce asserts the manual propagate tool coalesces with an
// in-flight reflection pass: with the per-account guard pre-claimed,
// handlePropagateClient must return the coalesce textResult WITHOUT calling
// RunPropagation (no Execute on the caller). Fails-when-absent the manual-tool
// guard — without it the tool would drive a second concurrent full recompute.
func TestPropagateCoalesce(t *testing.T) {
	// Pre-claim the SAME exported key the loop uses, simulating an in-flight pass.
	release, ok := clientthought.AcquireReflectionPass(clientthought.ReflectionPassKey)
	require.True(t, ok, "test must win the first claim")
	defer release()

	rec := &recordingPropagateCaller{}
	deps := interceptTestDeps{gc: rec}

	res := handlePropagateClient(context.Background(), deps, kgtools.CallToolParams{})

	require.False(t, res.IsError, "coalesce returns a normal textResult, not an error: %s", toolResultText(res))
	assert.False(t, rec.executed.Load(),
		"on coalesce the manual tool must NOT call RunPropagation (no Execute drain)")
	assert.Contains(t, toolResultText(res), "already in progress",
		"the coalesce response names the in-flight pass")
}

// fakeReflectionForcer records ForceFullPass invocations and returns a scripted
// (result, err) pair. It is the proof surface for the force_full routing tests:
// called==true proves thoughts(propagate, force_full:true) reached the lever.
type fakeReflectionForcer struct {
	called atomic.Bool
	result clientthought.PropagationResult
	err    error
}

func (f *fakeReflectionForcer) ForceFullPass(_ context.Context) (clientthought.PropagationResult, error) {
	f.called.Store(true)
	return f.result, f.err
}

// propagateForceFullParams marshals {operation:"propagate", force_full:true}.
func propagateForceFullParams(t *testing.T) kgtools.CallToolParams {
	t.Helper()
	return kgtools.CallToolParams{
		Name:      "thoughts",
		Arguments: marshalOrEmpty(map[string]any{"operation": "propagate", "force_full": true}),
	}
}

// propagateParamsWithForceFull marshals {operation:"propagate", force_full:<v>} for
// an ARBITRARY force_full value — used to drive the string-coercion cases (v is a
// JSON string "true"/"false", a bool, or garbage) that exercise the flexBool decode.
func propagateParamsWithForceFull(t *testing.T, v any) kgtools.CallToolParams {
	t.Helper()
	return kgtools.CallToolParams{
		Name:      "thoughts",
		Arguments: marshalOrEmpty(map[string]any{"operation": "propagate", "force_full": v}),
	}
}

// TestPropagateForceFull_RoutesToForcer (FAILS-WHEN-ABSENT) proves force_full:true
// drives the ReflectionForcer (the live PropagationLoop) — NOT the incremental
// RunPropagation path. The fake forcer records the call; the incremental path's
// GraphCaller.Execute must NOT be reached. Without the force_full routing the tool
// would run the ordinary incremental propagate and never touch the lever.
func TestPropagateForceFull_RoutesToForcer(t *testing.T) {
	forcer := &fakeReflectionForcer{result: clientthought.PropagationResult{ThoughtsProcessed: 7, Components: 2}}
	rec := &recordingPropagateCaller{}
	deps := interceptTestDeps{gc: rec, forcer: forcer}

	res := handlePropagateClient(context.Background(), deps, propagateForceFullParams(t))

	require.False(t, res.IsError, "a successful forced pass is not an error: %s", toolResultText(res))
	assert.True(t, forcer.called.Load(), "force_full:true must drive ForceFullPass on the ReflectionForcer")
	assert.False(t, rec.executed.Load(),
		"force_full:true must NOT run the incremental RunPropagation path (no Execute drain on the GraphCaller)")
	assert.Contains(t, toolResultText(res), "Forced full backstop pass complete",
		"the forced-pass response is rendered from the lever's result")
}

// TestPropagateForceFull_NilForcerErrorsLoudly (FAILS-WHEN-ABSENT) proves that with
// force_full:true and NO reflection loop running in this process (nil forcer), the
// tool returns a LOUD error rather than silently falling through to the incremental
// path. A silent fallthrough would mislead an operator into believing a full
// backstop pass ran when none could.
func TestPropagateForceFull_NilForcerErrorsLoudly(t *testing.T) {
	rec := &recordingPropagateCaller{}
	deps := interceptTestDeps{gc: rec, forcer: nil} // no reflection loop in this process

	res := handlePropagateClient(context.Background(), deps, propagateForceFullParams(t))

	require.True(t, res.IsError, "a nil forcer on force_full:true must be a loud error, not a silent fallthrough")
	assert.False(t, rec.executed.Load(),
		"the nil-forcer error must NOT fall through to the incremental RunPropagation path")
	assert.Contains(t, toolResultText(res), "reflection loop not running",
		"the error names the missing reflection loop")
}

// TestPropagate_NotReadyGate (FAILS-WHEN-ABSENT) proves the bind-first
// wiring-window gate (bind-first startup): with PropReady()=false, BOTH force_full and
// similarity return the uniform "daemon still starting" error and do NOT dispatch
// — even with a live forcer present (the readiness check fires BEFORE the
// nil-forcer check). The fails-when-absent property: drop the gate and force_full
// reaches the (live) forcer instead of returning the not-ready error. The
// incremental propagate path (no force_full/similarity) is UNGATED and must NOT
// emit the not-ready message.
func TestPropagate_NotReadyGate(t *testing.T) {
	t.Run("force_full", func(t *testing.T) {
		forcer := &fakeReflectionForcer{}
		rec := &recordingPropagateCaller{}
		deps := interceptTestDeps{gc: rec, forcer: forcer, propNotReady: true}
		res := handlePropagateClient(context.Background(), deps, propagateForceFullParams(t))
		require.True(t, res.IsError)
		assert.Contains(t, toolResultText(res), "daemon still starting")
		assert.False(t, forcer.called.Load(), "must not dispatch to the forcer when not ready")
		assert.False(t, rec.executed.Load(), "must not fall through to incremental RunPropagation")
	})
	t.Run("similarity", func(t *testing.T) {
		deps := similarityDispatchDeps{forcer: &fakeSimilarityForcer{}, propNotReady: true}
		res := callPropagate(deps, `{"similarity":true}`)
		require.True(t, res.IsError)
		assert.Contains(t, toolResultText(res), "daemon still starting")
	})
	t.Run("incremental-ungated", func(t *testing.T) {
		rec := &recordingPropagateCaller{}
		deps := interceptTestDeps{gc: rec, propNotReady: true}
		res := handlePropagateClient(context.Background(), deps, kgtools.CallToolParams{
			Name:      "thoughts",
			Arguments: marshalOrEmpty(map[string]any{"operation": "propagate"}),
		})
		assert.NotContains(t, toolResultText(res), "daemon still starting",
			"plain incremental propagate is UNGATED — it must not emit the not-ready message")
	})
}

// TestPropagateForceFull_CoalesceSurfacesAbsorbed proves a force_full request that
// the lever absorbs onto an in-flight pass (ErrReflectionInFlight) surfaces as a
// benign textResult, not an error.
func TestPropagateForceFull_CoalesceSurfacesAbsorbed(t *testing.T) {
	forcer := &fakeReflectionForcer{err: clientthought.ErrReflectionInFlight}
	deps := interceptTestDeps{gc: &recordingPropagateCaller{}, forcer: forcer}

	res := handlePropagateClient(context.Background(), deps, propagateForceFullParams(t))

	require.False(t, res.IsError, "a coalesce is a benign textResult, not an error: %s", toolResultText(res))
	assert.True(t, forcer.called.Load(), "the lever was invoked and reported the in-flight coalesce")
	assert.Contains(t, toolResultText(res), "already in progress",
		"the coalesce response names the in-flight pass")
}

// TestPropagate_DefaultPathUnchanged proves force_full absent leaves the existing
// incremental path byte-identical: it drains the corpus via RunPropagation (Execute
// reached) and never touches the ReflectionForcer.
func TestPropagate_DefaultPathUnchanged(t *testing.T) {
	forcer := &fakeReflectionForcer{}
	rec := &recordingPropagateCaller{}
	deps := interceptTestDeps{gc: rec, forcer: forcer}

	// No force_full field → the default incremental path.
	res := handlePropagateClient(context.Background(), deps, kgtools.CallToolParams{
		Name:      "thoughts",
		Arguments: marshalOrEmpty(map[string]any{"operation": "propagate"}),
	})

	require.False(t, res.IsError, "default propagate must not error: %s", toolResultText(res))
	assert.True(t, rec.executed.Load(), "the default path runs the incremental RunPropagation (Execute drain)")
	assert.False(t, forcer.called.Load(), "the default path must NOT touch the ReflectionForcer")
}

// TestPropagateForceFull_StringTrueRoutesToForcer (FAILS-WHEN-ABSENT) is the bug
// regression: a caller sending force_full as the STRING "true" (stale caller schemas
// coerce unknown params to strings; LLM callers routinely type "true") must route to
// the forcer, NOT silently fall through to the incremental path. Before the flexBool
// fix the string failed to decode into a plain bool field and the handler ran a
// plain incremental pass with no warning.
func TestPropagateForceFull_StringTrueRoutesToForcer(t *testing.T) {
	forcer := &fakeReflectionForcer{result: clientthought.PropagationResult{ThoughtsProcessed: 3}}
	rec := &recordingPropagateCaller{}
	deps := interceptTestDeps{gc: rec, forcer: forcer}

	res := handlePropagateClient(context.Background(), deps, propagateParamsWithForceFull(t, "true"))

	require.False(t, res.IsError, "force_full:\"true\" (string) is a valid forced pass, not an error: %s", toolResultText(res))
	assert.True(t, forcer.called.Load(), "force_full:\"true\" (string) must route to the forcer, not the incremental path")
	assert.False(t, rec.executed.Load(),
		"the string-true forced pass must NOT drain the incremental RunPropagation path")
}

// TestPropagateForceFull_StringFalseTakesDefault proves force_full:"false" (string)
// is honored as false and takes the incremental default path — the string coercion
// is symmetric, not a one-way "any string means force".
func TestPropagateForceFull_StringFalseTakesDefault(t *testing.T) {
	forcer := &fakeReflectionForcer{}
	rec := &recordingPropagateCaller{}
	deps := interceptTestDeps{gc: rec, forcer: forcer}

	res := handlePropagateClient(context.Background(), deps, propagateParamsWithForceFull(t, "false"))

	require.False(t, res.IsError, "force_full:\"false\" must take the default path, not error: %s", toolResultText(res))
	assert.True(t, rec.executed.Load(), "force_full:\"false\" runs the incremental path (Execute drain)")
	assert.False(t, forcer.called.Load(), "force_full:\"false\" must NOT touch the forcer")
}

// TestPropagateForceFull_GarbageErrorsLoudly (FAILS-WHEN-ABSENT) proves a force_full
// value flexBool cannot read as a bool (e.g. "maybe") is surfaced as a LOUD error,
// never swallowed into the default path. Silent degradation on a malformed arg is
// the exact failure class this fix guards against — the handler must NOT run a pass
// it cannot interpret the operator's intent for.
func TestPropagateForceFull_GarbageErrorsLoudly(t *testing.T) {
	forcer := &fakeReflectionForcer{}
	rec := &recordingPropagateCaller{}
	deps := interceptTestDeps{gc: rec, forcer: forcer}

	res := handlePropagateClient(context.Background(), deps, propagateParamsWithForceFull(t, "maybe"))

	require.True(t, res.IsError, "a garbage force_full must be a loud error, not a silent default")
	assert.False(t, rec.executed.Load(), "the garbage-arg error must NOT fall through to the incremental path")
	assert.False(t, forcer.called.Load(), "the garbage-arg error must NOT touch the forcer")
	assert.Contains(t, toolResultText(res), "force_full",
		"the error names the offending parameter")
}
