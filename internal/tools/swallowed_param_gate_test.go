// SPDX-License-Identifier: Apache-2.0

// swallowed_param_gate_test.go — the control set for the swallowed-parameter
// refusal.
//
// THE POSITIVE SPECIMENS ARE TRANSCRIBED FROM REAL DAMAGED NODES, not invented:
// a thoughts(think) content body that ended in a bare `<parameter name="session">`
// fragment, a criterion summary that ended in a `<metadata>…</metadata>` block,
// and a content body that ended in a stray `</invoke>`. All three were produced
// by a mis-serialized tool call and stored verbatim while the parameters after
// the stray closing tag reached the tool as ABSENT.
//
// THE KNOWN-NEGATIVE CONTROLS ARE ALSO REAL STORED TEXT, and they are the
// load-bearing half. A refusal keyed on "the value contains its own closing tag"
// would refuse the bug report about this bug: the defect ticket's own summary and
// description both QUOTE `</summary><metadata>{…}</metadata>` as the specimen.
// Those two strings are pinned below verbatim. If the predicate is ever widened
// to a plain substring test they go red, which is the outcome that matters — a
// gate that cannot store its own incident report is not shippable.

package tools

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/backends"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// The three reproduced failure shapes, verbatim.
const (
	// A think call whose `session` parameter was swallowed into content: the
	// remainder opens a tag that is never closed and runs to the end of the value.
	swallowSpecimenBareParam = "During ledger triage the mechanism was traced end to end.\n" +
		"</content>\n<parameter name=\"session\">ledger-triage"
	// A criterion update whose metadata block was swallowed into summary: the
	// remainder is balanced markup that TERMINATES the value.
	swallowSpecimenMetaBlock = "the boundary sweep leaves no unevaluated gate criterion" +
		"</summary><metadata>{\"evaluate_at\":\"phase-2-boundary\"}</metadata>"
	// A think call whose closing scaffolding was swallowed into content.
	swallowSpecimenStrayInvoke = "Approach for the remaining defects, read against the current tree." +
		"</content>\n</invoke>"
)

// The two known-negative controls, transcribed from the defect ticket's own
// stored summary and description. Both QUOTE the specimen mid-prose and continue
// into sentences afterwards.
const (
	swallowControlTicketSummary = "Three related defects on the criterion-node update path, observed live in " +
		"one session: (3) dropped keys can serialize INTO the summary as literal " +
		"'</summary><metadata>{…}</metadata>' text. Silent narrowing on a"
	swallowControlTicketDescription = "DEFECT 3 — dropped keys serialized into summary. In one observed case the " +
		"intended metadata keys (evaluate_at / gate_script / added_by) ended up INSIDE the summary " +
		"field as literal \"</summary><metadata>{…}</metadata>\" text while metadata carried only " +
		"author/command/type. Needs isolation: same drop path or a distinct serialization leak."
	// The description-field counterpart. The ticket's own description quotes
	// `</summary>`, so scanning it AS a description never reaches the predicate at
	// all — the closing-tag anchor is absent and the function short-circuits. A
	// control that short-circuits proves nothing, so the description case needs a
	// value that actually carries `</description>` mid-prose.
	swallowControlDescriptionProse = "The repair note records the damaged shape: the body ended with " +
		"</description><metadata>{…}</metadata> and every parameter after it was lost. " +
		"The sentence continues past the quotation, which is what a quotation does."
)

