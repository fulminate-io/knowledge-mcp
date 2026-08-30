// SPDX-License-Identifier: Apache-2.0

package tools

// mutate_surface_discoverability_test.go covers the DISCOVERABILITY half of the
// mutate deny-gates: the param-accounting gate was already loud, but its generic message
// ends "drop it or issue a separate call that does" without naming the call. Each
// test below pins a rejection that now names the working form, and each pairs the
// rejection with the shape that ACTUALLY WORKS, so a hint that drifts away from
// the live routing is caught rather than merely re-asserted.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// TestUnlink_LinkGraphRejection_NamesTheLinkageForm pins the (a) fix: an unlink
// carrying link_graph is still rejected — nothing routes it — but the rejection
// now names the call that DOES retract a linkage edge.
//
// The rejection itself is not new; what is new is that it stops reading as "the
// linkage graph is unreachable from unlink", which is false and was the reading
// the ticket was filed on.
func TestUnlink_LinkGraphRejection_NamesTheLinkageForm(t *testing.T) {
	fc := &fakeGraphCaller{}
	handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name: "mutate",
		Arguments: json.RawMessage(`{"operation":"unlink","link_graph":"linkage",` +
			`"from":"00000000000000000000000000000aaa","to":"00000000000000000000000000000bbb",` +
			`"relationship":"implements"}`),
	})
	require.True(t, handled, "the rejection is a claim")
	require.True(t, res.IsError, "link_graph is not routed on unlink: %s", toolResultText(res))

	msg := toolResultText(res)
	assert.Contains(t, msg, "link_graph", "the rejection still NAMES the param")
	for _, want := range []string{
		`graph:"linkage"`,
		`proxy:knowledge:<code-id>`,
		"unlink resolves no raw foreign id",
	} {
		assert.Containsf(t, msg, want, "the rejection must name the working unlink form (missing %q)", want)
	}
	assert.NotContains(t, msg, "issue a separate call that does",
		"this param has a hint, so it must not fall back to the unnamed generic message")
	assert.Empty(t, fc.execMutations, "a pre-write rejection issues ZERO mutations")
}

// TestUnlink_WithoutLinkGraph_IsNotRejected is the control for the test above and
// for the whole (a) item: the ONLY thing wrong with the reported call was the
// param name, so a plain unlink must still pass accounting and decline to the
// engine. Without this, a gate that rejected every unlink would satisfy the
// rejection test.
func TestUnlink_WithoutLinkGraph_IsNotRejected(t *testing.T) {
	fc := &fakeGraphCaller{}
	handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name: "mutate",
		Arguments: json.RawMessage(`{"operation":"unlink",` +
			`"from":"00000000000000000000000000000aaa","to":"00000000000000000000000000000bbb",` +
			`"relationship":"implements"}`),
	})
	assert.False(t, handled, "a plain unlink declines to the engine unlink arm")
	assert.False(t, res.IsError, "a plain unlink must not be rejected: %s", toolResultText(res))
}

// TestUnlink_GraphLinkageForm_ReachesTheNonKnowledgePath proves the form the hint
// names is the form the dispatch actually serves: with graph:"linkage" the call
// passes accounting and declines to the engine, rather than being rejected here.
//
// This is the assertion that keeps the hint HONEST. A hint is prose; without a
// test driving the shape it names, it can go on naming a form that a later
// routing change stopped serving, and no gate would notice.
func TestUnlink_GraphLinkageForm_ReachesTheNonKnowledgePath(t *testing.T) {
	fc := &fakeGraphCaller{}
	handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name: "mutate",
		Arguments: json.RawMessage(`{"operation":"unlink","graph":"linkage",` +
			`"from":"00000000000000000000000000000aaa",` +
			`"to":"proxy:knowledge:cmd/knowledge/internal/projects/builders.go",` +
			`"relationship":"IMPLEMENTS"}`),
	})
	assert.False(t, handled, "the linkage-graph unlink is not claimed by any client arm — it routes onward")
	assert.False(t, res.IsError, "the form the hint names must not be rejected: %s", toolResultText(res))
}

// TestUnlink_NameRejection_NamesTheNameAddressedForm covers the class improvement
// on the other param armUnlink rejects. `name` is the graph-INSTANCE selector; it
// routes fine one graph over, and the generic message never said so.
func TestUnlink_NameRejection_NamesTheNameAddressedForm(t *testing.T) {
	fc := &fakeGraphCaller{}
	handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name: "mutate",
		Arguments: json.RawMessage(`{"operation":"unlink","name":"some-instance",` +
			`"from":"00000000000000000000000000000aaa","to":"00000000000000000000000000000bbb",` +
			`"relationship":"relates-to"}`),
	})
	require.True(t, handled)
	require.True(t, res.IsError, "name is not routed on a knowledge-family unlink: %s", toolResultText(res))

	msg := toolResultText(res)
	assert.Contains(t, msg, "name")
	assert.Contains(t, msg, "graph-INSTANCE selector",
		"the rejection explains WHY the param reaches nothing here")
	assert.Contains(t, msg, `mutate(unlink, graph:"<family>", name:"<instance>"`,
		"the rejection names the shape that does route it")
	assert.NotContains(t, msg, "issue a separate call that does")
}

