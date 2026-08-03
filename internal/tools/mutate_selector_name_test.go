// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
)

// mutate_selector_name_test.go covers the CLIENT-PRODUCER half of the node-name/
// graph-selector conflation: the engine's compile arms no longer copy `name` into
// GraphSelector.Name (compile_mutate_selector_test.go pins that), and these tests
// prove the real intercept forwards — several of which SYNTHESIZE a name the
// caller never typed — reach those arms and land on a selector-free Target.
//
// The rows here are the shapes measured failing against the live daemon once the
// per-family selector reject went in: a description-only update on a criterion
// (whose name is DERIVED from the description, so a caller who passed no name
// still forwards one) and a log-backend upsert (which passes the backend's
// display name). Both returned "graph=knowledge holds ONE graph: name= is a
// label, not a selector".

// lastExecTarget returns the Target of the most recent ExecuteRequest the fake
// captured. A nil Target is the expected shape for a bare knowledge write, so the
// helper reads through the proto getter rather than requiring non-nil.
func lastExecTarget(t *testing.T, fc *fakeGraphCaller) *knowledgev1.GraphSelector {
	t.Helper()
	require.GreaterOrEqual(t, len(fc.execRequests), 1, "expected at least one forwarded Execute")
	return fc.execRequests[len(fc.execRequests)-1].GetTarget()
}

// TestTypedUpdate_DerivedCriterionNameStaysOffTheGraphSelector is the live repro
// row: mutate(update, id, description) on a criterion. The typed router derives
// name=description (the Name==Description convention), so the forward carries a
// name even though the caller passed none — and that derived name used to become
// the requested graph instance.
func TestTypedUpdate_DerivedCriterionNameStaysOffTheGraphSelector(t *testing.T) {
	node := nodeOf(t, "c1", "criterion", "the suite is green", "the suite is green",
		map[string]string{"type": "manual"})
	fc, handled := runTypedUpdate(t, node, mutateArgs{
		Operation:   "update",
		ID:          "c1",
		Description: "the suite is green and the lint gate passes",
	})
	require.True(t, handled)

	m := lastUpdatePlan(t, fc)
	assert.Equal(t, "the suite is green and the lint gate passes", m.GetSetFields()["name"],
		"the derived name must still reach the NODE — this moves it off the selector, not out of the write")
	assert.Empty(t, lastExecTarget(t, fc).GetName(),
		"the derived node name must not become the requested graph instance")
}

// TestTypedUpdate_SuppliedNameStaysOffTheGraphSelector covers the same seam for a
// type whose name the caller supplies directly (a rule keeps its own name, where a
// criterion's is derived), so the pin is not specific to the derive path.
func TestTypedUpdate_SuppliedNameStaysOffTheGraphSelector(t *testing.T) {
	node := nodeOf(t, "r1", "rule", "no naked goroutines", "no naked goroutines", nil)
	fc, handled := runTypedUpdate(t, node, mutateArgs{
		Operation: "update",
		ID:        "r1",
		Name:      "no unsupervised goroutines",
		Scope:     "*.go",
	})
	require.True(t, handled)

	m := lastUpdatePlan(t, fc)
	assert.Equal(t, "no unsupervised goroutines", m.GetSetFields()["name"])
	assert.Empty(t, lastExecTarget(t, fc).GetName())
}

// TestUpsertLogBackend_NameStaysOffTheGraphSelector covers the upsert arm through
// its real producer: configure_log_backend sends the backend's display name as the
// node name on every call, so this path was broken for every log-backend write.
func TestUpsertLogBackend_NameStaysOffTheGraphSelector(t *testing.T) {
	fc := &fakeGraphCaller{}
	res := upsertLogBackend(context.Background(), fc, "log_backend:prod-loki", manageArgs{
		Name:     "prod-loki",
		Provider: "loki",
		URL:      "https://logs.example.internal",
		AuthType: "bearer",
	}, true)
	require.False(t, res.IsError, "upsert must succeed: %s", toolResultText(res))

	require.GreaterOrEqual(t, len(fc.execMutations), 1)
	last := fc.execMutations[len(fc.execMutations)-1]
	require.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_UPSERT, last.GetKind())
	require.Len(t, last.GetNodeBodies(), 1)
	assert.Equal(t, "prod-loki", last.GetNodeBodies()[0].GetName(),
		"the backend name still rides the node body")
	assert.Empty(t, lastExecTarget(t, fc).GetName(),
		"the backend's display name must not become the requested graph instance")
}

// TestBackendBackedUpdate_NameStaysOffTheGraphSelector covers the tracker-backed
// forward (marshalForwardedMutateUpdateArgs), the third client producer that puts
// a node name on the wire — a Linear-backed ticket rename.
func TestBackendBackedUpdate_NameStaysOffTheGraphSelector(t *testing.T) {
	args := marshalForwardedMutateUpdateArgs(mutateArgs{
		Operation:   "update",
		ID:          "t1",
		Name:        "tracker-backed selector probe",
		Description: "d",
	}, "linear")

	req, ok := engine.Compile("mutate", args)
	require.True(t, ok, "the tracker-backed forward must compile to a MutationPlan")
	assert.Equal(t, "tracker-backed selector probe", req.GetMutation().GetSetFields()["name"],
		"the ticket name still reaches the node")
	assert.Empty(t, req.GetTarget().GetName(),
		"a tracker-backed node's name must not become the requested graph instance")
}