// TestSwallowedParamFragment_DiscriminatesFailureFromQuotation is the predicate's
// own control set: three real failures must be named, and four values that are
// merely ABOUT the failure (or contain no markup at all) must be silent.
func TestSwallowedParamFragment_DiscriminatesFailureFromQuotation(t *testing.T) {
	refused := []struct {
		name  string
		field string
		text  string
		want  string
	}{
		{
			name:  "bare parameter tag, unterminated, runs to end of content",
			field: "content",
			text:  swallowSpecimenBareParam,
			want:  "<parameter name=\"session\">",
		},
		{
			name:  "balanced metadata block terminating the summary",
			field: "summary",
			text:  swallowSpecimenMetaBlock,
			want:  "</metadata>",
		},
		{
			name:  "stray closing scaffolding terminating the content",
			field: "content",
			text:  swallowSpecimenStrayInvoke,
			want:  "</invoke>",
		},
		{
			name:  "value ends exactly at its own closing tag",
			field: "description",
			text:  "the sweep leaves no orphan rows</description>\n",
			want:  "</description>",
		},
	}
	for _, tc := range refused {
		t.Run("refused/"+tc.name, func(t *testing.T) {
			frag := swallowedParamFragment(tc.field, tc.text)
			require.NotEmpty(t, frag, "this shape must be named as a swallowed-parameter tail")
			assert.Contains(t, frag, "</"+tc.field+">", "the fragment starts at the stray closing tag")
			assert.Contains(t, frag, tc.want, "the fragment carries the swallowed remainder")
		})
	}

	// nearMiss marks the controls that must REACH the predicate rather than
	// short-circuiting on an absent closing tag. Without that distinction a
	// control can pass for the wrong reason — the ticket's own DESCRIPTION quotes
	// `</summary>`, so scanning it as a description never anchors at all and would
	// stay green against a predicate widened to a plain substring test. The
	// require below makes that failure mode impossible to author by accident.
	allowed := []struct {
		name     string
		field    string
		text     string
		nearMiss bool
	}{
		{
			name:     "ticket summary quoting the specimen mid-prose",
			field:    "summary",
			text:     swallowControlTicketSummary,
			nearMiss: true,
		},
		{
			name:     "ticket description text, scanned against the tag it quotes",
			field:    "summary",
			text:     swallowControlTicketDescription,
			nearMiss: true,
		},
		{
			name:     "description prose quoting its own closing tag mid-sentence",
			field:    "description",
			text:     swallowControlDescriptionProse,
			nearMiss: true,
		},
		{
			name:  "prose with no markup at all",
			field: "content",
			text:  "The reconcile leaves no orphan rows; the sweep gate asserts remainder = 0.",
		},
		{
			name:  "markup naming a DIFFERENT field's closing tag",
			field: "summary",
			text:  "the leak put </content> inside the body</notsummary>",
		},
	}
	for _, tc := range allowed {
		t.Run("allowed/"+tc.name, func(t *testing.T) {
			if tc.nearMiss {
				require.Contains(t, tc.text, "</"+tc.field+">",
					"a near-miss control must carry the field's own closing tag, or it never reaches the predicate")
			}
			assert.Empty(t, swallowedParamFragment(tc.field, tc.text),
				"a value that merely mentions the markup must stay writable")
		})
	}
}

// TestRejectSwallowedParamValues_DescendsNestedBodies pins that the sweep reaches
// the nested per-node bodies create_batch / create_plan carry, not only top-level
// params. A top-level-only sweep would leave every structured create unguarded.
func TestRejectSwallowedParamValues_DescendsNestedBodies(t *testing.T) {
	raw, err := json.Marshal(map[string]any{
		"operation": "create_batch",
		"nodes": []map[string]any{
			{"type": "finding", "summary": "clean", "content": "clean body"},
			{"type": "finding", "summary": "also clean", "content": swallowSpecimenBareParam},
		},
	})
	require.NoError(t, err)
	err = rejectSwallowedParamValues("mutate", json.RawMessage(raw))
	require.Error(t, err, "a swallowed tail inside nodes[] must be refused")
	assert.Contains(t, err.Error(), "content", "the error names the offending field")

	// Known positive for the same instrument: the identical payload with the
	// damaged body replaced by clean prose must pass, so a sweep that refused
	// everything (or never ran) is distinguishable from one that discriminates.
	clean, err := json.Marshal(map[string]any{
		"operation": "create_batch",
		"nodes": []map[string]any{
			{"type": "finding", "summary": "clean", "content": "clean body"},
			{"type": "finding", "summary": "also clean", "content": "an ordinary body"},
		},
	})
	require.NoError(t, err)
	assert.NoError(t, rejectSwallowedParamValues("mutate", json.RawMessage(clean)))
}

// TestInterceptMutate_SwallowedParamTail_RefusesBeforeAnyWrite is the end-to-end
// half: the reported criterion-summary shape must be refused at the tool
// boundary, with ZERO forwarded mutations, and the error must name the field and
// quote the offending tail.
func TestInterceptMutate_SwallowedParamTail_RefusesBeforeAnyWrite(t *testing.T) {
	stored, err := json.Marshal(map[string]any{
		"id": "c1", "type": "criterion",
		"symbol_name": "old description", "description": "old description",
		"metadata": map[string]string{"type": "manual"},
	})
	require.NoError(t, err)
	fc := &fakeGraphCaller{queryResponses: map[string]kgtools.ToolResult{
		"c1": {Content: []kgtools.ContentBlock{{Type: "text", Text: string(stored)}}},
	}}
	deps := interceptTestDeps{byName: map[string]backends.Backend{}, gc: fc}

	args, err := json.Marshal(map[string]any{
		"operation": "update", "id": "c1", "summary": swallowSpecimenMetaBlock,
	})
	require.NoError(t, err)

	handled, res := InterceptMutate(opCtx(), deps, kgtools.CallToolParams{
		Name: "mutate", Arguments: args,
	})
	require.True(t, handled, "the malformed call must be CLAIMED and refused, never passed on")
	require.True(t, res.IsError, "a swallowed-parameter tail must be refused, not stored")
	body := toolResultText(res)
	assert.Contains(t, body, "summary", "the refusal names the offending field")
	assert.Contains(t, body, "</summary>", "the refusal quotes the malformed input")
	assert.Empty(t, fc.execMutations,
		"refusing means ZERO writes — never a partial write with the params dropped")
}

