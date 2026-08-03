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

// typedUpdateRaw rebuilds the caller payload for a direct typed-update call. The
// wire mirror's omitempty tags make the marshal of the test's own literal carry
// exactly the fields it populated, which is the payload InterceptMutate would
// have seeded on the production path. This lives in the test only — a production
// helper synthesizing raw from the struct would reintroduce the absent-versus-
// empty collision the raw carrier exists to remove.
func typedUpdateRaw(t *testing.T, a mutateArgs) string {
	t.Helper()
	raw, err := json.Marshal(a)
	require.NoError(t, err)
	return string(raw)
}

func runTypedUpdate(t *testing.T, node *knowledgev1.Node, a mutateArgs) (*fakeGraphCaller, bool) {
	t.Helper()
	fc := &fakeGraphCaller{}
	deps := interceptTestDeps{byName: map[string]backends.Backend{}, gc: fc}
	handled, res := handleClientMutateUpdateTyped(context.Background(), deps, withRawArgs(a, typedUpdateRaw(t, a)), node)
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
			handled, res := handleClientMutateUpdateTyped(context.Background(), deps,
				withRawArgs(tc.args, typedUpdateRaw(t, tc.args)), tc.node)
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

// TestMutateUpdateTyped_CriterionNameRejected pins Gate 14: a criterion update
// carrying BOTH name and description must be REJECTED naming `name`, not
// silently resolved in favor of the description.
//
// The discard itself was deliberate — a criterion's name is DERIVED from its
// description (the Name==Description convention upsertCriterionNode
// establishes) — but it was silent, so a caller supplying both got a node that
// disagreed with the call and no signal. The create arm already answers this
// question by rejecting `name` on a criterion create; update must agree, or the
// two paths disagree about whether a criterion may carry an independent name.
//
// Zero forwards is load-bearing: a rejected update leaves the node
// byte-identical.
func TestMutateUpdateTyped_CriterionNameRejected(t *testing.T) {
	node := nodeOf(t, "c1", "criterion", "old desc", "old desc", map[string]string{"type": "manual"})
	a := mutateArgs{
		Operation:   "update",
		ID:          "c1",
		Name:        "short name",
		Description: "the long observable check",
	}
	fc := &fakeGraphCaller{}
	deps := interceptTestDeps{byName: map[string]backends.Backend{}, gc: fc}
	handled, res := handleClientMutateUpdateTyped(context.Background(), deps,
		withRawArgs(a, typedUpdateRaw(t, a)), node)

	require.True(t, handled, "the rejection is a claim — the call must not fall through silently")
	require.True(t, res.IsError, "a criterion update carrying name must reject: %s", toolResultText(res))
	assert.Contains(t, toolResultText(res), "name", "the rejection must NAME the offending param")
	assert.Empty(t, fc.execMutations, "a rejected update issues ZERO forwards")
}

// TestMutateUpdateTyped_RuleNameApplied is the SCOPE FENCE for the rejection
// above: it proves the new rule is criterion-scoped and not a blanket name ban.
// A rule update carrying name and description together forwards the name
// VERBATIM — rule updates are field-independent and always were, so this
// exercises behavior that already works.
func TestMutateUpdateTyped_RuleNameApplied(t *testing.T) {
	node := nodeOf(t, "r1", "rule", "old name", "old desc", nil)
	fc, handled := runTypedUpdate(t, node, mutateArgs{
		Operation:   "update",
		ID:          "r1",
		Name:        "short name",
		Description: "the long rule text",
	})
	require.True(t, handled)
	m := lastUpdatePlan(t, fc)
	assert.Equal(t, "short name", m.GetSetFields()["name"],
		"a rule update forwards the caller's name verbatim, never the description")
	assert.Equal(t, "the long rule text", m.GetSetFields()["description"])
}

// TestMutateUpdateTyped_SiblingFieldsPassThrough discharges the ticket's
// sibling-pair audit as an executable assertion rather than prose: name,
// summary, content and keywords each ride the forward under their OWN key with
// no cross-write.
//
// The four sentinel values are deliberately DISTINCT — a fixture reusing one
// string cannot tell a pass-through from a cross-write. `name` was the only
// collision; summary already implements caller-wins (the re-derive runs only
// when the caller passed none) and content and keywords are pure pass-through.
func TestMutateUpdateTyped_SiblingFieldsPassThrough(t *testing.T) {
	node := nodeOf(t, "r1", "rule", "old name", "old desc", nil)
	a := mutateArgs{
		Operation: "update",
		ID:        "r1",
		Name:      "sentinel-name",
		Summary:   "sentinel-summary",
		Content:   "sentinel-content",
		Keywords:  "sentinel-keywords",
		Scope:     "*.go",
	}
	fc, handled := runTypedUpdate(t, node, a)
	require.True(t, handled)
	m := lastUpdatePlan(t, fc)

	fields := map[string]string{
		"name":     "sentinel-name",
		"summary":  "sentinel-summary",
		"content":  "sentinel-content",
		"keywords": "sentinel-keywords",
	}
	for key, want := range fields {
		assert.Equalf(t, want, m.GetSetFields()[key], "%s must ride the forward under its own key", key)
		for otherKey := range fields {
			if otherKey == key {
				continue
			}
			assert.NotEqualf(t, want, m.GetSetFields()[otherKey],
				"%s's value leaked into %s — a cross-write", key, otherKey)
		}
	}
}

// TestMutateUpdate_StatusClearToBlank_TypedAndFallthrough pins Gate 19 on both
// LOCAL paths: an explicit status:"" is a clear-to-blank WRITE and must reach
// the forward as an empty value, while an absent status must leave the key out
// entirely.
//
// The absent case is the guard, not decoration: without it a fix that always
// emits status would clear the status of every node updated for any other
// reason.
func TestMutateUpdate_StatusClearToBlank_TypedAndFallthrough(t *testing.T) {
	t.Run("typed router: explicit blank forwards status=\"\"", func(t *testing.T) {
		node := nodeOf(t, "c1", "criterion", "desc", "desc", map[string]string{"type": "manual"})
		a := mutateArgs{Operation: "update", ID: "c1", Command: "go test ./..."}
		fc := &fakeGraphCaller{}
		deps := interceptTestDeps{byName: map[string]backends.Backend{}, gc: fc}
		handled, res := handleClientMutateUpdateTyped(context.Background(), deps,
			withRawArgs(a, `{"operation":"update","id":"c1","command":"go test ./...","status":""}`), node)
		require.True(t, handled)
		require.False(t, res.IsError, "unexpected error: %s", toolResultText(res))
		m := lastUpdatePlan(t, fc)
		require.Contains(t, m.GetSetFields(), "status",
			"an explicit blank status must reach the forward — it is a clear-to-blank request")
		assert.Empty(t, m.GetSetFields()["status"])
	})

	t.Run("typed router: absent status omits the key", func(t *testing.T) {
		node := nodeOf(t, "c1", "criterion", "desc", "desc", map[string]string{"type": "manual"})
		fc, handled := runTypedUpdate(t, node, mutateArgs{Operation: "update", ID: "c1", Command: "go test ./..."})
		require.True(t, handled)
		m := lastUpdatePlan(t, fc)
		assert.NotContains(t, m.GetSetFields(), "status",
			"an update that never mentions status must leave it untouched")
	})

	t.Run("generic single-id path: explicit blank compiles to status=\"\"", func(t *testing.T) {
		m := compileUpdateForTest(t, `{"operation":"update","id":"n1","status":""}`)
		require.Contains(t, m.GetSetFields(), "status")
		assert.Empty(t, m.GetSetFields()["status"])
	})

	t.Run("generic single-id path: absent status omits the key", func(t *testing.T) {
		m := compileUpdateForTest(t, `{"operation":"update","id":"n1","summary":"s"}`)
		assert.NotContains(t, m.GetSetFields(), "status")
	})
}

// TestMutateUpdate_StatusClearToBlank_BackendRejected pins the other half of
// Gate 19: clear-to-blank is legal locally but MEANINGLESS on a tracker-backed
// node, whose external tracker has no blank state. The rejection is loud, names
// `status`, and lands before any tracker write.
func TestMutateUpdate_StatusClearToBlank_BackendRejected(t *testing.T) {
	backend := &fakeBackend{name: "linear"}
	fc := &fakeGraphCaller{queryResponses: map[string]kgtools.ToolResult{
		"backend-1": nodeResultJSON(t, "backend-1", "ticket", map[string]string{"backend": "linear"}),
	}}
	deps := interceptTestDeps{byName: map[string]backends.Backend{"linear": backend}, gc: fc}

	handled, res := InterceptMutate(opCtx(), deps, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"update","id":"backend-1","status":""}`),
	})
	require.True(t, handled, "the rejection is a claim — it must not fall through silently")
	require.True(t, res.IsError, "clearing status on a tracker-backed node must reject: %s", toolResultText(res))
	assert.Contains(t, toolResultText(res), "status", "the rejection must NAME the offending param")
	assert.Zero(t, backend.updateTicketCalls, "a rejected update issues NO tracker write")
}
