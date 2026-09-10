// SPDX-License-Identifier: Apache-2.0

// style_rule_reference_paths_test.go — one SOLE-EDGE fixture per collection path
// a style rule can reach an assembly by.
//
// WHY SOLE-EDGE. A style rule reaches a ticket through two independent
// collectors — ticket.go's EdgeUses case feeds renderTicketPatterns, its
// EdgeAudits case feeds renderLanguagePatternsSection — and reaches a plan
// through a third, plan.go's own EdgeAudits case. A fixture that attaches ONE
// rule through TWO of them observes neither path: with the uses-edge branch
// deleted the whole suite stayed green, because the surviving section still put
// the id on the instruction line and every assertion read the assembly whole.
// Each fixture below therefore attaches its rule by ONE edge and asserts the
// reference line INSIDE the section that path renders into.
//
// THREE PATHS, THREE MUTATIONS, TWO BRANCHES. The ticket-audits and plan-audits
// paths share renderLanguagePatternsSection, so there is no third reference
// branch to delete; the third discriminator is the plan-side COLLECTOR:
//
//	delete renderTicketPatterns' isStyleRuleNode branch           → uses path RED, other two green
//	delete renderLanguagePatternsSection's isStyleRuleNode branch → both audits paths RED, uses green
//	delete plan.go's EdgeAudits collection case                   → plan path RED, both ticket paths green
//
// EVERY EXPECTED VALUE HERE IS A LITERAL. The reference lines, the control
// lines and the section names are spelled out rather than composed from
// styleRefColumnSeparator, styleRefAbsentColumn or kgtypes.CapStyleColumn: a
// value built from the symbol under test moves with it and pins nothing.

package render

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/corpus"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// fixtureStyleRuleLongSummary is 89 bytes, so the reference render cuts it at
// the column cap and marks the cut. It is on the uses-edge fixture rather than
// on all of them so that path's golden pins the ELLIPSIS SPELLING as rendered
// bytes: no other golden in the tree carries a capped column.
const fixtureStyleRuleLongSummary = "Loops over a map are sorted by key before iteration, never left to Go's random map order."

// styleUsesEdgeTicketFixture attaches a style rule and an ordinary pattern to a
// ticket through EdgeUses ALONE — the edge ticket.go collects into `patterns`,
// which renderTicketPatterns renders as the `## Patterns` section.
func styleUsesEdgeTicketFixture() *graphFixture {
	ticket := &knowledgev1.Node{
		Id: "tkt-uses", Type: string(kgtypes.NodeTicket), SymbolName: "t-uses-edge",
		Status: kgtypes.StatusOpen, Description: "uses desc", Summary: "uses summary",
	}
	rule := newStyleRuleNode("sr-uses", "no-random-map-order")
	rule.Summary = fixtureStyleRuleLongSummary
	return newGraphFixture().
		addKnowledgeNode(ticket).
		addKnowledgeNode(rule).
		addKnowledgeNode(newOrdinaryPatternNode("lp-uses", "lang-pattern-fixture")).
		addKnowledgeEdge("tkt-uses", "sr-uses", kgtypes.EdgeUses).
		addKnowledgeEdge("tkt-uses", "lp-uses", kgtypes.EdgeUses)
}

// assemblySection lifts one `## <header>` section off a rendered assembly, so an
// assertion can say WHICH section a line landed in. A Contains over the whole
// text cannot: a rule attached by two edges renders into two sections, and that
// is exactly the confusion the sole-edge fixtures exist to remove.
func assemblySection(t *testing.T, text, header string) string {
	t.Helper()
	marker := "\n## " + header + "\n"
	i := strings.Index(text, marker)
	require.GreaterOrEqual(t, i, 0, "the assembly has no %q section:\n%s", header, text)
	rest := text[i+len(marker):]
	if before, _, ok := strings.Cut(rest, "\n## "); ok {
		return before
	}
	return rest
}