// TestInterceptThoughts_SwallowedParamTail_RefusesBeforeAnyWrite is the same
// end-to-end assertion on the think path, which is where the swallowed
// session/links parameters were actually observed.
func TestInterceptThoughts_SwallowedParamTail_RefusesBeforeAnyWrite(t *testing.T) {
	fc := &fakeGraphCaller{}
	deps := interceptTestDeps{byName: map[string]backends.Backend{}, gc: fc}

	args, err := json.Marshal(map[string]any{
		"operation": "think",
		"summary":   "ledger triage traced the mechanism end to end",
		"content":   swallowSpecimenBareParam,
	})
	require.NoError(t, err)

	handled, res := InterceptThoughts(opCtx(), deps, kgtools.CallToolParams{
		Name: "thoughts", Arguments: args,
	})
	require.True(t, handled)
	require.True(t, res.IsError, "a swallowed-parameter tail must be refused, not stored")
	body := toolResultText(res)
	assert.Contains(t, body, "content", "the refusal names the offending field")
	assert.Contains(t, body, "session", "the refusal quotes the swallowed parameter markup")
	assert.Empty(t, fc.execMutations,
		"refusing means ZERO writes — never a thought stored with tag soup in its body")

	// Known positive for the same instrument: an identical call whose content is
	// clean must NOT be refused by this gate, so a gate that refuses every think
	// is distinguishable from one that discriminates.
	clean, err := json.Marshal(map[string]any{
		"operation": "think",
		"summary":   "ledger triage traced the mechanism end to end",
		"content":   "Ledger triage traced the mechanism end to end.",
	})
	require.NoError(t, err)
	_, cleanRes := InterceptThoughts(opCtx(), deps, kgtools.CallToolParams{
		Name: "thoughts", Arguments: clean,
	})
	assert.NotContains(t, toolResultText(cleanRes), "swallowed",
		"a clean think must not be refused by the swallowed-parameter gate")
}

// TestSwallowScannedFields_ExactContents asserts the SET, which is the one thing
// the behavioral test below structurally cannot: "each family drives at least one
// of its fields" floors coverage at five of the fourteen additions, so nine
// declared names could be absent, misspelled, or never added and every behavioral
// leg would still pass.
//
// TWO ASSERTIONS THAT FAIL ON DIFFERENT MISTAKES. Cardinality catches an omission
// and a duplicate; set equality catches a MISSPELLING, which keeps the count at 17
// while silently scanning a key no tool ever sends — a dead entry that looks like
// coverage. Containment would not do: it passes with an extra unclassified name,
// which is how `findings` would slip back in.
func TestSwallowScannedFields_ExactContents(t *testing.T) {
	// The executable copy of the step's prose list. It lives beside the thing it
	// describes, so any future addition to swallowScannedFields reddens here in the
	// SAME edit — which is the moment to decide whether the new name belongs.
	want := []string{
		"content", "description", "summary",
		"reasoning",
		"goal", "overview", "sketch", "no_patterns_reason",
		"rationale", "alternatives", "context", "choice",
		"question",
		"conclusion", "evidence", "enforcement", "edge_evidence",
	}
	require.Len(t, want, 17, "the expectation table itself is the seventeen declared names")

	assert.Len(t, swallowScannedFields, 17,
		"swallowScannedFields holds exactly the seventeen declared names — a count mismatch is an "+
			"omission or a duplicate")
	assert.ElementsMatch(t, want, swallowScannedFields,
		"the scanned set must EQUAL the declared set; a name present here and absent there is a "+
			"misspelling that scans a key no tool sends")

	// THE EXCLUSION IS PINNED BY CONSTRUCTION: `findings` is "Comma-separated
	// finding node IDs", not prose, so adding it reddens the equality above. This
	// leg names the reason so a later reader does not re-propose it.
	assert.NotContains(t, swallowScannedFields, "findings",
		"`findings` carries node IDs, not prose — an id-valued field in a prose scanner buys nothing")
}

