// SPDX-License-Identifier: Apache-2.0

// style_rule_reference_test.go — the assembly's style-rule REFERENCE render.
//
// WHAT THESE TESTS OBSERVE. A style rule reaches a ticket or a plan the same way
// any language pattern does, and until this change it was rendered the same way
// too: its whole dsl_pattern in an untruncated fence. A style rule's body is
// prose plus a check shape plus two fixtures, so hydrating one into every
// assembly spends a lane's context on text it did not ask for. The rule is now a
// REFERENCE — an id, a severity and a capped summary — and one instruction line
// tells the reader to fetch the bodies in a single by-ids call.
//
// Every assertion below names the production symbol it observes
// (styleRuleReferenceLine, renderStyleRuleInstruction, styleRefBlockIsWithinBound,
// kgtypes.StyleIndexColumnCap) rather than re-deriving the value it expects: a
// test that reaches its expected value by a path production does not use proves
// only that the test can compute it.
//
// NAMING THE SYMBOL IS NOT THE SAME AS DISCRIMINATING, and this file's own
// history is the lesson. Asserting styleRefBlockIsWithinBound names the helper
// and observes nothing: its answer key is styleRefBlockBound, so multiplying the
// bound by a thousand left this file green. Asserting a capped column against
// kgtypes.StyleIndexColumnCap and kgtypes.StyleIndexEllipsis is the same shape.
// The assertions that PIN those figures are stated in literals, in
// style_rule_reference_bounds_test.go and in kgtypes/style_render_test.go; what
// stays here is the arm that proves the render still routes through them.

package render

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/corpus"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// The fixture bodies. They are distinctive strings rather than realistic ones so
// a NotContains assertion cannot pass by accident against surrounding prose.
const (
	fixtureStyleRuleProse = "NAKED-RETURN-PROSE-BODY: a naked return in a function longer than a " +
		"few lines forces the reader to scan upward for the result names."
	fixtureStyleRuleDSL       = "func $N($$$P) ($$$R) { $$$_ return }"
	fixtureStyleRuleWhere     = `{"kind":{"of":"N","is":"function_declaration"}}`
	fixtureStyleRuleBadBody   = "BAD-FIXTURE-BODY-func f() (err error) { return }"
	fixtureStyleRuleGoodBody  = "GOOD-FIXTURE-BODY-func f() (err error) { return err }"
	fixtureStyleRuleSummary   = "Functions do not use naked returns."
	fixtureOrdinaryPatternDSL = "defer $DB.Close()"
)

// newStyleRuleNode builds a style rule carrying EVERY body the reference render
// must leave behind: the prose, the check shape, the where-tree and both
// fixtures.
func newStyleRuleNode(id, name string) *knowledgev1.Node {
	n := &knowledgev1.Node{
		Id: id, Type: string(kgtypes.NodeFinding), SymbolName: name,
		Summary: fixtureStyleRuleSummary, Description: fixtureStyleRuleProse,
		Source: "test", Status: "active",
	}
	kgtypes.SetValue(n, kgtypes.MetaKeyPracticeKind, kgtypes.PracticeKindStyleRule)
	kgtypes.SetValue(n, corpus.MetaSeverity, "warning")
	kgtypes.SetValue(n, corpus.MetaDSLPattern, fixtureStyleRuleDSL)
	kgtypes.SetValue(n, corpus.MetaCheckWhere, fixtureStyleRuleWhere)
	kgtypes.SetValue(n, corpus.MetaFixtureBad, fixtureStyleRuleBadBody)
	kgtypes.SetValue(n, corpus.MetaFixtureGood, fixtureStyleRuleGoodBody)
	return n
}

// newOrdinaryPatternNode builds a language pattern that is NOT a style rule. Its
// render is unchanged by this ticket, and the golden below is what proves it.
func newOrdinaryPatternNode(id, name string) *knowledgev1.Node {
	n := &knowledgev1.Node{
		Id: id, Type: string(kgtypes.NodeFinding), SymbolName: name,
		Source: "test", Status: "active",
	}
	kgtypes.SetValue(n, corpus.MetaDSLPattern, fixtureOrdinaryPatternDSL)
	return n
}

