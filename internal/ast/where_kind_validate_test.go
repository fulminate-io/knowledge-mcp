// SPDX-License-Identifier: Apache-2.0

// where_kind_validate_test.go — compile-time refusal of where-tree kind leaves
// naming a kind the grammar does not have, the four-way control ladder that
// keeps a valid-but-absent kind distinguishable from a bogus one, and the
// single-vocabulary guarantee shared with operation=list_node_kinds.

package ast

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// ladderPattern binds $F to the callee identifier of a call, which is what lets
// a kind leaf on F be true (identifier), false-but-legitimate (type_identifier)
// or unspellable (identifierr) over one fixture.
const ladderPattern = "$F($$$A)"

// ladderFixture is one call expression: enough for the pattern to match, small
// enough that a zero is unambiguous.
const ladderFixture = `package main

func A() {
	alpha(beta)
}
`

// kindWhere is the one-leaf where-tree the ladder varies.
func kindWhere(is string) *WhereNode {
	return &WhereNode{Kind: &KindLeaf{Of: "F", Is: []string{is}}}
}

// ladderRun mirrors what the tool handlers do, in their order: validate the
// where-tree against the grammar first and walk only if it holds. The handlers
// are the real thing — TestAstWhereKind_ValidatedOnMatchCountAndReplace in the
// tools package pins that all three of them call the validator — but the ladder
// needs both halves behind ONE probe, because its whole subject is that two
// calls which used to return the same answer no longer do.
func ladderRun(t *testing.T, dir string, where *WhereNode) ([]RawMatch, error) {
	t.Helper()
	if err := ValidateWhereKinds(where, treesitter.LangGo); err != nil {
		return nil, err
	}
	raws, _, err := honestyMatch(t, dir, treesitter.LangGo, ladderPattern, where, Scope{})
	return raws, err
}

// TestWhereKind_UnknownKindErrorsWithSuggestion pins the rejection and the
// message it carries: the offending kind, the language, and the closest valid
// spellings — never the whole vocabulary, which for the larger grammars would
// bury the answer.
func TestWhereKind_UnknownKindErrorsWithSuggestion(t *testing.T) {
	vocabulary, ok := NodeKinds(treesitter.LangGo)
	require.True(t, ok)
	require.Contains(t, vocabulary, "identifier",
		"setup: the near miss being suggested has to be a real kind")
	require.NotContains(t, vocabulary, "identifierr",
		"setup: the rejected name has to be one the grammar really lacks")

	t.Run("names the kind and suggests the near miss", func(t *testing.T) {
		err := ValidateWhereKinds(kindWhere("identifierr"), treesitter.LangGo)
		require.Error(t, err, "a kind the grammar lacks is refused, not walked")
		msg := err.Error()
		assert.Contains(t, msg, "identifierr", "the error names the offending kind")
		assert.Contains(t, msg, "for language go", "and the language it was resolved against")
		assert.Contains(t, msg, "did you mean")
		// The QUOTED form on purpose: bare `identifier` is a substring of
		// `identifierr`, so asserting it unquoted would pass on the echoed
		// offending name alone and prove nothing about the suggestion.
		assert.Contains(t, msg, `"identifier"`, "the near miss is offered by name")
	})

	t.Run("known positive: a real kind is accepted", func(t *testing.T) {
		// Without this the rejection above is satisfiable by a validator that
		// refuses everything, which would break every legitimate kind filter.
		assert.NoError(t, ValidateWhereKinds(kindWhere("identifier"), treesitter.LangGo))
	})

	t.Run("no near miss means no invented suggestion", func(t *testing.T) {
		err := ValidateWhereKinds(kindWhere("zzqqxxwwvv"), treesitter.LangGo)
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "did you mean",
			"nothing in the grammar is close, so offering a suggestion would misdirect")
		assert.Contains(t, err.Error(), "list_node_kinds",
			"the caller is pointed at the full vocabulary instead")
	})

	t.Run("nested leaves are reached too", func(t *testing.T) {
		// A kind leaf buried in a composer or a sub-pattern is exactly as
		// undecidable during the walk as one at the top, so each shape must be
		// refused the same way.
		for name, where := range map[string]*WhereNode{
			"all":              {All: []*WhereNode{kindWhere("identifierr")}},
			"any":              {Any: []*WhereNode{kindWhere("identifierr")}},
			"not":              {Not: kindWhere("identifierr")},
			"inside_pattern":   {InsidePattern: &SubPatternLeaf{Of: "$match", Pattern: "func $N() { $$$B }", Where: kindWhere("identifierr")}},
			"contains_pattern": {ContainsPattern: &SubPatternLeaf{Of: "$match", Pattern: "$X", Where: kindWhere("identifierr")}},
		} {
			t.Run(name, func(t *testing.T) {
				err := ValidateWhereKinds(where, treesitter.LangGo)
				require.Error(t, err)
				assert.Contains(t, err.Error(), "identifierr")
			})
		}
	})

	t.Run("no filter and unregistered language are not this check's errors", func(t *testing.T) {
		assert.NoError(t, ValidateWhereKinds(nil, treesitter.LangGo))
		assert.NoError(t, ValidateWhereKinds(kindWhere("identifierr"), treesitter.Language("klingon")),
			"an unregistered language is the caller's own check to report, not a kind error")
	})
}

