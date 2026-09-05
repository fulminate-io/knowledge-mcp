// SPDX-License-Identifier: Apache-2.0

package tools

// intercept_create_plan_sections_test.go covers create_plan's sectioned shape:
// the accept path, the six refusal arms, the determinism rule the refusals
// inherit, and the proof that a refusal writes nothing.
//
// EVERY REFUSAL ARM ASSERTS TWO THINGS — the message names the offending section
// and the valid shape, AND no mutate Execute fired. A refusal that reported an
// error after writing would satisfy an IsError-only check.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

func sectionPlanFake() *fakePlanGraphCaller {
	return &fakePlanGraphCaller{
		mutateResult: kgtools.ToolResult{
			Content: []kgtools.ContentBlock{{Type: "text", Text: `{"ids":["plan-1","sec-0","sec-1"]}`}},
		},
		queryResponses: map[string]kgtools.ToolResult{},
	}
}

func createPlanCall(t *testing.T, fc *fakePlanGraphCaller, args string) kgtools.ToolResult {
	t.Helper()
	handled, res := InterceptCreatePlan(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name:      "create_plan",
		Arguments: json.RawMessage(args),
	})
	require.True(t, handled, "create_plan is always claimed")
	return res
}

// mutateCalls counts the write Executes the fake saw, which is how each refusal
// arm proves it wrote NOTHING.
func mutateCalls(fc *fakePlanGraphCaller) int {
	n := 0
	for _, c := range fc.calls {
		if c.tool == "mutate" {
			n++
		}
	}
	return n
}

// R5-a. An ordered sections list with NO phases creates the plan.
//
// RED LEG: against the tree before this change the same call returned
// "at least one phase is required".
func TestInterceptCreatePlan_SectionsWithNoPhases(t *testing.T) {
	fc := sectionPlanFake()
	fc.queryResponses["plan-1"] = nodeResultJSON(t, "plan-1", "plan", map[string]string{})
	res := createPlanCall(t, fc, `{
		"name":"sectioned",
		"goal":"the goal",
		"summary":"a sectioned plan",
		"no_patterns_reason":"redesign of an in-repo artifact shape",
		"sections":[
			{"name":"Touch points","body":"every site the change reaches","summary":"touch points"},
			{"name":"What to test","body":"the list the implementer writes tests from","summary":"what to test"}
		]
	}`)
	require.False(t, res.IsError, "a sectioned plan with no phases must create: %s", toolResultText(res))
	assert.Contains(t, toolResultText(res), "Plan created: sectioned")
	assert.Equal(t, 1, mutateCalls(fc), "exactly one create_batch write")
}

// R5-b. A phases-and-steps plan with NO sections creates EXACTLY as today, and
// "exactly" is asserted as a BYTE COMPARE against the pre-change tree rather than
// as a smoke check.
//
// WHY THE BYTE COMPARE AND NOT A Contains. The requirement is that the shape this
// change did not touch is untouched, and a Contains on the success line passes
// against a render that gained a stray blank line, lost the pattern-suggest
// block, reordered the tree or changed an id suffix. Every one of those is a
// visible regression to a caller and invisible to the assertion that was here
// before.
//
// THE LITERAL IS CAPTURED, NOT COMPOSED. It is the output of this exact fixture
// run through InterceptCreatePlan at origin/main 46196268 — the tree before this
// branch — transcribed from that run's own log. Composing an expected string here
// would only assert that this test agrees with itself.
func TestInterceptCreatePlan_PhasePlanUnaffectedBySections(t *testing.T) {
	// Captured at 46196268 by running this fixture through InterceptCreatePlan.
	const preChangeRender = "Plan created: phase plan → ID: plan-1\n" +
		"\n (plan)\n  ID: plan-1\n\n\n" +
		"## Pattern Auto-Suggest\n" +
		"Cross-practice fan-out found no hits above 0.40 for query `\"phase plan g\"`. If you suspect a pattern applies, run\n" +
		"`search({\"graph\": \"practice\", \"query\": \"<refined terms>\"})` and retry the create call with `pattern_ids` set.\n" +
		" [graph: knowledge/default]"

	fc := sectionPlanFake()
	fc.queryResponses["plan-1"] = nodeResultJSON(t, "plan-1", "plan", map[string]string{})
	res := createPlanCall(t, fc, `{
		"name":"phase plan",
		"goal":"g",
		"summary":"s",
		"no_patterns_reason":"trivial",
		"phases":[{"name":"phase-1","overview":"o","summary":"ps","steps":[{"name":"step-1","description":"step 1 description body","summary":"ss"}]}]
	}`)
	require.False(t, res.IsError, "%s", toolResultText(res))
	assert.Equal(t, preChangeRender, toolResultText(res),
		"a phase plan's rendered create result is byte-identical to the pre-change tree's")
}

