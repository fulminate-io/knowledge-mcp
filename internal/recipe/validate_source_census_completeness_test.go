// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validate_source_census_completeness_test.go gates checkRule's COMPLETENESS
// rather than any one arm's behaviour.
//
// THE FILENAME IS LOAD-BEARING. This file was first called
// validate_source_default_arm_test.go, and Go read the trailing `_arm` as a
// GOARCH build constraint and put the whole file in IgnoredGoFiles: the package
// reported ok with none of these tests in the binary. Any name ending in a GOOS
// or GOARCH token does that silently.
//
// THE SHAPE THIS CLOSES IS THE ONE THE WALK RULE SHIPPED WITH. A rule type added
// with a dispatch arm and no census arm validates SILENTLY and evaluates to zero
// rows, because checkRule's type switch had no default. dispatchRule's loud
// fallthrough cannot catch it: that one fires when the DISPATCH arm is missing,
// which is the arm no author forgets, precisely because omitting it means the
// rule does nothing at all and is noticed immediately.

// censusOrphanRule is a rule type that exists only in this test and deliberately
// has no arm on checkRule's switch — the state a future eleventh rule type is in
// on the day someone adds it and forgets the census.
type censusOrphanRule struct{ Pos Position }

func (r censusOrphanRule) isRule()            {}
func (r censusOrphanRule) Position() Position { return r.Pos }

// censusArmMarker is the substring the default arm's refusal carries. Every
// assertion below is written against THIS rather than against overall success or
// failure, so a rule refused for an unrelated census reason cannot make the
// control lie in either direction.
const censusArmMarker = "has no source-census arm"

func TestValidateAgainstSource_RuleWithNoCensusArmIsRefused(t *testing.T) {
	sv := validatorFixture(t)

	_, _, err := validateAgainstSource(
		&Recipe{Rules: []Rule{censusOrphanRule{Pos: Position{Line: 3, Col: 1}}}}, sv)
	require.Error(t, err, "a rule type with no census arm must be refused, not validated silently")
	assert.Contains(t, err.Error(), censusArmMarker)
	assert.Contains(t, err.Error(), "censusOrphanRule", "the refusal names the offending type")
	assert.Contains(t, err.Error(), "3:1", "and the position it was declared at")
}

// TestValidateAgainstSource_EveryDeclaredRuleHasACensusArm is the control, and it
// is the half that stops the default arm from being a scheduled false failure:
// one value of every declared rule type goes through the real entry point, and
// none of them may reach the default.
//
// The rules are built to be otherwise VALID against the fixture — its vocabulary
// is document/section/paragraph, CONTAINS, and the metadata keys level and
// page_first — so a refusal here would be about the arm rather than about a
// value the census legitimately rejects. The assertion is still scoped to the
// marker, because that is the property under test.
func TestValidateAgainstSource_EveryDeclaredRuleHasACensusArm(t *testing.T) {
	sv := validatorFixture(t)

	name := ExprField{Path: []string{"node", "symbol_name"}}
	rules := []Rule{
		RuleSelect{NodeType: "section"},
		RuleTraverse{EdgeType: "CONTAINS", Direction: "out"},
		RuleWalk{EdgeType: "CONTAINS"},
		RuleFilter{Where: &WhereNode{Exists: &ExistsLeaf{Of: "node.symbol_name"}}},
		RuleBind{Var: "x", Value: name},
		RuleGroupBy{Key: name},
		RuleEmit{NodeType: "pattern", Fields: map[string]Expr{"name": name}},
		RuleLookup{NodeType: "pattern", Identity: name, As: "p"},
		RuleLink{From: name, To: name, Rel: "relates-to"},
		RuleSourceRef{Ref: name},
	}
	require.Len(t, rules, 10, "one value per declared rule type; add one here when a rule is added")

	for _, rule := range rules {
		_, _, err := validateAgainstSource(&Recipe{Rules: []Rule{rule}}, sv)
		if err != nil {
			assert.NotContains(t, err.Error(), censusArmMarker,
				"%T reached checkRule's default arm — it needs a census arm", rule)
		}
	}
}

// TestValidateAgainstSource_CensusArmCountMatchesTheDeclaredRules pins the count
// the control above asserts to the rule set the AST actually declares, so adding
// an eleventh rule type without extending the control is itself a failure rather
// than a silently unexercised type.
func TestValidateAgainstSource_CensusArmCountMatchesTheDeclaredRules(t *testing.T) {
	src, err := os.ReadFile("ast.go")
	require.NoError(t, err)
	declared := strings.Count(string(src), ") isRule()")
	assert.Equal(t, 10, declared,
		"ast.go declares %d rule types; the ten-rule control in this file must list every one", declared)
}
