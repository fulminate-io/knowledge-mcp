// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// flowCapabilityRow is one language's flow-arm capability, with the reason its
// arm exists or does not.
type flowCapabilityRow struct {
	// FlowArm reports a registered flow-step arm.
	FlowArm bool

	// Reason states WHAT THIS ROW'S ARM SERVES, or what the language does not
	// write. IT IS REQUIRED ON EVERY ROW, WITHOUT EXCEPTION — armed and unarmed
	// alike. That is a uniform rule with no classes, no marking field and no
	// judgment call, and is therefore gateable:
	// every_row_states_what_its_arm_serves is the gate, and it is live from the
	// moment this table exists rather than dormant until some later row lands.
	Reason string
}

// testFlowStepsCensus is the per-language flow-arm table.
//
// IT IS READ BY NOTHING OUTSIDE THIS PACKAGE, and that is deliberate rather than
// incidental. It is an unexported var in a _test.go file, which Go compiles only
// into this package's own test binary, so no other package can reference it
// under any import. A consumer that needs to know whether a language is armed
// calls FlowStepsArm, which is exported for exactly that purpose. Do not
// introduce a cross-package reader for this table.
var testFlowStepsCensus = map[Language]flowCapabilityRow{
	// THE RULED FIFTEEN.
	LangGo: {FlowArm: true,
		Reason: "the reference arm every other is written against; its walk and its normalizeCallee use are what the other fourteen copy"},
	LangJavaScript: {FlowArm: true,
		Reason: "the ECMAScript arm; `this` is a keyword rather than a named parameter, so a field write records the parameter on the right-hand side and nothing seeds `this` itself"},
	LangTypeScript: {FlowArm: true,
		Reason: "the ECMAScript arm with the typed parameter forms; required_parameter and optional_parameter wrap the name beside its annotation"},
	LangTSX: {FlowArm: true,
		Reason: "the ECMAScript arm on its own symbol table; tsx numbers the same kind names differently from typescript, so a shared table would classify its nodes wrongly rather than not at all"},
	LangPython: {FlowArm: true,
		Reason: "the dynamic arm; `self` and `cls` are receivers rather than position zero, and the bare keyword separator consumes no position"},
	LangRuby: {FlowArm: true,
		Reason: "the dynamic arm; it owns the plan's proof that a callee spelling can carry an @, because an instance-variable receiver composes one"},
	LangRust: {FlowArm: true,
		Reason: "the systems arm; the self_parameter takes no position, and a reference expression unwraps to its operand"},
	LangC: {FlowArm: true,
		Reason: "the systems arm; address-of and dereference unwrap to their operand, and the parenthesized pointer-call form is followed because the Calls query captures it"},
	LangCPP: {FlowArm: true,
		Reason: "the systems arm; it owns the plan's proof that a callee spelling can carry a separator, since `ptr->run` and `Ns::fn` are both nameable"},
	LangCSharp: {FlowArm: true,
		Reason: "the nominal-static arm; C# declares no named `this` node, so the receiver is recognized by text inside a member access"},
	LangGroovy: {FlowArm: true,
		Reason: "the nominal-static arm; the vendored grammar errors on a this-qualified write, so groovy records no receiver-field write at all — a grammar boundary rather than a missing arm"},
	LangJava: {FlowArm: true,
		Reason: "the nominal-static arm; its callee span is composed from an object field and a name field, matching the two-capture Calls query"},
	LangKotlin: {FlowArm: true,
		Reason: "the nominal-static arm; its arguments sit under a call_suffix, which is also where a trailing lambda lands and therefore where a call with no parenthesized argument correctly records nothing"},
	LangScala: {FlowArm: true,
		Reason: "the nominal-static arm; only an explicit return records a result flow, because the idiomatic last-expression form is not distinguishable at this layer"},
	LangSwift: {FlowArm: true,
		Reason: "its own arm rather than a nominal-static member, matching its separate qualifier arm; a labeled argument still occupies its ordinal position"},

	// THE EXCLUSIONS, each carrying its own justification rather than being
	// silent. A language absent from the ruled fifteen is a DECISION, and the
	// reason is what makes it re-examinable.
	LangPHP: {
		Reason: "excluded by the ruling: the ast DSL denies PHP outright for a placeholder-sigil collision, so the flow leaf that reads these facts could never be exercised there"},
	LangBash: {
		Reason: "excluded by the ruling: no binder — a shell callee is a command word rather than a name, which is why bash also takes no callee-profile row"},
	LangElixir: {
		Reason: "excluded by the ruling: no binder — its qualifier arm is absent too, so there is no name-to-value binding for a closure to run over"},
	LangElm: {
		Reason: "excluded by the ruling: no binder"},
	LangLua: {
		Reason: "excluded by the ruling: no binder, and it is also the one language whose chained tail the cut-keyed decline cannot reach"},
	LangOCaml: {
		Reason: "excluded by the ruling: no binder"},

	// The non-code grammars. None declares a function with parameters, so there
	// is no flow for an arm to state.
	LangCSS:        {Reason: "a stylesheet grammar: no parameters, no calls, nothing for a flow arm to state"},
	LangHTML:       {Reason: "a markup grammar: no parameters, no calls, nothing for a flow arm to state"},
	LangSQL:        {Reason: "a query grammar: this collector reads it for structure rather than for dataflow"},
	LangHCL:        {Reason: "a configuration grammar: no parameters, no calls, nothing for a flow arm to state"},
	LangProtobuf:   {Reason: "an IDL grammar: it declares message shapes rather than executable bodies"},
	LangDockerfile: {Reason: "a build-recipe grammar: its instructions are not parameterized bodies"},
	LangSvelte:     {Reason: "a component grammar: its script blocks are not chunked as declarations with parameters here"},
	LangToml:       {Reason: "a data grammar: no parameters, no calls, nothing for a flow arm to state"},
	LangYaml:       {Reason: "a data grammar: no parameters, no calls, nothing for a flow arm to state"},
	LangMarkdown:   {Reason: "a prose grammar: no parameters, no calls, nothing for a flow arm to state"},
	LangCue:        {Reason: "a configuration grammar: no parameters, no calls, nothing for a flow arm to state"},
}

