// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// armedQualifierLanguages is the CLOSED set of languages carrying a
// qualifier-type arm.
//
// IT IS WRITTEN OUT RATHER THAN DERIVED FROM THE REGISTRY, and that is the
// whole point of this test. A subject list read out of the registry would agree
// with the registry by construction and could never catch an arm that landed
// without a decision behind it. Adding a language here is the decision; the
// test is what makes the decision explicit.
// The set spans three groups that arrived separately and are listed as one
// because the registry is one: the systems group (go, rust, swift, cpp, c), the
// nominal-static group (java, kotlin, scala, csharp, php, groovy) and the
// dynamic group (typescript, tsx, javascript, python, ruby).
//
// ELIXIR IS ARMED BUT DOES NOT BELONG HERE, and that is the row worth pausing
// on. It carries a TYPE-FACTS arm and NO qualifier arm, because the language has
// no receiver-dispatch call form for a typed qualifier to bind — so listing it
// would assert a registration that must not exist. This list tracks the
// QUALIFIER registry specifically, not armedness in general.
var armedQualifierLanguages = []Language{
	LangGo, LangRust, LangSwift, LangCPP, LangC,
	LangJava, LangKotlin, LangScala, LangCSharp, LangPHP, LangGroovy,
	LangTypeScript, LangTSX, LangJavaScript, LangPython, LangRuby,
}

// unarmedQualifierControl is the CONTROL half: languages that deliberately
// carry no qualifier arm.
//
// It is not an exhaustive list of unarmed languages — the capability census is
// that, row by row. These exist so this test discriminates: a registry
// populating NOTHING fails the armed half, and one populating EVERYTHING fails
// this half.
//
// ELIXIR IS THE SHARPEST OF THEM and is the reason this set is no longer drawn
// from languages that simply have not been reached yet. It is an ARMED language
// — its type-facts arm captures @behaviour conformance — that still must carry
// NO qualifier arm, so it separates "this registry is empty for the language"
// from "no work has touched the language". Successive groups armed every
// merely-not-yet-armed control this set used to name; a deliberate exclusion
// cannot be armed out from under it.
var unarmedQualifierControl = []Language{LangElixir, LangLua}

// TestQualifierArmRegistrationAllowlist pins which languages carry a
// qualifier-type arm, in both directions.
//
// THE TWO HALVES ARE EACH OTHER'S KNOWN-POSITIVE CONTROL, which is what keeps
// either from passing vacuously.
func TestQualifierArmRegistrationAllowlist(t *testing.T) {
	require.NotEmpty(t, armedQualifierLanguages, "control: the armed set is non-empty, or the loop below asserts nothing")
	require.NotEmpty(t, unarmedQualifierControl, "control: the control set is non-empty, or the loop below asserts nothing")

	for _, lang := range armedQualifierLanguages {
		assert.Containsf(t, qualifierTypeResolvers, lang,
			"%s is on the armed allowlist but no qualifier-types arm is registered for it", lang)
	}

	for _, lang := range unarmedQualifierControl {
		assert.NotContainsf(t, qualifierTypeResolvers, lang,
			"%s is a control language and must carry no qualifier-types arm; adding one is a decision that belongs on the allowlist above", lang)
		assert.Nilf(t, qualifierTypesFor(lang, nil, nil),
			"%s must reach no qualifier-types arm", lang)
	}

	// THE ALLOWLIST IS CLOSED, not merely a lower bound: a language that
	// registers an arm without being named above is exactly the drift this test
	// exists to catch, and asserting only the forward direction would miss it.
	armed := map[Language]bool{}
	for _, lang := range armedQualifierLanguages {
		armed[lang] = true
	}
	for lang := range qualifierTypeResolvers {
		assert.Truef(t, armed[lang],
			"%s has a registered qualifier-types arm but is absent from the closed allowlist", lang)
	}
}