// TestWhereKind_ControlLadderDistinguishesAbsentFromBogus is the four-way
// ladder. Rungs 1 and 2 establish the pattern and the filter both work; rung 3
// is the one that must NOT change — a real kind that simply does not occur is a
// clean zero, and a validator that errored there would break legitimate
// searches. Rung 4 is the fix: the bogus kind, which used to return rung 3's
// answer exactly, now errors.
func TestWhereKind_ControlLadderDistinguishesAbsentFromBogus(t *testing.T) {
	vocabulary, ok := NodeKinds(treesitter.LangGo)
	require.True(t, ok)
	require.Contains(t, vocabulary, "type_identifier",
		"setup: rung 3 is only meaningful if its kind is genuinely valid")
	require.NotContains(t, vocabulary, "identifierr")

	dir := fixtureRepo(t, map[string]string{"main.go": ladderFixture})

	unfiltered, err := ladderRun(t, dir, nil)
	require.NoError(t, err)
	require.NotEmpty(t, unfiltered, "rung 1: unfiltered, the pattern matches")

	present, err := ladderRun(t, dir, kindWhere("identifier"))
	require.NoError(t, err)
	assert.NotEmpty(t, present, "rung 2: a valid kind that IS present still matches")

	absent, err := ladderRun(t, dir, kindWhere("type_identifier"))
	require.NoError(t, err, "rung 3: a valid kind that is absent is a legitimate search")
	assert.Empty(t, absent, "and its answer is a clean zero, with no error")

	bogus, err := ladderRun(t, dir, kindWhere("identifierr"))
	require.Error(t, err, "rung 4: the kind the grammar lacks is refused")
	assert.Empty(t, bogus)
	assert.Contains(t, err.Error(), "identifierr")

	// The property the ladder exists for, stated once more as a comparison
	// rather than as two separate verdicts: rungs 3 and 4 both returned an
	// empty result, so the ERROR is the only thing telling them apart.
	assert.Empty(t, absent)
	assert.Empty(t, bogus)
	require.NoError(t, ValidateWhereKinds(kindWhere("type_identifier"), treesitter.LangGo))
	require.Error(t, ValidateWhereKinds(kindWhere("identifierr"), treesitter.LangGo))
}

// TestWhereKind_VocabularyMatchesListNodeKinds pins the single-vocabulary
// guarantee. The validator and operation=list_node_kinds read NodeKinds, so a
// name the tool prints can never be a name the validator rejects; the
// behavioral half of that parity — the handler actually calling it — is pinned
// by TestAstListNodeKinds_UsesSharedVocabulary in the tools package, which is
// where the handler lives.
func TestWhereKind_VocabularyMatchesListNodeKinds(t *testing.T) {
	vocabulary, ok := NodeKinds(treesitter.LangGo)
	require.True(t, ok)
	require.Greater(t, len(vocabulary), 50,
		"setup: an empty vocabulary would make every assertion below vacuous")

	// The same anchors the list_node_kinds test asserts, so a drift in either
	// direction shows up as a disagreement between the two tests.
	for _, kind := range []string{"function_declaration", "method_declaration", "call_expression", "identifier"} {
		assert.Contains(t, vocabulary, kind)
	}

	// Anonymous tokens are absent from what a kind leaf may name — and absent
	// by CLASSIFICATION, not by the grammar simply lacking them, which is what
	// the anonymous set proves.
	vocab, ok := newKindVocabulary(treesitter.LangGo)
	require.True(t, ok)
	for _, token := range []string{"func", "+"} {
		assert.NotContains(t, vocabulary, token, "an anonymous token is not a named kind")
		assert.Contains(t, vocab.anonymous, token, "but the grammar does carry it as a token")
	}

	// Every name the tool prints is a name the validator accepts. The bogus
	// control below is what keeps this zero honest: without it, a validator
	// that accepted everything would satisfy the loop just as well.
	rejected := []string{}
	for _, kind := range vocabulary {
		if err := ValidateWhereKinds(kindWhere(kind), treesitter.LangGo); err != nil {
			rejected = append(rejected, kind)
		}
	}
	assert.Empty(t, rejected, "the validator rejects nothing list_node_kinds prints")
	require.Error(t, ValidateWhereKinds(kindWhere("identifierr"), treesitter.LangGo),
		"known positive: the same call path does reject a name outside the vocabulary")

	// An anonymous token is spelled perfectly — telling that caller they have a
	// typo would send them hunting for a mistake they did not make. This is the
	// route callers actually take: operation=explain prints these, and
	// operation=list_node_kinds does not.
	err := ValidateWhereKinds(kindWhere("func"), treesitter.LangGo)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "anonymous token")
	assert.NotContains(t, err.Error(), "did you mean",
		"a correctly spelled token needs the anonymous-token message, not a spelling suggestion")
}

// TestClosestKinds_RanksNearestFirstAndBounds covers the suggester directly:
// it is the one genuinely-new unit here, and its ordering is what decides
// whether the message's first suggestion is the useful one.
func TestClosestKinds_RanksNearestFirstAndBounds(t *testing.T) {
	vocabulary, ok := NodeKinds(treesitter.LangGo)
	require.True(t, ok)

	hits := closestKinds("identifierr", vocabulary)
	require.NotEmpty(t, hits)
	assert.Equal(t, "identifier", hits[0], "the nearest candidate leads")
	assert.LessOrEqual(t, len(hits), maxKindSuggestions, "the list is bounded")
	assert.Equal(t, hits, closestKinds("identifierr", vocabulary),
		"and is deterministic, so the error message does not vary run to run")

	assert.Empty(t, closestKinds("zzqqxxwwvv", vocabulary),
		"nothing within the distance bound yields no suggestion rather than a far one")

	assert.Equal(t, []int{0, 3, 3, 1},
		[]int{
			editDistance("abc", "abc"),
			editDistance("", "abc"),
			editDistance("kitten", "sitting"),
			editDistance("identifier", "identifierr"),
		})
}
