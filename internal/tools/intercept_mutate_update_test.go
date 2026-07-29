// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/backends"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/projects"
)

// compileUpdateForTest compiles a mutate(update) args JSON through the same
// engine.Compile path the generic dispatch + executeMutate use, returning the
// UPDATE MutationPlan so a test can assert the Phase-1 top-level routing.
func compileUpdateForTest(t *testing.T, args string) *knowledgev1.MutationPlan {
	t.Helper()
	req, ok := engine.Compile("mutate", json.RawMessage(args))
	require.True(t, ok, "update args must compile to a MutationPlan")
	m := req.GetMutation()
	require.NotNil(t, m)
	return m
}

// nodeOf builds a *knowledgev1.Node carrying symbol_name + description +
// metadata so the per-type update re-derive can read the effective post-update
// fields off the looked-up node.
func nodeOf(t *testing.T, id, typ, symbolName, description string, metadata map[string]string) *knowledgev1.Node {
	t.Helper()
	return &knowledgev1.Node{
		Id:          id,
		Type:        typ,
		SymbolName:  symbolName,
		Description: description,
		Metadata:    metadata,
	}
}

// lastUpdatePlan returns the most recent UPDATE MutationPlan captured by the fake.
func lastUpdatePlan(t *testing.T, fc *fakeGraphCaller) *knowledgev1.MutationPlan {
	t.Helper()
	require.GreaterOrEqual(t, len(fc.execMutations), 1, "expected at least one forwarded mutation")
	m := fc.execMutations[len(fc.execMutations)-1]
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_UPDATE, m.GetKind())
	return m
}

func runTypedUpdate(t *testing.T, node *knowledgev1.Node, a mutateArgs) (*fakeGraphCaller, bool) {
	t.Helper()
	fc := &fakeGraphCaller{}
	deps := interceptTestDeps{byName: map[string]backends.Backend{}, gc: fc}
	handled, res := handleClientMutateUpdateTyped(context.Background(), deps, a, node)
	if handled {
		require.False(t, res.IsError, "unexpected error result: %s", toolResultText(res))
	}
	return fc, handled
}

// (a) criterion command persists metadata.command; rule scope → metadata.scope;
// finding evidence → metadata.evidence.
func TestTypedUpdate_PerTypeParamsLandInMetadata(t *testing.T) {
	t.Run("criterion command", func(t *testing.T) {
		node := nodeOf(t, "c1", "criterion", "the suite is green", "the suite is green", map[string]string{"type": "manual"})
		fc, handled := runTypedUpdate(t, node, mutateArgs{Operation: "update", ID: "c1", Command: "go test ./..."})
		require.True(t, handled)
		m := lastUpdatePlan(t, fc)
		assert.Equal(t, "go test ./...", m.GetSetMetadata()["command"])
		// command must NOT ride top-level set_fields (clean forward).
		assert.NotContains(t, m.GetSetFields(), "command")
	})
	t.Run("rule scope", func(t *testing.T) {
		node := nodeOf(t, "r1", "rule", "no naked goroutines", "no naked goroutines", nil)
		fc, handled := runTypedUpdate(t, node, mutateArgs{Operation: "update", ID: "r1", Scope: "*.go"})
		require.True(t, handled)
		m := lastUpdatePlan(t, fc)
		assert.Equal(t, "*.go", m.GetSetMetadata()["scope"])
		assert.NotContains(t, m.GetSetFields(), "scope")
	})
	t.Run("finding evidence", func(t *testing.T) {
		node := nodeOf(t, "f1", "finding", "leak", "leak in handler", nil)
		fc, handled := runTypedUpdate(t, node, mutateArgs{Operation: "update", ID: "f1", Evidence: "store.go:42"})
		require.True(t, handled)
		m := lastUpdatePlan(t, fc)
		assert.Equal(t, "store.go:42", m.GetSetMetadata()["evidence"])
		assert.NotContains(t, m.GetSetFields(), "evidence")
	})
}