// R5-c. The refusal arms, one test each. Each names the offending section and
// each writes nothing.
func TestInterceptCreatePlan_SectionRefusals(t *testing.T) {
	cases := []struct {
		name    string
		args    string
		wantSub []string
	}{
		{
			name: "neither phases nor sections",
			args: `{"name":"n","goal":"g","summary":"s","no_patterns_reason":"r"}`,
			// The zero-phase refusal still fires for a plan that supplies neither
			// shape — it is only lifted for a plan that supplies sections.
			wantSub: []string{"phases", "sections"},
		},
		{
			name:    "sections and phases together",
			args:    `{"name":"n","goal":"g","summary":"s","no_patterns_reason":"r","phases":[{"name":"p","summary":"ps","steps":[{"name":"st","description":"d body","summary":"ss"}]}],"sections":[{"name":"a","body":"A","summary":"as"}]}`,
			wantSub: []string{"phases", "sections", "exactly one"},
		},
		{
			name:    "a section with no name",
			args:    `{"name":"n","goal":"g","summary":"s","no_patterns_reason":"r","sections":[{"name":"","body":"A","summary":"as"}]}`,
			wantSub: []string{"sections[0].name"},
		},
		{
			name:    "a section with no body",
			args:    `{"name":"n","goal":"g","summary":"s","no_patterns_reason":"r","sections":[{"name":"a","body":"","summary":"as"}]}`,
			wantSub: []string{"sections[0].body"},
		},
		{
			name:    "a duplicate position",
			args:    `{"name":"n","goal":"g","summary":"s","no_patterns_reason":"r","sections":[{"name":"a","body":"A","summary":"as","position":2},{"name":"b","body":"B","summary":"bs","position":2}]}`,
			wantSub: []string{"sections[1].position", "2", "sections[0]"},
		},
		{
			name:    "a missing position among supplied ones",
			args:    `{"name":"n","goal":"g","summary":"s","no_patterns_reason":"r","sections":[{"name":"a","body":"A","summary":"as","position":0},{"name":"b","body":"B","summary":"bs"}]}`,
			wantSub: []string{"sections[1].position", "every section"},
		},
		{
			name:    "a negative position",
			args:    `{"name":"n","goal":"g","summary":"s","no_patterns_reason":"r","sections":[{"name":"a","body":"A","summary":"as","position":-1}]}`,
			wantSub: []string{"sections[0].position", "-1"},
		},
		{
			name: "an undeclared key inside a sections entry",
			// rejectUndeclaredParams is TOP-LEVEL ONLY, so a nested undeclared key
			// needs its own arm — without one it vanishes into a successful create.
			args:    `{"name":"n","goal":"g","summary":"s","no_patterns_reason":"r","sections":[{"name":"a","body":"A","summary":"as","overview":"typo'd key"}]}`,
			wantSub: []string{"sections[0]", "overview"},
		},
		{
			name:    "a section with no summary",
			args:    `{"name":"n","goal":"g","summary":"s","no_patterns_reason":"r","sections":[{"name":"a","body":"A"}]}`,
			wantSub: []string{"sections[0].summary"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := sectionPlanFake()
			res := createPlanCall(t, fc, tc.args)
			require.True(t, res.IsError, "expected a refusal, got: %s", toolResultText(res))
			body := toolResultText(res)
			for _, sub := range tc.wantSub {
				assert.Contains(t, body, sub, "the refusal must name %q", sub)
			}
			// R5-e: a refusal writes NOTHING.
			assert.Zero(t, mutateCalls(fc), "a refused create_plan must not write")
		})
	}
}

// R5-d. The refusal walks the payload in the CALLER'S OWN ORDER, so a payload
// with TWO offenders always names the same one first. Without this a caller
// fixing the reported offender can be handed a different one on every retry.
func TestInterceptCreatePlan_SectionRefusalIsDeterministic(t *testing.T) {
	const twoOffenders = `{"name":"n","goal":"g","summary":"s","no_patterns_reason":"r","sections":[
		{"name":"","body":"A","summary":"as"},
		{"name":"b","body":"","summary":"bs"}
	]}`
	var first string
	for i := range 8 {
		fc := sectionPlanFake()
		res := createPlanCall(t, fc, twoOffenders)
		require.True(t, res.IsError)
		body := toolResultText(res)
		if i == 0 {
			first = body
			assert.Contains(t, body, "sections[0].name", "the FIRST offender in the caller's order is named")
			assert.NotContains(t, body, "sections[1]", "the second offender is not what is reported")
			continue
		}
		assert.Equal(t, first, body, "the same payload names the same offender on every run")
	}
}

// TestSectionKeySet_IsReadOffTheSchema pins that the section guard derives its
// vocabulary from the tool definition rather than carrying a second copy.
//
// WHY A SECOND COPY IS THE DEFECT AND NOT MERELY UNTIDY. The schema is what the
// caller is shown; the guard is what the caller is judged by. Two hand-written
// lists drift in the direction that is worst for the caller — a key added to the
// schema and not the guard is documented and refused, and a key added to the
// guard and not the schema is accepted and undocumented. The sibling guard on
// update_batch's items[] states this rule where it follows it; this is the same
// rule at the other nested list.
//
// THE ASSERTION IS ON THE REFUSAL MESSAGE, not only on the helper, because the
// message is what a caller reads and it is where a hand-list actually surfaced:
// before this change it named "name, body, summary and position" in prose while
// the schema was the real authority.
func TestSectionKeySet_IsReadOffTheSchema(t *testing.T) {
	declared := declaredSectionKeys()
	require.NotEmpty(t, declared, "the guard must find the sections[] shape on the tool definition")

	// The decoder's own json tags, transcribed from createPlanSection. Nothing but
	// this assertion holds the three lists — schema, decoder, guard — equal.
	for _, key := range []string{"name", "body", "summary", "position"} {
		assert.True(t, declared[key], "createPlanSection decodes %q but the sections[] schema does not declare it", key)
	}
	assert.Len(t, declared, 4, "a schema key the decoder cannot read is silently unusable")

	fc := sectionPlanFake()
	res := createPlanCall(t, fc, `{
		"name":"p","goal":"g","summary":"s","no_patterns_reason":"x",
		"sections":[{"name":"n","body":"b","summary":"s","overview":"typo"}]
	}`)
	require.True(t, res.IsError)
	assert.Contains(t, toolResultText(res), strings.Join(sortedDeclaredKeys(declared), ", "),
		"the refusal lists the SCHEMA's key set verbatim, so the message cannot drift from what the caller was shown")
}