// styleTicketFixture wires one style rule and one ordinary pattern onto a ticket
// through the audits edge that carries language patterns.
func styleTicketFixture() *graphFixture {
	ticket := &knowledgev1.Node{
		Id: "tkt-style", Type: string(kgtypes.NodeTicket), SymbolName: "t-with-style",
		Status: kgtypes.StatusOpen, Description: "style desc", Summary: "style summary",
	}
	kgtypes.SetValue(ticket, "no_patterns_reason", "fixture")
	return newGraphFixture().
		addKnowledgeNode(ticket).
		addKnowledgeNode(newStyleRuleNode("sr-id", "no-naked-returns")).
		addKnowledgeNode(newOrdinaryPatternNode("lp-id", "lang-pattern-fixture")).
		addKnowledgeEdge("tkt-style", "sr-id", kgtypes.EdgeAudits).
		addKnowledgeEdge("tkt-style", "lp-id", kgtypes.EdgeAudits)
}

// stylePlanFixture is the plan-side twin of styleTicketFixture. Plan assembly
// renders the same section through the same function, and the ruling names plans
// as well as tickets, so every ticket-side assertion has a plan-side sibling.
func stylePlanFixture() *graphFixture {
	plan := &knowledgev1.Node{
		Id: "plan-style", Type: string(kgtypes.NodePlan), SymbolName: "p-with-style",
		Status: "active",
	}
	return newGraphFixture().
		addKnowledgeNode(plan).
		addKnowledgeNode(newStyleRuleNode("sr-id", "no-naked-returns")).
		addKnowledgeNode(newOrdinaryPatternNode("lp-id", "lang-pattern-fixture")).
		addKnowledgeEdge("plan-style", "sr-id", kgtypes.EdgeAudits).
		addKnowledgeEdge("plan-style", "lp-id", kgtypes.EdgeAudits)
}

// --- (1) the reference line and the untouched ordinary pattern, byte-exact ---

// TestGoldenTicketWithStyleRule pins the WHOLE assembled ticket. It is the
// entry that proves both halves of the ruling at once: the style rule renders as
// one reference line, and the ordinary pattern beside it still renders its body
// in a fence exactly as it did before.
func TestGoldenTicketWithStyleRule(t *testing.T) {
	text, err := callRender(context.Background(), styleTicketFixture(), map[string]any{"id": "tkt-style"})
	require.NoError(t, err)
	runGolden(t, "ticket_with_style_rule", text, "tkt-style", "sr-id", "lp-id")
}

// --- (5) the same, for plan assembly ---

func TestGoldenPlanWithStyleRule(t *testing.T) {
	text, err := callRender(context.Background(), stylePlanFixture(), map[string]any{"id": "plan-style"})
	require.NoError(t, err)
	runGolden(t, "plan_with_style_rule", text, "plan-style", "sr-id", "lp-id")
}

// --- (4) no hydrated body reaches the assembly ---