// swallowNearMiss builds the GREEN fixture for a field: prose that quotes the
// field's own closing tag MID-SENTENCE and continues afterwards. That is the
// discrimination the gate's header requires — an unrelated clean string exercises
// neither leg of the predicate, so it would prove nothing about the widening.
func swallowNearMiss(field string) string {
	return "The repair note records the damaged shape: the body ended with </" + field +
		"><metadata>{…}</metadata> and every parameter after it was lost. " +
		"The sentence continues past the quotation, which is what a quotation does."
}

// swallowRedFixture builds the RED fixture for a field: a swallowed remainder that
// runs to the END of the value, which is what separates a failure from a quotation.
func swallowRedFixture(field string) string {
	return "Prose that was cut short by a mis-serialized call.</" + field +
		"><metadata>{\"evaluate_at\":\"phase-2-boundary\"}</metadata>"
}

// TestSwallowedParamGate_CoversEveryCallerTextField (FAILS-WHEN-ABSENT) gates the
// widened coverage's BEHAVIOUR — one red fixture and one green near-miss PER TOOL
// FAMILY, plus the exclusion and the collision.
//
// THE GREEN LEGS ARE LOAD-BEARING. Without them the whole test is satisfiable by a
// gate that refuses every value containing an angle bracket, which would make
// fourteen prose fields unwritable — a far worse outcome than the defect.
func TestSwallowedParamGate_CoversEveryCallerTextField(t *testing.T) {
	families := []struct {
		name   string
		tool   string
		fields []string
	}{
		{"thoughts_charge", "thoughts", []string{"reasoning"}},
		{"create_plan", "create_plan", []string{"goal", "overview", "sketch", "no_patterns_reason"}},
		{"record_decision", "record_decision", []string{"rationale", "alternatives", "context", "choice"}},
		{"create_research", "create_research", []string{"question", "context", "goal"}},
		{"mutate", "mutate", []string{"conclusion", "evidence", "enforcement", "edge_evidence"}},
	}
	for _, fam := range families {
		t.Run(fam.name, func(t *testing.T) {
			for _, field := range fam.fields {
				raw, err := json.Marshal(map[string]any{field: swallowRedFixture(field)})
				require.NoError(t, err)
				err = rejectSwallowedParamValues(fam.tool, json.RawMessage(raw))
				require.Errorf(t, err, "a mis-serialized %q must be REFUSED", field)
				assert.Containsf(t, err.Error(), field, "the refusal names %q", field)

				green, err := json.Marshal(map[string]any{field: swallowNearMiss(field)})
				require.NoError(t, err)
				assert.NoErrorf(t, rejectSwallowedParamValues(fam.tool, json.RawMessage(green)),
					"prose QUOTING the closing tag mid-sentence must stay writable in %q", field)
			}
		})
	}

	t.Run("regression: the three originally-covered fields still behave", func(t *testing.T) {
		// The widening must not have changed the PREDICATE, only which fields it is
		// pointed at.
		for _, field := range []string{"content", "description", "summary"} {
			raw, err := json.Marshal(map[string]any{field: swallowRedFixture(field)})
			require.NoError(t, err)
			require.Errorf(t, rejectSwallowedParamValues("mutate", json.RawMessage(raw)),
				"%q was covered before the widening and must stay covered", field)

			green, err := json.Marshal(map[string]any{field: swallowNearMiss(field)})
			require.NoError(t, err)
			assert.NoErrorf(t, rejectSwallowedParamValues("mutate", json.RawMessage(green)),
				"and its near-miss must stay writable")
		}
	})

	t.Run("exclusion: findings is NOT scanned", func(t *testing.T) {
		// Without this leg an implementer who added every proposed name — including
		// the id-valued one — passes every other behavioral leg.
		raw, err := json.Marshal(map[string]any{"findings": swallowRedFixture("findings")})
		require.NoError(t, err)
		assert.NoError(t, rejectSwallowedParamValues("mutate", json.RawMessage(raw)),
			"`findings` carries node IDs and is deliberately outside the prose scanner")
	})

	t.Run("collision: an ARRAY-valued evidence is skipped, not errored", func(t *testing.T) {
		// The wire key `evidence` is prose on mutate and an array of node IDs on
		// thoughts(charge). It is safe only because the scanner type-asserts to
		// string and skips a non-string value — a TYPE GUARD, not a design. Pinned
		// so a future refactor of the walk cannot remove it silently.
		raw, err := json.Marshal(map[string]any{
			"operation": "charge", "evidence": []string{"node-a", "node-b"},
		})
		require.NoError(t, err)
		assert.NoError(t, rejectSwallowedParamValues("thoughts", json.RawMessage(raw)),
			"an array-valued evidence must be skipped by the scanner, never refused and never a panic")
	})
}
