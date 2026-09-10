// SPDX-License-Identifier: Apache-2.0

package tools

// style_rule_help_test.go is requirement 7 plus the two documentation surfaces
// this change adds.
//
// THE CONTRACT-KEY CENSUS ANCHORS ON contractKeys, the live slice the corpus
// check gate consults, never on a list transcribed here. A hand-copied list
// would go green on the day it drifted and this file would then be documenting
// the drift rather than catching it — which is exactly how help("patterns")
// came to advertise EIGHT keys while the contract carried nine.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/corpus"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// helpNumberWords maps the counts this census can produce onto the word the
// prose spells them with, so the assertion reads the same sentence a human does.
var helpNumberWords = map[int]string{
	7: "seven", 8: "eight", 9: "nine", 10: "ten", 11: "eleven",
}

// TestHelpPatterns_NamesEveryContractKeyAndCountsThemRight is requirement 7.
func TestHelpPatterns_NamesEveryContractKeyAndCountsThemRight(t *testing.T) {
	require.Len(t, contractKeys, 9, "the contract is nine keys; this census is anchored on that slice")

	for _, key := range contractKeys {
		assert.Contains(t, helpPatterns, key,
			"help(\"patterns\") must name every key the check contract carries")
	}
	// The one that was missing, named explicitly so a regression is legible.
	assert.Contains(t, helpPatterns, corpus.MetaAppliesToTests,
		"applies_to_tests is a contract key and was the one help(\"patterns\") omitted")

	word, ok := helpNumberWords[len(contractKeys)]
	require.True(t, ok, "no spelled number for a %d-key contract", len(contractKeys))
	assert.Contains(t, helpPatterns, "The "+word+" contract keys:",
		"the PROSE COUNT must agree with the number of keys listed — the two disagreeing "+
			"is the defect requirement 7 names")

	// KNOWN-POSITIVE CONTROL, on the same instrument in the same run: holing the
	// help must make the census report the hole. Without it a matcher broken into
	// always-matching would satisfy every assertion above.
	holed := strings.ReplaceAll(helpPatterns, corpus.MetaAppliesToTests, "")
	assert.NotContains(t, holed, corpus.MetaAppliesToTests,
		"control: the containment instrument reports a miss when one exists")
	assert.NotContains(t, helpPatterns, "The eight contract keys:",
		"the superseded count must be gone, not merely joined by the new one")
}

// TestHelpPatterns_DocumentsTheStyleRuleVocabulary pins that the carrier this
// ticket defines is DOCUMENTED where a reader of the patterns contract looks.
func TestHelpPatterns_DocumentsTheStyleRuleVocabulary(t *testing.T) {
	for _, key := range []string{
		kgtypes.MetaKeyPracticeKind, kgtypes.PracticeKindStyleRule,
		kgtypes.MetaKeyStyleScopeRepo, kgtypes.MetaKeyStyleScopePaths,
		kgtypes.MetaKeyLinterName, kgtypes.MetaKeyLinterRuleID,
		kgtypes.MetaKeySisterCheck, kgtypes.MetaKeySisterPractice,
	} {
		assert.Contains(t, helpPatterns, key,
			"help(\"patterns\") must document the style-rule key %q — this ticket DEFINES the "+
				"carrier and nothing writes the cross-link, so the documentation is the only "+
				"place a later author learns the spelling", key)
	}
	assert.Contains(t, helpPatterns, "AUTHORED BY ITS OWN TOOL CALL",
		"the help must say a check is never created by an import, batch, landing or migration")
	assert.Contains(t, helpPatterns, "No import, batch, landing or migration",
		"and must name the four venues that do not create one")
}

// TestHelpQuery_DocumentsTheStyleIndexMode covers the mode census's own rule
// (the query help names every declared mode) for the mode this change adds, and
// the two selection inputs a caller cannot guess.
func TestHelpQuery_DocumentsTheStyleIndexMode(t *testing.T) {
	assert.Contains(t, helpQuery, `"style_index"`,
		"the mode census matches a QUOTED token, so the help must name it that way")
	assert.Contains(t, helpQuery, "path_prefixes",
		"the help must name the path selector, which is not derivable from the mode name")
	assert.Contains(t, helpQuery, "path-segment boundaries",
		"and the matching rule, since `pkg` admitting `pkgextra` is the mistake it prevents")
}

// TestHelpManage_DocumentsTheImportOperation covers the operation census's rule
// for the operation this change adds.
func TestHelpManage_DocumentsTheImportOperation(t *testing.T) {
	assert.Contains(t, helpManage, `"operation": "`+OpStyleRulesImport+`"`,
		"the operation census demands a worked call, not merely the word")
	assert.Contains(t, helpManage, "No check node, no fixture node and",
		"the help must say what the import does NOT write")
	assert.Contains(t, helpManage, "UPDATED per field rather than re-created",
		"and why re-importing the same list is idempotent")
}

// TestHelpPatterns_SaysAStyleRuleAssemblesAsAReference is the documentation half
// of the assembly render.
//
// WHY IT BELONGS IN help("patterns"). A reader who attaches a style rule to a
// ticket learns the vocabulary here, and the one thing the vocabulary does not
// tell them is what an assembly will DO with the rule: it renders a one-line
// reference and drops the body. Without that sentence the reader who wants the
// prose reaches for one lookup per rule, which is the loop the instruction line
// in the assembly exists to prevent — and they meet it only after the assembly
// has already spent their context.
func TestHelpPatterns_SaysAStyleRuleAssemblesAsAReference(t *testing.T) {
	const claim = "A STYLE RULE IN AN ASSEMBLY IS A REFERENCE, NEVER A HYDRATED BODY."
	assert.Contains(t, helpPatterns, claim,
		"the help must say what a ticket or plan assembly does with an attached rule")
	assert.Contains(t, helpPatterns, `query(graph:"practice", ids:["<rule id>", ...])`,
		"and must carry the BULK-READ call shape, since naming the limitation without "+
			"the remedy teaches the per-rule loop by omission")
	assert.Contains(t, helpPatterns, "never one call per rule",
		"and must say the read is one call, not one per rule")

	// KNOWN-POSITIVE CONTROL on the same instrument in the same run.
	holed := strings.ReplaceAll(helpPatterns, claim, "")
	assert.NotContains(t, holed, claim,
		"control: the containment instrument reports a miss when one exists")
}