// TestStyleRuleBodiesNeverRendered walks the four bodies a style rule can carry
// and asserts each is absent from the assembled text.
//
// THE KNOWN-POSITIVE IS THE POINT OF THE LAST SUBTEST. Four NotContains
// assertions over an assembly that failed to render anything at all would pass
// vacuously, so the same run asserts the ORDINARY pattern's body IS present
// through the same instrument, field and path.
func TestStyleRuleBodiesNeverRendered(t *testing.T) {
	for _, tc := range []struct {
		name string
		f    *graphFixture
		id   string
	}{
		{"ticket", styleTicketFixture(), "tkt-style"},
		{"plan", stylePlanFixture(), "plan-style"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text, err := callRender(context.Background(), tc.f, map[string]any{"id": tc.id})
			require.NoError(t, err)
			for _, body := range []struct{ what, content string }{
				{"description", fixtureStyleRuleProse},
				{corpus.MetaDSLPattern, fixtureStyleRuleDSL},
				{corpus.MetaCheckWhere, fixtureStyleRuleWhere},
				{corpus.MetaFixtureBad, fixtureStyleRuleBadBody},
				{corpus.MetaFixtureGood, fixtureStyleRuleGoodBody},
			} {
				assert.NotContains(t, text, body.content,
					"a style rule's %s is a REFERENCE's job to point at, never to carry", body.what)
			}
			// The known-positive: the same assembly, the same field, an ordinary
			// pattern whose body still rides.
			assert.Contains(t, text, fixtureOrdinaryPatternDSL,
				"the control: a pattern that is not a style rule keeps today's whole-body render")
			// And the reference itself is there, so the NotContains above are not
			// passing on an empty section.
			assert.Contains(t, text, "sr-id", "the style rule is referenced by id")
			assert.Contains(t, text, fixtureStyleRuleSummary, "the reference carries the rule's summary")
		})
	}
}

// --- (3) the instruction line, once per assembly ---

// TestStyleRuleInstructionOncePerAssembly asserts the count, the payload and the
// absence. The count is the requirement that a per-rule emission would break;
// the payload is what makes the instruction actionable; the absence is what
// keeps every assembly without a style rule byte-identical to before.
func TestStyleRuleInstructionOncePerAssembly(t *testing.T) {
	ticket := &knowledgev1.Node{
		Id: "tkt-two", Type: string(kgtypes.NodeTicket), SymbolName: "t-two-rules",
		Status: kgtypes.StatusOpen, Description: "two rules", Summary: "two rules",
	}
	kgtypes.SetValue(ticket, "no_patterns_reason", "fixture")
	f := newGraphFixture().
		addKnowledgeNode(ticket).
		addKnowledgeNode(newStyleRuleNode("sr-one", "rule-one")).
		addKnowledgeNode(newStyleRuleNode("sr-two", "rule-two")).
		addKnowledgeEdge("tkt-two", "sr-one", kgtypes.EdgeAudits).
		addKnowledgeEdge("tkt-two", "sr-two", kgtypes.EdgeAudits)

	text, err := callRender(context.Background(), f, map[string]any{"id": "tkt-two"})
	require.NoError(t, err)

	assert.Equal(t, 1, strings.Count(text, styleRefInstructionMarker),
		"two style rules, ONE instruction line - a per-rule emission is the mutation this catches")
	// Every referenced id inside ONE by-ids call shape, which is the whole point
	// of the instruction: one read, not one read per rule.
	assert.Contains(t, text, `query(graph:"practice", ids:["sr-one", "sr-two"])`,
		"the call shape names every referenced id in one by-ids read")

	t.Run("absent with no style rule", func(t *testing.T) {
		plain := &knowledgev1.Node{
			Id: "tkt-plain", Type: string(kgtypes.NodeTicket), SymbolName: "t-plain",
			Status: kgtypes.StatusOpen, Description: "plain", Summary: "plain",
		}
		kgtypes.SetValue(plain, "no_patterns_reason", "fixture")
		pf := newGraphFixture().
			addKnowledgeNode(plain).
			addKnowledgeNode(newOrdinaryPatternNode("lp-only", "lang-pattern-fixture")).
			addKnowledgeEdge("tkt-plain", "lp-only", kgtypes.EdgeAudits)
		plainText, perr := callRender(context.Background(), pf, map[string]any{"id": "tkt-plain"})
		require.NoError(t, perr)
		assert.NotContains(t, plainText, styleRefInstructionMarker,
			"no style rule attached, no instruction line")
		assert.Contains(t, plainText, fixtureOrdinaryPatternDSL,
			"the known-positive: the assembly did render, it just had no style rule")
	})
}