// (T2-2) finding source persists metadata.source and does NOT write the node
// Source field; a research (non-finding) update's source routes to the node
// field (top-level set_fields.source via Phase 1).
func TestTypedUpdate_FindingSourceInMetadata_ResearchSourceToField(t *testing.T) {
	t.Run("finding source → metadata.source, not node field", func(t *testing.T) {
		node := nodeOf(t, "f1", "finding", "leak", "leak in handler", nil)
		fc, handled := runTypedUpdate(t, node, mutateArgs{Operation: "update", ID: "f1", Source: "manual"})
		require.True(t, handled)
		m := lastUpdatePlan(t, fc)
		assert.Equal(t, "manual", m.GetSetMetadata()["source"])
		assert.NotContains(t, m.GetSetFields(), "source", "finding source must NOT route to the node Source field")
	})
	t.Run("research source → node field via Phase-1 generic dispatch (typed router declines)", func(t *testing.T) {
		// The typed router does NOT claim a research+source update — source routes
		// top-level via the Phase-1 updateSetFields widening (generic engine
		// dispatch), landing in the node Source field. So the typed handler returns
		// (false,_) and the generic path owns it.
		node := nodeOf(t, "rs1", "research", "Why slow?", "Why slow?", nil)
		_, handled := runTypedUpdate(t, node, mutateArgs{Operation: "update", ID: "rs1", Source: "git"})
		assert.False(t, handled, "research+source must fall through to the Phase-1 generic dispatch")

		// And the generic compile routes source into top-level set_fields (the node
		// Source field), NOT metadata — the Phase-1 contract.
		m := compileUpdateForTest(t, `{"operation":"update","id":"rs1","source":"git"}`)
		assert.Equal(t, "git", m.GetSetFields()["source"], "research source routes to the node field")
		assert.NotContains(t, m.GetSetMetadata(), "source")
	})
}

// (c) re-derived-when-absent: a criterion update changing command WITHOUT
// summary forwards set_fields.summary == DeriveCriterionSummary(effectiveType,
// effectiveDescription, newCommand).
func TestTypedUpdate_RederiveSummaryWhenAbsent(t *testing.T) {
	node := nodeOf(t, "c1", "criterion", "the suite is green", "the suite is green", map[string]string{"type": "automated"})
	fc, handled := runTypedUpdate(t, node, mutateArgs{Operation: "update", ID: "c1", Command: "go test ./..."})
	require.True(t, handled)
	m := lastUpdatePlan(t, fc)
	want := projects.DeriveCriterionSummary("automated", "the suite is green", "go test ./...")
	assert.Equal(t, want, m.GetSetFields()["summary"])
}

// (c) caller-wins: a criterion update changing command AND passing summary
// forwards the caller's summary verbatim (no re-derivation).
func TestTypedUpdate_CallerSummaryWins(t *testing.T) {
	node := nodeOf(t, "c1", "criterion", "desc", "desc", map[string]string{"type": "manual"})
	fc, handled := runTypedUpdate(t, node, mutateArgs{Operation: "update", ID: "c1", Command: "make x", Summary: "my explicit summary"})
	require.True(t, handled)
	m := lastUpdatePlan(t, fc)
	assert.Equal(t, "my explicit summary", m.GetSetFields()["summary"])
}

// (d) a criterion update changing description forwards set_fields.name == new
// description; a finding/rule update changing description does NOT re-stamp name.
func TestTypedUpdate_CriterionRestampNameFromDescription(t *testing.T) {
	t.Run("criterion re-stamps name=description", func(t *testing.T) {
		node := nodeOf(t, "c1", "criterion", "old desc", "old desc", map[string]string{"type": "manual"})
		fc, handled := runTypedUpdate(t, node, mutateArgs{Operation: "update", ID: "c1", Description: "new desc"})
		require.True(t, handled)
		m := lastUpdatePlan(t, fc)
		assert.Equal(t, "new desc", m.GetSetFields()["name"])
	})
	t.Run("finding does NOT re-stamp name", func(t *testing.T) {
		node := nodeOf(t, "f1", "finding", "old name", "old desc", nil)
		fc, handled := runTypedUpdate(t, node, mutateArgs{Operation: "update", ID: "f1", Description: "new desc"})
		require.True(t, handled)
		m := lastUpdatePlan(t, fc)
		assert.NotContains(t, m.GetSetFields(), "name", "finding update must not re-stamp name")
	})
	t.Run("rule does NOT re-stamp name", func(t *testing.T) {
		node := nodeOf(t, "r1", "rule", "old name", "old desc", nil)
		fc, handled := runTypedUpdate(t, node, mutateArgs{Operation: "update", ID: "r1", Description: "new desc"})
		require.True(t, handled)
		m := lastUpdatePlan(t, fc)
		assert.NotContains(t, m.GetSetFields(), "name", "rule update must not re-stamp name")
	})
}