// ruledFlowLanguages is the plan-mandated armed set, written as LITERALS because
// it is a RULING rather than a measurement: the point of the assertion below is
// that the registry agrees with a decision made outside the code, so deriving
// this list from the registry would compare the registry with itself.
var ruledFlowLanguages = []Language{
	LangGo, LangJavaScript, LangTypeScript, LangTSX, LangPython, LangRust, LangC, LangCPP,
	LangCSharp, LangGroovy, LangJava, LangKotlin, LangRuby, LangScala, LangSwift,
}

// TestFlowStepsCensus holds the per-language flow table to the registry it
// describes, and holds both to the ruling.
func TestFlowStepsCensus(t *testing.T) {
	t.Run("every_row_states_what_its_arm_serves", func(t *testing.T) {
		require.NotEmpty(t, testFlowStepsCensus,
			"control: the table is non-empty, or every assertion here is vacuous")
		for lang, row := range testFlowStepsCensus {
			assert.NotEmptyf(t, row.Reason,
				"%s has no reason: state what its arm serves, or what the language does not write", lang)
			_, armed := FlowStepsArm(lang)
			assert.Equalf(t, row.FlowArm, armed,
				"%s: the table says FlowArm=%v and the registry says %v — the table can neither "+
					"claim an arm the registry lacks nor miss one it has", lang, row.FlowArm, armed)
		}
	})

	t.Run("armed_set_is_the_ruled_fifteen", func(t *testing.T) {
		want := map[Language]bool{}
		for _, l := range ruledFlowLanguages {
			want[l] = true
		}
		require.Len(t, want, 15, "control: the ruled list itself holds fifteen distinct languages")

		got := map[Language]bool{}
		for _, lang := range RegisteredLanguages() {
			if _, armed := FlowStepsArm(lang); armed {
				got[lang] = true
			}
		}
		assert.Equal(t, want, got,
			"the armed set is EXACTLY the ruled fifteen — neither a language short nor one over")

		// THE NAMED EXCLUSIONS, each asserted with the reason the ruling gives.
		// They are stated individually rather than folded into the set equality
		// above because a set difference reports WHAT differs and never WHY.
		_, phpArmed := FlowStepsArm(LangPHP)
		assert.False(t, phpArmed,
			"php is NOT armed: the ast DSL denies it outright for a placeholder-sigil collision, "+
				"so the flow leaf that reads these facts could never be exercised there")
		for _, lang := range []Language{LangBash, LangElixir, LangElm, LangLua, LangOCaml} {
			_, armed := FlowStepsArm(lang)
			assert.Falsef(t, armed,
				"%s is NOT armed: the ruling excludes it for the missing binder — there is no "+
					"name-to-value binding for a closure to run over", lang)
		}
	})

	t.Run("registry_matches_the_table", func(t *testing.T) {
		// TWO INDEPENDENT MEASUREMENTS, NEITHER WRITTEN AS A NUMBER. A language
		// added to the registry later cannot escape this table: it fails here
		// until it is accounted for, armed or not.
		registered := RegisteredLanguages()
		require.NotEmpty(t, registered,
			"control: the registry is non-empty, or every assertion here is vacuous")
		for _, lang := range registered {
			require.Containsf(t, testFlowStepsCensus, lang,
				"%s is a registered language with no flow census row: add one stating what its "+
					"arm serves, or what the language does not write", lang)
		}
		for lang := range testFlowStepsCensus {
			require.Containsf(t, registered, lang,
				"%s has a flow census row but is not a registered language", lang)
		}
	})

	t.Run("swift_arm_registered", func(t *testing.T) {
		// SWIFT IS CALLED OUT SEPARATELY because it is the one ruled language
		// whose arm lands in this phase rather than in an arm-group phase, so a
		// dispatch that skipped its step would leave the set-equality assertion
		// above as the only thing standing between it and a silently missing arm.
		_, armed := FlowStepsArm(LangSwift)
		assert.True(t, armed, "the swift flow arm is registered")

		// KNOWN-NEGATIVE CONTROL in the same subtest: a language the ruling
		// excludes is NOT armed, so a registry that answered true for everything
		// could not satisfy both halves.
		_, phpArmed := FlowStepsArm(LangPHP)
		assert.False(t, phpArmed,
			"control: the accessor does not answer true for every language")
	})
}
