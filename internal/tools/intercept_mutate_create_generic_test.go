// SPDX-License-Identifier: Apache-2.0

package tools

// intercept_mutate_create_generic_test.go gates the type-blind context-linked
// create arm. It is a sibling of intercept_mutate_create_context_test.go rather
// than part of it because that file sits against the repo's per-file length
// convention; the two are one subject read together.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// TestMutateCreateContextLinked_TypeBlind asserts the SET, not one member: a
// create carrying a resolvable ticket_id born-links whatever its type is.
//
// Group 2 is what makes this a set assertion rather than a longer hand-list. A
// type no vocabulary advertises must born-link too, because the arm is selected
// by the PRESENCE of a context param and never by a type list. Without that
// group the test is satisfied by a switch with a few more cases — the per-type
// design wearing the type-blind one's name.
//
// Group 3 is the anti-over-reach half: the three types that have their own
// handlers must still reach them, so the new arm is proven not to have stolen
// the creates that already worked.
func TestMutateCreateContextLinked_TypeBlind(t *testing.T) {
	t.Run("every advertised generic type born-links", func(t *testing.T) {
		for _, typ := range []string{"resource", "event", "memory", "document"} {
			t.Run(typ, func(t *testing.T) {
				assertTypeBornLinks(t, typ)
			})
		}
	})

	t.Run("a type no vocabulary advertises born-links too", func(t *testing.T) {
		for _, typ := range []string{"use_case", "zzz-nonce-type-9271"} {
			t.Run(typ, func(t *testing.T) {
				assertTypeBornLinks(t, typ)
			})
		}
	})

	t.Run("the separately-armed types still reach their own arms", func(t *testing.T) {
		// Each of the three renders a line only its own handler writes, so a
		// create silently re-routed onto the shared arm shows up here as the
		// generic "Created → ID:" line instead.
		cases := []struct{ typ, wantPrefix string }{
			{"finding", "Finding recorded:"},
			{"research", "Research question recorded:"},
			{"rule", "Rule added:"},
		}
		for _, tc := range cases {
			t.Run(tc.typ, func(t *testing.T) {
				fc := &fakeGraphCaller{mutateIDs: []string{"armed-1"}}
				args := `{"operation":"create","type":"` + tc.typ + `","name":"n","summary":"a searchable summary",` +
					`"description":"d","session":"a-session"}`
				handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
					Name: "mutate", Arguments: json.RawMessage(args),
				})
				require.True(t, handled)
				require.False(t, res.IsError, "create must succeed: %s", toolResultText(res))
				assert.Contains(t, toolResultText(res), tc.wantPrefix,
					"a %s create must still render its own arm's line", tc.typ)
			})
		}
	})
}

// assertTypeBornLinks drives one generic create carrying a resolvable ticket_id
// through the production intercept and asserts the emitted CREATE MutationPlan
// carries ticket--contains-->newNode.
func assertTypeBornLinks(t *testing.T, typ string) {
	t.Helper()
	fc := &fakeGraphCaller{
		queryResponses: map[string]kgtools.ToolResult{
			"tkt-blind": nodeResultJSON(t, "tkt-blind", "ticket", nil),
		},
		mutateIDs: []string{"node-" + typ},
	}
	args := `{"operation":"create","type":"` + typ + `","name":"n","summary":"a searchable summary",` +
		`"ticket_id":"tkt-blind"}`
	handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name: "mutate", Arguments: json.RawMessage(args),
	})
	require.True(t, handled, "a create carrying a context param is CLAIMED, whatever its type")
	require.False(t, res.IsError, "create must succeed: %s", toolResultText(res))
	assert.True(t, findContainsToNewNode(firstCreatePlan(t, fc), "tkt-blind"),
		"a %s create must carry ticket--contains-->newNode", typ)
}

