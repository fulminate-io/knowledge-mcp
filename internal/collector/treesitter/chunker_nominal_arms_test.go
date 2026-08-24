// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNominalArmsAreRegisteredInPairs holds every nominal-static language to
// having BOTH of its arms registered.
//
// THE TREE STATE IT IS RED IN is a language whose qualifier-type arm is
// registered while its type-facts arm is not. Such a language produces
// qualifier bindings with no type-level facts behind them, so the field hop
// silently finds nothing and the language UNDER-BINDS while every other gate
// stays green. Under-binding leaves no trace in any count that is checked
// elsewhere, which is why the invariant is asserted directly and is always on
// rather than being derived from an end-to-end number.
//
// No real-graph operation may run against a tree in the half-armed state.
func TestNominalArmsAreRegisteredInPairs(t *testing.T) {
	armed := NominalArmedLanguages()
	require.NotEmpty(t, armed, "control: the armed set is non-empty, or the loop below asserts nothing")

	for _, lang := range armed {
		require.Containsf(t, qualifierTypeResolvers, lang,
			"%s is in the armed set with NO qualifier-type arm registered", lang)
		require.Containsf(t, typeFactsResolvers, lang,
			"%s has a qualifier-type arm but NO type-facts arm: its qualifiers bind to types whose "+
				"declarations carry no facts, so the field hop finds nothing and the language "+
				"under-binds silently", lang)
	}

	// KNOWN-POSITIVE CONTROL. Go is not in the armed set and is not expected to
	// be, but it does hold both arms — so a registry read that returned nothing
	// for everybody cannot pass the loop above by accident.
	require.Contains(t, qualifierTypeResolvers, LangGo,
		"control: Go holds a qualifier-type arm, so the assertions above read a populated registry")
	require.Contains(t, typeFactsResolvers, LangGo,
		"control: Go holds a type-facts arm, so the assertions above read a populated registry")
	require.NotContains(t, armed, LangGo,
		"control: the armed set is this group's six languages, not every language holding arms")
}

// TestNominalCensusRowsMatchTheArms holds the capability census and this
// group's armed set to each other.
//
// WHAT IT ASSERTS IS ARM REGISTRATION, AND NOTHING MORE. A true row means "this
// language has a registered type-facts arm", not "this language produces
// declared-conformance edges" — an arm may serve conformance capture, a
// slot-bind derivation or a method-set derivation, and even a
// conformance-capturing arm emits zero edges where the grammar has no contract
// construct. Do not strengthen this test's message past what it checks; the
// edge-production evidence lives in the collector's own end-to-end guard, the
// corpus conformance mode and the spot audit.
//
// THE LOOP IS PER LANGUAGE ON PURPOSE. An aggregate count of true rows is
// satisfied by flipping the wrong six, and a half-applied state — booleans
// flipped, reason left blank — is exactly what a per-row assertion names at the
// language that caused it rather than somewhere downstream.
func TestNominalCensusRowsMatchTheArms(t *testing.T) {
	armed := NominalArmedLanguages()
	require.NotEmpty(t, armed, "control: the armed set is non-empty, or the loop below asserts nothing")

	for _, lang := range armed {
		row, ok := testTypedQualifierCensus[lang]
		require.Truef(t, ok, "%s is armed but carries no census row", lang)
		require.Truef(t, row.QualifierArm,
			"%s is armed but its census row reads QualifierArm false, which reds the landed census "+
				"subtests against correct work", lang)
		require.Truef(t, row.TypeFactsArm,
			"%s is armed but its census row reads TypeFactsArm false", lang)
		require.NotEmptyf(t, row.Reason,
			"%s carries a flipped row with a BLANK reason: every row states what its arms serve", lang)
		require.NotNilf(t, qualifierTypeResolvers[lang], "%s has no registered qualifier-type arm", lang)
		require.NotNilf(t, typeFactsResolvers[lang], "%s has no registered type-facts arm", lang)
	}

	// KNOWN-POSITIVE CONTROL: a language OUTSIDE this group's armed set also
	// reads true/true with a reason, so a test reading an empty table cannot
	// pass the loop above.
	goRow := testTypedQualifierCensus[LangGo]
	require.True(t, goRow.QualifierArm, "control: Go reads armed")
	require.True(t, goRow.TypeFactsArm, "control: Go reads armed")
	require.NotEmpty(t, goRow.Reason, "control: Go states what its arms serve")

	// KNOWN-NEGATIVE CONTROL: a registered language this group does NOT arm
	// still reads false/false, so a table someone flipped wholesale fails here.
	//
	// THE CONTROL LANGUAGE IS ONE THE CENSUS MARKS DELIBERATELY UNARMED rather
	// than merely not-yet-armed, and the distinction is what keeps this leg from
	// evaporating: successive groups have armed the languages that once sat here,
	// and each time this control had to move. Lua's row records that the language
	// declares no types and no conformance at all, which no later group can flip.
	luaRow := testTypedQualifierCensus[LangLua]
	require.False(t, luaRow.QualifierArm, "control: an unarmed language still reads unarmed")
	require.False(t, luaRow.TypeFactsArm, "control: an unarmed language still reads unarmed")
	require.NotEmpty(t, luaRow.Reason, "control: an unarmed row states what the language does not write")
}