// TestStyleRuleReferenceReachesEveryCollectionPath drives each collection path
// on its own edge and asserts the reference line, the ordinary-pattern control
// in the SAME section on the SAME run, and the instruction line.
func TestStyleRuleReferenceReachesEveryCollectionPath(t *testing.T) {
	for _, tc := range []struct {
		name     string
		fixture  func() *graphFixture
		hostID   string
		section  string
		wantLine string
		control  string
	}{
		{
			name:    "ticket, uses edge, into the Patterns section",
			fixture: styleUsesEdgeTicketFixture,
			hostID:  "tkt-uses",
			section: "Patterns",
			// The summary is cut at the column cap and the cut is marked.
			wantLine: "- sr-uses — severity=warning — Loops over a map are sorted by key before iteration, neve...\n",
			// The control: the pattern that is NOT a style rule keeps the
			// Patterns section's status-and-id render, in this same run.
			control: "- [active] lang-pattern-fixture — ID: lp-uses\n",
		},
		{
			name:     "ticket, audits edge, into the Language patterns section",
			fixture:  styleTicketFixture,
			hostID:   "tkt-style",
			section:  "Language patterns",
			wantLine: "- sr-id — severity=warning — Functions do not use naked returns.\n",
			control:  "- lp-id — lang-pattern-fixture\n\n```\ndefer $DB.Close()\n```\n",
		},
		{
			name:     "plan, audits edge, into the Language patterns section",
			fixture:  stylePlanFixture,
			hostID:   "plan-style",
			section:  "Language patterns",
			wantLine: "- sr-id — severity=warning — Functions do not use naked returns.\n",
			control:  "- lp-id — lang-pattern-fixture\n\n```\ndefer $DB.Close()\n```\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text, err := callRender(context.Background(), tc.fixture(), map[string]any{"id": tc.hostID})
			require.NoError(t, err)

			section := assemblySection(t, text, tc.section)
			assert.Contains(t, section, tc.wantLine,
				"the reference line must be rendered by THIS path's section, %q", tc.section)
			assert.Contains(t, section, tc.control,
				"the same-run control: a pattern that is not a style rule keeps its own render")
			assert.Contains(t, text, styleRefInstructionMarker,
				"a referenced rule always brings the one bulk-read instruction with it")

			// No hydrated body reaches the assembly on THIS path either. The
			// prose is the discriminating arm (the render has a description to
			// print); the check shape is the regression guard.
			assert.NotContains(t, text, fixtureStyleRuleProse,
				"a style rule's prose is a reference's job to point at, never to carry")
			assert.NotContains(t, text, fixtureStyleRuleDSL,
				"and neither is its check shape")
		})
	}
}

// TestGoldenTicketWithStyleRuleOnUsesEdge is the uses path's golden. The two
// audits paths have theirs already (ticket_with_style_rule, plan_with_style_rule);
// this one completes the set and is the only golden in the tree whose bytes
// carry a capped column, so a change to the ellipsis spelling moves it.
func TestGoldenTicketWithStyleRuleOnUsesEdge(t *testing.T) {
	text, err := callRender(context.Background(), styleUsesEdgeTicketFixture(), map[string]any{"id": "tkt-uses"})
	require.NoError(t, err)
	runGolden(t, "ticket_with_style_rule_uses_edge", text, "tkt-uses", "sr-uses", "lp-uses")
}

// TestStyleRuleMarkerIsAValueCheckNotAPresenceCheck closes the cell the marker's
// own design depends on: a practice node carrying a practice_kind whose value is
// NOT style_rule renders exactly as it did before this change.
//
// THE ASSERTION IS AN EQUALITY BETWEEN TWO RENDERS, neither of them a constant
// this file owns: the same node with practice_kind="pattern" and the same node
// with no practice_kind key at all must produce byte-identical assemblies. That
// makes "the prior render" the answer key rather than any literal I could write.
//
// THE MUTATION: turn isStyleRuleNode into a presence check
// (`kgtypes.Value(n, kgtypes.MetaKeyPracticeKind) != ""`) and this goes red —
// the marked node renders as a reference line while its unmarked twin renders
// its body.
func TestStyleRuleMarkerIsAValueCheckNotAPresenceCheck(t *testing.T) {
	// "pattern" is a deliberate literal, not a kgtypes constant: the vocabulary
	// holds exactly one value today, and the point of this cell is the value the
	// vocabulary does NOT hold.
	const otherKind = "pattern"

	build := func(kind string) *graphFixture {
		ticket := &knowledgev1.Node{
			Id: "tkt-kind", Type: string(kgtypes.NodeTicket), SymbolName: "t-kind",
			Status: kgtypes.StatusOpen, Description: "kind desc", Summary: "kind summary",
		}
		kgtypes.SetValue(ticket, "no_patterns_reason", "fixture")
		lp := newOrdinaryPatternNode("lp-kind", "other-kind-pattern")
		lp.Summary = fixtureStyleRuleSummary
		kgtypes.SetValue(lp, corpus.MetaSeverity, "warning")
		if kind != "" {
			kgtypes.SetValue(lp, kgtypes.MetaKeyPracticeKind, kind)
		}
		return newGraphFixture().
			addKnowledgeNode(ticket).
			addKnowledgeNode(lp).
			addKnowledgeEdge("tkt-kind", "lp-kind", kgtypes.EdgeAudits)
	}

	marked, err := callRender(context.Background(), build(otherKind), map[string]any{"id": "tkt-kind"})
	require.NoError(t, err)
	unmarked, err := callRender(context.Background(), build(""), map[string]any{"id": "tkt-kind"})
	require.NoError(t, err)

	assert.Equal(t, unmarked, marked,
		"practice_kind=%q is not style_rule, so the node keeps the render it had before "+
			"this change — the marker is a VALUE check, never a presence check", otherKind)
	// The known-positive on the same instrument: the render under test did run
	// and does carry the body a style rule would have replaced.
	assert.Contains(t, marked, fixtureOrdinaryPatternDSL,
		"control: the assembly rendered, and rendered the pattern's whole body")
	assert.NotContains(t, marked, styleRefInstructionMarker,
		"no style rule was attached, so no bulk-read instruction is emitted")
}