// --- (2) the summary is capped, the id never is ---

// TestStyleRuleReferenceCaps drives the cap at its two hazards: a summary far
// past the cap, and a cut that lands mid-rune.
//
// THE ID IS THE OTHER HALF and it is asserted on an id LONGER than the cap: a
// truncated id does not resolve, so capping it would turn the reference into a
// dead pointer while every other assertion here still passed.
func TestStyleRuleReferenceCaps(t *testing.T) {
	longID := "style-rule-" + strings.Repeat("z", kgtypes.StyleIndexColumnCap)
	n := newStyleRuleNode(longID, "long-rule")
	// 500 runes is the summary width the server admits, and every rune here is
	// three bytes, so the cap must cut inside a rune to land on its budget.
	n.Summary = strings.Repeat("界", 500)

	line := styleRuleReferenceLine(n)

	assert.Contains(t, line, longID, "the id is rendered WHOLE - a capped id does not resolve")
	assert.NotContains(t, line, longID+kgtypes.StyleIndexEllipsis)
	summary := styleRefSummaryColumnForTest(t, line)
	assert.LessOrEqual(t, len(summary), kgtypes.StyleIndexColumnCap,
		"the summary column is bounded in BYTES at the declared cap")
	assert.True(t, strings.HasSuffix(summary, kgtypes.StyleIndexEllipsis),
		"a capped column says so rather than truncating silently")
	assert.True(t, utf8.ValidString(summary), "the cut lands on a rune boundary")

	t.Run("a short summary is untouched", func(t *testing.T) {
		short := newStyleRuleNode("sr-short", "short-rule")
		assert.Contains(t, styleRuleReferenceLine(short), fixtureStyleRuleSummary)
		assert.NotContains(t, styleRuleReferenceLine(short), kgtypes.StyleIndexEllipsis)
	})
}

// styleRefSummaryColumnForTest lifts the summary column off a rendered reference
// line. The line's columns are separated by styleRefColumnSeparator, which is
// the production constant the render joins with, so this reads the line the way
// the render wrote it rather than by re-guessing its shape.
func styleRefSummaryColumnForTest(t *testing.T, line string) string {
	t.Helper()
	cols := strings.Split(strings.TrimSuffix(line, "\n"), styleRefColumnSeparator)
	require.GreaterOrEqual(t, len(cols), 3, "a reference line is id, severity and summary at minimum")
	return cols[len(cols)-1]
}

// --- (6) the block's size is bounded by declared constants ---

// TestStyleRuleReferenceBlockWithinBound measures the rendered block against
// styleRefBlockBound, the production helper that owns the arithmetic, over the
// widest inputs the render admits: a maximal severity, a maximal summary, a
// scope on every rule and an id past the column cap.
//
// IT IS THE ROUTING ARM, NOT THE PINNING ARM. The helper supplies its own answer
// key, so a bound loosened on purpose stays green here;
// TestStyleRuleReferenceBlockBoundIsTheDocumentedArithmetic states the same
// budget in literals and is what goes red for that.
func TestStyleRuleReferenceBlockWithinBound(t *testing.T) {
	for _, n := range []int{1, 2, 7} {
		var sb strings.Builder
		ids := make([]string, 0, n)
		for i := range n {
			id := "style-rule-" + strings.Repeat("z", kgtypes.StyleIndexColumnCap) + string(rune('a'+i))
			rule := newStyleRuleNode(id, "wide-rule")
			rule.Summary = strings.Repeat("界", 500)
			// THE MAXIMAL SEVERITY, which this test used to hold at seven bytes
			// while calling itself the widest inputs the render admits. A
			// practice node accepts arbitrary metadata and this render does not
			// validate severity against the ladder, so its width is corpus data.
			kgtypes.SetValue(rule, corpus.MetaSeverity, strings.Repeat("W", 4000))
			kgtypes.SetValue(rule, kgtypes.MetaKeyStyleScopeRepo, strings.Repeat("r", 80))
			kgtypes.SetValue(rule, kgtypes.MetaKeyStyleScopePaths, `["`+strings.Repeat("p", 200)+`"]`)
			sb.WriteString(styleRuleReferenceLine(rule))
			ids = append(ids, id)
		}
		renderStyleRuleInstruction(&sb, ids)
		block := sb.String()
		assert.True(t, styleRefBlockIsWithinBound(ids, block),
			"%d rules: the block is %d bytes against a declared bound of %d",
			n, len(block), styleRefBlockBound(ids))
	}
}