// (T2-1) positive-pass: a criterion update carrying summary + metadata + format
// + status SUCCEEDS and forwards (does not reject).
func TestTypedUpdate_PositivePass_RoutableParamsSucceed(t *testing.T) {
	node := nodeOf(t, "c1", "criterion", "desc", "desc", map[string]string{"type": "manual"})
	a := mutateArgs{
		Operation: "update",
		ID:        "c1",
		Summary:   "explicit",
		Metadata:  map[string]string{"k": "v"},
		Format:    "json",
		Status:    "completed",
	}
	fc, handled := runTypedUpdate(t, node, a)
	require.True(t, handled)
	m := lastUpdatePlan(t, fc)
	assert.Equal(t, "completed", m.GetSetFields()["status"])
	assert.Equal(t, "explicit", m.GetSetFields()["summary"])
	assert.Equal(t, "v", m.GetSetMetadata()["k"])
}

// (b) An update with a param unroutable for the type returns a structured error
// naming the offending param AND leaves the node byte-identical (ZERO forwarded
// mutations — the reject returns before any executeMutate).
func TestTypedUpdate_RejectsUnroutableParam(t *testing.T) {
	cases := []struct {
		name      string
		node      *knowledgev1.Node
		args      mutateArgs
		wantParam string
		wantType  string
	}{
		{
			name:      "finding carrying scope",
			node:      nodeOf(t, "f1", "finding", "leak", "leak", nil),
			args:      mutateArgs{Operation: "update", ID: "f1", Scope: "*.go"},
			wantParam: "scope",
			wantType:  "finding",
		},
		{
			name:      "rule carrying command",
			node:      nodeOf(t, "r1", "rule", "rule", "rule", nil),
			args:      mutateArgs{Operation: "update", ID: "r1", Command: "go test"},
			wantParam: "command",
			wantType:  "rule",
		},
		{
			name:      "criterion carrying scope",
			node:      nodeOf(t, "c1", "criterion", "c", "c", map[string]string{"type": "manual"}),
			args:      mutateArgs{Operation: "update", ID: "c1", Scope: "*.go"},
			wantParam: "scope",
			wantType:  "criterion",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := &fakeGraphCaller{}
			deps := interceptTestDeps{byName: map[string]backends.Backend{}, gc: fc}
			handled, res := handleClientMutateUpdateTyped(context.Background(), deps, tc.args, tc.node)
			require.True(t, handled, "an unroutable param must be claimed (and rejected)")
			require.True(t, res.IsError, "expected a structured rejection error")
			body := toolResultText(res)
			assert.Contains(t, body, tc.wantParam, "error names the offending param")
			assert.Contains(t, body, tc.wantType, "error names the node type")
			assert.Empty(t, fc.execMutations, "reject must issue ZERO writes (node byte-identical)")
		})
	}
}

// (65afcbc0) The per-type router claims only non-backend non-rollup typed
// updates: a status=completed plan rollup still fires its rollup arm, and a
// backend-backed update still forwards through Linear — both UNCHANGED.
func TestInterceptMutate_TypedRouter_AfterBackendAndRollup(t *testing.T) {
	t.Run("criterion command routes through the typed claim via InterceptMutate", func(t *testing.T) {
		body, err := json.Marshal(map[string]any{
			"id": "c1", "type": "criterion", "symbol_name": "c", "description": "c",
			"metadata": map[string]string{"type": "manual"},
		})
		require.NoError(t, err)
		fc := &fakeGraphCaller{queryResponses: map[string]kgtools.ToolResult{
			"c1": {Content: []kgtools.ContentBlock{{Type: "text", Text: string(body)}}},
		}}
		deps := interceptTestDeps{byName: map[string]backends.Backend{}, gc: fc}
		handled, res := InterceptMutate(opCtx(), deps, kgtools.CallToolParams{
			Name:      "mutate",
			Arguments: json.RawMessage(`{"operation":"update","id":"c1","command":"go test ./..."}`),
		})
		require.True(t, handled, "typed criterion update must be claimed")
		require.False(t, res.IsError, "expected success: %s", toolResultText(res))
		require.GreaterOrEqual(t, len(fc.execMutations), 1)
		m := fc.execMutations[len(fc.execMutations)-1]
		assert.Equal(t, "go test ./...", m.GetSetMetadata()["command"])
	})

	t.Run("plain ticket name update still falls through (router declines)", func(t *testing.T) {
		fc := &fakeGraphCaller{queryResponses: map[string]kgtools.ToolResult{
			"t1": nodeResultJSON(t, "t1", "ticket", map[string]string{}),
		}}
		deps := interceptTestDeps{byName: map[string]backends.Backend{"linear": &fakeBackend{}}, gc: fc}
		handled, _ := InterceptMutate(opCtx(), deps, kgtools.CallToolParams{
			Name:      "mutate",
			Arguments: json.RawMessage(`{"operation":"update","id":"t1","name":"renamed"}`),
		})
		assert.False(t, handled, "a plain typed update with no per-type param must fall through")
	})
}