// TestContextLinkTrio_RejectionNamesTheFollowUpLink covers the class improvement
// on the three create arms with no context-link carrier. The trio has an exact
// working alternative — the create returns the id its follow-up link needs — and
// the generic message never named it.
//
// The two arms that still refuse the trio are driven, because the hint is
// attached per-arm: a fix applied to one arm and forgotten on the other passes
// any single-arm test. The generic-create row is gone — a generic create
// carrying a context param is now claimed and born-linked rather than refused,
// so there is no refusal there left to assert.
//
// THE TABLE DRIVES THE PRODUCTION CHAIN, not one interceptor. InterceptAddCriterion
// claims create+criterion ahead of InterceptMutate in the real dispatch order, so
// calling InterceptMutate alone would route the criterion row somewhere it never
// goes in production and measure a different arm than the row is named for.
func TestContextLinkTrio_RejectionNamesTheFollowUpLink(t *testing.T) {
	cases := []struct {
		name  string
		args  string
		param string
	}{
		{
			name:  "criterion create rejecting session",
			args:  `{"operation":"create","type":"criterion","step_id":"00000000000000000000000000000aaa","description":"d","session":"s"}`,
			param: "session",
		},
		{
			name: "create_batch rejecting ticket_id",
			args: `{"operation":"create_batch","ticket_id":"00000000000000000000000000000aaa",` +
				`"nodes":[{"type":"finding","name":"n","summary":"s"}]}`,
			param: "ticket_id",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := &fakeGraphCaller{}
			params := kgtools.CallToolParams{Name: "mutate", Arguments: json.RawMessage(tc.args)}
			deps := interceptTestDeps{gc: fc}
			handled, res := InterceptAddCriterion(context.Background(), deps, params)
			if !handled {
				handled, res = InterceptMutate(opCtx(), deps, params)
			}
			require.True(t, handled, "the rejection is a claim")
			require.True(t, res.IsError, "%s has no carrier on this arm: %s", tc.param, toolResultText(res))

			msg := toolResultText(res)
			assert.Contains(t, msg, tc.param, "the rejection still NAMES the param")
			assert.Contains(t, msg, "issue a follow-up mutate(link)",
				"the rejection names the call that does the work")
			assert.Contains(t, msg, `relationship:"relates-to"`)
			assert.Contains(t, msg, `relationship:"contains"`)
			assert.NotContains(t, msg, "issue a separate call that does",
				"a param with a hint must not fall back to the unnamed generic message")
			assert.Empty(t, fc.execMutations, "a pre-write rejection issues ZERO mutations")
		})
	}
}

// TestContextLinkTrio_RoutingArmsStillAcceptIt is the anti-over-reach control for
// the test above: the trio is REJECTED only on arms with no carrier. The
// finding/research/rule creates route it, and a hint added to the wrong arm — or
// a rejection widened while adding one — would show up here as a create that
// stopped accepting its own documented params.
func TestContextLinkTrio_RoutingArmsStillAcceptIt(t *testing.T) {
	fc := &fakeGraphCaller{mutateIDs: []string{"fnd-1"}}
	handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name: "mutate",
		Arguments: json.RawMessage(`{"operation":"create","type":"finding","name":"f","summary":"s",` +
			`"description":"d","session":"a-session"}`),
	})
	require.True(t, handled)
	assert.False(t, res.IsError,
		"a finding create routes the context-link trio — it must not be rejected: %s", toolResultText(res))
}

// TestMutateSchema_DocumentsTheUnlinkLinkageForm pins the schema half of (a). The
// hint reaches only a caller who already made the mistake; the schema is what a
// caller reads BEFORE composing the call, and mutate_schema.go's graph vocabulary
// omitted linkage entirely.
func TestMutateSchema_DocumentsTheUnlinkLinkageForm(t *testing.T) {
	t.Run("the graph param names linkage and the proxy-endpoint requirement", func(t *testing.T) {
		graph, ok := mutateProperties()["graph"]
		require.True(t, ok)
		for _, want := range []string{"linkage", "operation=unlink", "proxy:knowledge:<code-id>"} {
			assert.Containsf(t, graph.Description, want, "graph description missing %q", want)
		}
	})

	t.Run("the link_graph param scopes itself to link and points at graph", func(t *testing.T) {
		linkGraph, ok := mutateProperties()["link_graph"]
		require.True(t, ok)
		assert.Contains(t, linkGraph.Description, "LINK ONLY")
		assert.Contains(t, linkGraph.Description, "unlink does not route this param")
	})

	t.Run("the unlink operation line carries the working call", func(t *testing.T) {
		desc := MutateToolDef().Description
		assert.Contains(t, desc, `graph:"linkage"`)
		assert.Contains(t, desc, `to:"proxy:knowledge:<code-id>"`)
	})
}