// --- the two controls this render adds, each killed below in review ---

// TestStyleRuleInstructionDeduplicatesIDs covers the one path that can name a
// rule twice: a single rule attached through BOTH pattern edges, which the two
// sections walk independently. Naming it twice inside a by-ids call asks the
// server for the same node twice and tells the reader there are two rules.
func TestStyleRuleInstructionDeduplicatesIDs(t *testing.T) {
	ticket := &knowledgev1.Node{
		Id: "tkt-dup", Type: string(kgtypes.NodeTicket), SymbolName: "t-dup",
		Status: kgtypes.StatusOpen, Description: "dup", Summary: "dup",
	}
	f := newGraphFixture().
		addKnowledgeNode(ticket).
		addKnowledgeNode(newStyleRuleNode("sr-dup", "dup-rule")).
		addKnowledgeEdge("tkt-dup", "sr-dup", kgtypes.EdgeUses).
		addKnowledgeEdge("tkt-dup", "sr-dup", kgtypes.EdgeAudits)

	text, err := callRender(context.Background(), f, map[string]any{"id": "tkt-dup"})
	require.NoError(t, err)
	assert.Contains(t, text, `query(graph:"practice", ids:["sr-dup"])`,
		"one rule reached the assembly through two edges and is named ONCE")
	assert.Equal(t, 1, strings.Count(text, styleRefInstructionMarker))
}

// TestStyleRuleReferenceAbsentColumns pins the marker a column carries when the
// rule left it unset. "The rule says nothing here" and "the render dropped a
// column" have to stay different things a reader can tell apart.
func TestStyleRuleReferenceAbsentColumns(t *testing.T) {
	bare := &knowledgev1.Node{Id: "sr-bare", Type: string(kgtypes.NodeFinding), SymbolName: "bare"}
	kgtypes.SetValue(bare, kgtypes.MetaKeyPracticeKind, kgtypes.PracticeKindStyleRule)

	// THE EXPECTED LINE IS SPELLED OUT, not composed from the constants under
	// test. Building it from styleRefAbsentColumn and styleRefColumnSeparator
	// made this assertion move with any change to either, so inverting the
	// marker to the empty string left the suite green — the render's own value,
	// checked against itself. A literal is what discriminates; the assertion
	// below ties that literal back to the production symbol it pins.
	line := styleRuleReferenceLine(bare)
	assert.Equal(t, "- sr-bare — severity=- — -\n", line)
	assert.Contains(t, line, "severity="+styleRefAbsentColumn,
		"the literal above is styleRefAbsentColumn's rendering, and this names it")
	// The scope column is OMITTED rather than marked absent: a markdown line can
	// drop a column, and most rules carry no scope.
	assert.NotContains(t, line, "repo=")
	assert.NotContains(t, line, "paths=")

	t.Run("a scoped rule renders its scope", func(t *testing.T) {
		scoped := newStyleRuleNode("sr-scoped", "scoped")
		kgtypes.SetValue(scoped, kgtypes.MetaKeyStyleScopeRepo, "knowledge")
		kgtypes.SetValue(scoped, kgtypes.MetaKeyStyleScopePaths, `["cmd/knowledge"]`)
		assert.Contains(t, styleRuleReferenceLine(scoped), "repo=knowledge")
		assert.Contains(t, styleRuleReferenceLine(scoped), "paths=")
	})
}
