// SPDX-License-Identifier: Apache-2.0

package tools

// style_rule_summary_cap_test.go covers the ONE parser input class that had
// neither a test nor an implementation: a summary at the server's 500-RUNE cap
// and one rune over it.
//
// IT IS ITS OWN FILE because style_rule_import_test.go is at the repository's
// 500-line source cap, and the class is a self-contained boundary rather than
// another row of the refusal table.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// styleRuleSummaryList builds a one-rule list whose summary is n RUNES of r.
func styleRuleSummaryList(t *testing.T, n int, r rune) string {
	t.Helper()
	return styleRuleListJSON(t, map[string]any{"rules": []any{map[string]any{
		"name": "a", "summary": strings.Repeat(string(r), n), "text": "t", "severity": "warning",
	}}})
}

// TestImportStyleRules_SummaryLengthBoundary drives the 500/501-RUNE class.
//
// WHY THE IMPORT REFUSES IT AT ALL, when the server refuses it too: the server's
// refusal names a BATCH INDEX, and the caller's file is what they can fix. Every
// other refusal this import raises names rules[N]; an over-length summary was the
// one class that reached the server and came back naming a position in a payload
// the author never wrote. The CAP itself is not re-declared here — the check
// calls validate.Summary, whose SummaryMaxLen is the client twin of the server's
// own constant, so the two cannot drift into two different 500s.
//
// THE BOUNDARY IS DRIVEN ON BOTH SIDES and in runes rather than bytes: a
// 500-rune summary of three-byte runes is 1500 bytes and must still be ADMITTED,
// which a byte-counting cap would refuse.
//
// THE MUTATION: delete the length check from validateStyleRuleEntry. The 501 row
// stops being refused client-side, the write is composed and sent, and the
// locator assertion goes red while the relay row below stays green.
func TestImportStyleRules_SummaryLengthBoundary(t *testing.T) {
	t.Run("500 runes is admitted", func(t *testing.T) {
		f := newStyleRuleStoreFake()
		msg, isErr := importRules(t, f, styleRuleSummaryList(t, 500, 'a'), false)
		require.False(t, isErr, "the server admits a 500-rune summary and so must the import: %s", msg)
		require.Len(t, f.plans, 1, "the rule is written")
		assert.Len(t, []rune(f.plans[0].GetNodeBodies()[0].GetSummary()), 500,
			"and it is written WHOLE — the cap refuses, it never clamps")
	})

	t.Run("500 multi-byte runes is admitted too", func(t *testing.T) {
		f := newStyleRuleStoreFake()
		// 1500 bytes. A byte-counting cap would refuse this, and the server
		// (which counts runes) would then have admitted what the client refused.
		msg, isErr := importRules(t, f, styleRuleSummaryList(t, 500, '界'), false)
		require.False(t, isErr, "the cap counts RUNES, as the server's does: %s", msg)
		require.Len(t, f.plans, 1)
	})

	t.Run("501 runes is refused naming the rule and the cap", func(t *testing.T) {
		f := newStyleRuleStoreFake()
		msg, isErr := importRules(t, f, styleRuleSummaryList(t, 501, 'a'), false)
		require.True(t, isErr, "one rune over the server's own cap must be refused, got: %s", msg)
		assert.Contains(t, msg, "rules[0]",
			"the refusal names the position in the AUTHOR'S file — the locator the server's batch index cannot give")
		assert.Contains(t, msg, "summary", "and the field")
		assert.Contains(t, msg, "500", "and the cap it exceeded")
		assert.Empty(t, f.plans,
			"and it is refused BEFORE the batch is composed — nothing reaches the wire")
	})

	t.Run("the mid-list case names the offending rule, not the first", func(t *testing.T) {
		f := newStyleRuleStoreFake()
		over := strings.Repeat("a", 501)
		body := styleRuleListJSON(t, map[string]any{"rules": []any{
			map[string]any{"name": "a", "summary": "sa", "text": "t", "severity": "info"},
			map[string]any{"name": "b", "summary": "sb", "text": "t", "severity": "info"},
			map[string]any{"name": "c", "summary": over, "text": "t", "severity": "info"},
		}})
		msg, isErr := importRules(t, f, body, false)
		require.True(t, isErr, msg)
		assert.Contains(t, msg, "rules[2]", "the third rule is the one named")
		assert.Empty(t, f.plans, "and none of the three is written")
	})

	// THE SERVER'S OWN REFUSAL IS RELAYED, NOT SWALLOWED. This is what the client
	// check ADDS TO rather than replaces: with the client check deleted the write
	// still fails, and this row still passes — it is the locator assertion above
	// that goes red. Driven with the fake refusing the batch, since the client
	// cannot make the real server refuse a payload it now rejects first.
	t.Run("a refusal from the far side is relayed verbatim", func(t *testing.T) {
		f := newStyleRuleStoreFake()
		f.failNext = true
		msg, isErr := importRules(t, f, oneRule, false)
		require.True(t, isErr, "a store-side refusal must surface as an error")
		assert.Contains(t, msg, "store unavailable",
			"the far side's message is relayed, never replaced by a generic one")
	})
}