// TestMutateCreateContextLinked_BodyIdenticalWithAndWithoutTrio is the single
// gate covering every way the context-linked path could diverge from the engine
// path on the BODY rather than on the edges: a dropped caller-supplied id, a
// stamped source, a derived summary, a client-side validation that accepts a
// body one way and refuses it the other, or a metadata map lost in transit.
// Adding a context param must change the EDGES and nothing else.
//
// The two sides are measured differently because they take different routes:
// WITH the trio the intercept claims the call, so the body is read off the
// MutationPlan the fake captured; WITHOUT it the intercept declines and no
// Execute happens at all, so the comparison body comes from engine.Compile —
// which is the very path the declining call would have taken.
func TestMutateCreateContextLinked_BodyIdenticalWithAndWithoutTrio(t *testing.T) {
	// Every field carries a distinct non-empty value: a body with empty fields
	// proves the equality of nothing.
	const bodyFields = `"type":"memory","id":"caller-id-7","name":"probe-name","description":"probe-description",` +
		`"summary":"probe summary text","content":"probe content body","status":"probe-status",` +
		`"source":"probe-source","metadata":{"probe-key":"probe-value"}`

	fc := &fakeGraphCaller{
		queryResponses: map[string]kgtools.ToolResult{
			"tkt-body": nodeResultJSON(t, "tkt-body", "ticket", nil),
		},
		mutateIDs: []string{"caller-id-7"},
	}
	withTrio := `{"operation":"create",` + bodyFields + `,"ticket_id":"tkt-body"}`
	handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name: "mutate", Arguments: json.RawMessage(withTrio),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "create must succeed: %s", toolResultText(res))
	claimed := firstCreatePlan(t, fc).GetNodeBodies()
	require.Len(t, claimed, 1)

	withoutTrio := `{"operation":"create",` + bodyFields + `}`
	req, ok := engine.Compile("mutate", json.RawMessage(withoutTrio))
	require.True(t, ok, "the trio-less payload must still lower to a MutationPlan")
	declined := req.GetMutation().GetNodeBodies()
	require.Len(t, declined, 1)

	a, b := claimed[0], declined[0]
	assert.Equal(t, b.GetType(), a.GetType())
	assert.Equal(t, b.GetName(), a.GetName())
	assert.Equal(t, b.GetDescription(), a.GetDescription())
	assert.Equal(t, b.GetSummary(), a.GetSummary(), "no summary derivation on the context-linked path")
	assert.Equal(t, b.GetContent(), a.GetContent())
	assert.Equal(t, b.GetStatus(), a.GetStatus())
	assert.Equal(t, b.GetMetadata(), a.GetMetadata(), "the caller's metadata map rides through unreshaped")
	assert.Equal(t, b.GetId(), a.GetId(), "a caller-supplied id survives both routes")
	assert.Equal(t, b.GetSource(), a.GetSource(), "no source stamping on the context-linked path")
}

// TestMutateCreateContextLinked_FormatHonoredBothRenders is the ONLY gate on the
// render branch — every other assertion in this area reads MutationPlans or file
// bytes, and the parity harness treats format as selection-only, which a
// deliberately-ignored cell satisfies just as well as a consumed one. Without
// this test the opposite design ships all-green.
//
// Legs 3 and 4 are a matched pair. Leg 3 alone is satisfied by an implementation
// that drops warnings entirely in json mode — silent information loss rather
// than a shape fix — so leg 4 pins that the key appears exactly when there is
// something to report.
func TestMutateCreateContextLinked_FormatHonoredBothRenders(t *testing.T) {
	seededFake := func(ids ...string) *fakeGraphCaller {
		return &fakeGraphCaller{
			queryResponses: map[string]kgtools.ToolResult{
				"tkt-fmt": nodeResultJSON(t, "tkt-fmt", "ticket", nil),
			},
			mutateIDs: ids,
		}
	}
	drive := func(t *testing.T, fc *fakeGraphCaller, args string) kgtools.ToolResult {
		t.Helper()
		handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "mutate", Arguments: json.RawMessage(args),
		})
		require.True(t, handled)
		require.False(t, res.IsError, "create must succeed: %s", toolResultText(res))
		return res
	}

	t.Run("json renders the engine's own ids object", func(t *testing.T) {
		res := drive(t, seededFake("doc-json"),
			`{"operation":"create","type":"document","name":"n","summary":"s","ticket_id":"tkt-fmt","format":"json"}`)
		var body map[string]any
		require.NoError(t, json.Unmarshal([]byte(toolResultText(res)), &body))
		require.Contains(t, body, "ids")
		ids, ok := body["ids"].([]any)
		require.True(t, ok, "ids must be an array: %v", body["ids"])
		require.Len(t, ids, 1)
		assert.Equal(t, "doc-json", ids[0])
		// LEG 3: a clean create's body is byte-compatible with the engine's, which
		// means the warnings key is ABSENT rather than present-and-null.
		assert.NotContains(t, body, "warnings",
			"a clean create must not grow a warnings key the engine's own json shape has no room for")
		assert.Len(t, body, 1, "ids is the ONLY key on a clean create: %v", body)
	})

	t.Run("text renders the engine's own single-id line", func(t *testing.T) {
		res := drive(t, seededFake("doc-text"),
			`{"operation":"create","type":"document","name":"n","summary":"s","ticket_id":"tkt-fmt"}`)
		assert.Contains(t, toolResultText(res), "Created → ID: doc-text")
	})

	t.Run("json carries warnings when a target drops", func(t *testing.T) {
		// The ticket resolves nowhere, so the fail-tolerance contract creates the
		// node anyway and reports the drop — in json, where leg 3 proved the key
		// is otherwise absent.
		fc := &fakeGraphCaller{mutateIDs: []string{"doc-warn"}}
		res := drive(t, fc,
			`{"operation":"create","type":"document","name":"n","summary":"s","ticket_id":"ghost-ticket","format":"json"}`)
		var body map[string]any
		require.NoError(t, json.Unmarshal([]byte(toolResultText(res)), &body))
		require.Contains(t, body, "warnings", "a dropped target must be reported, not swallowed by the json branch")
		warnings, ok := body["warnings"].([]any)
		require.True(t, ok, "warnings must be an array: %v", body["warnings"])
		require.NotEmpty(t, warnings)
		assert.Contains(t, warnings[0], "ghost-ticket", "the warning names the target that dropped")
	})
}
