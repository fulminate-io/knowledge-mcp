// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// TestSplitQualifierPerLanguage pins the separator set of every language that
// registers one, plus the two properties the table's shape makes easy to break.
//
// unlisted_defaults_to_dot IS THE DEFAULTING CATCHER and is the reason this
// test exists at all. langProfiles is an OVERRIDE table, so it takes no
// completeness test of the kind scopeKinds carries; what it owes instead is
// proof that a language with NO ROW still splits. A future refactor that
// "closes" the table by filling it with zero-value rows would retire the
// qualified rungs for every unlisted language — their dynamic groups and
// receiver bindings with them — and no other gate in this package would go red.
//
// IT PROBES ELM, NOT GO, AND THAT MOVED. The sibling-rung gating gave go a row
// of its own, so a go probe now reads a REGISTERED row and states nothing at
// all about the defaulting rule it was written to catch — a catcher that passes
// in every state of the table. elm is unlisted, so it restores the property;
// go_registered_row_still_splits keeps go's own outcome pinned beside it,
// because the row that replaced its defaulting is exactly what could break it.
//
// c_never_splits and bash_never_splits are the opposite property on the same
// mechanism: an explicitly EMPTY separator set is not the same as an absent
// row, and collapsing the two would tear a bash command name such as
// `./deploy.sh` into a bogus qualifier and name.
func TestSplitQualifierPerLanguage(t *testing.T) {
	cases := []struct {
		name          string
		lang          treesitter.Language
		target        string
		wantQualifier string
		wantName      string
	}{
		{
			name: "unlisted_defaults_to_dot", lang: treesitter.LangElm,
			target: "pkg.Fn", wantQualifier: "pkg", wantName: "Fn",
		},
		{
			name: "go_registered_row_still_splits", lang: treesitter.LangGo,
			target: "pkg.Fn", wantQualifier: "pkg", wantName: "Fn",
		},
		{
			name: "java_dot", lang: treesitter.LangJava,
			target: "obj.doThing", wantQualifier: "obj", wantName: "doThing",
		},
		{
			name: "java_unqualified_has_no_qualifier", lang: treesitter.LangJava,
			target: "plain", wantQualifier: "", wantName: "plain",
		},
		{
			name: "rust_scope_operator", lang: treesitter.LangRust,
			target: "foo::bar", wantQualifier: "foo", wantName: "bar",
		},
		{
			name: "rust_still_splits_a_field_access", lang: treesitter.LangRust,
			target: "obj.do_thing", wantQualifier: "obj", wantName: "do_thing",
		},
		{
			// The longest separator wins at a tie position: `a::b` must never
			// yield ("a:", "b") off the second colon.
			name: "cpp_longest_separator_wins", lang: treesitter.LangCPP,
			target: "a::b", wantQualifier: "a", wantName: "b",
		},
		{
			name: "cpp_arrow", lang: treesitter.LangCPP,
			target: "ptr->m2", wantQualifier: "ptr", wantName: "m2",
		},
		{
			// The leading backslash stays inside the qualifier: it is part of
			// the namespace's own spelling, and the `::` is the later split.
			name: "php_scope_beats_the_namespace_separator", lang: treesitter.LangPHP,
			target: `\Other\Thing::go`, wantQualifier: `\Other\Thing`, wantName: "go",
		},
		{
			name: "php_namespace_separator", lang: treesitter.LangPHP,
			target: `\Foo\Bar`, wantQualifier: `\Foo`, wantName: "Bar",
		},
		{
			name: "php_arrow", lang: treesitter.LangPHP,
			target: "$o->doThing", wantQualifier: "$o", wantName: "doThing",
		},
		{
			name: "lua_colon_call", lang: treesitter.LangLua,
			target: "obj:meth", wantQualifier: "obj", wantName: "meth",
		},
		{
			name: "c_never_splits", lang: treesitter.LangC,
			target: "a.b", wantQualifier: "", wantName: "a.b",
		},
		{
			name: "bash_never_splits", lang: treesitter.LangBash,
			target: "./deploy.sh", wantQualifier: "", wantName: "./deploy.sh",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotQualifier, gotName := splitQualifier(tc.lang, tc.target)
			assert.Equal(t, tc.wantQualifier, gotQualifier, "qualifier")
			assert.Equal(t, tc.wantName, gotName, "name")
		})
	}
}

// TestProfileForDefaultsAreNotTheZeroValue is the other half of the defaulting
// rule: an unregistered language must come back dot-splitting and import-first,
// never with the struct's zero value, which would be an empty separator set and
// locals-first.
//
// It reads through profileFor rather than indexing the table, which is the
// discipline the whole package keeps — profileFor is the only read path, so a
// test that indexed the map directly would both bypass the defaulting rule it
// is asserting and break the gate that pins that rule.
//
// THE PROBE IS ELM RATHER THAN GO FOR THE SAME REASON THE SPLIT CASE MOVED: the
// sibling-rung gating registered a go row, so probing go here would read that
// row and assert nothing about the default. elm is unlisted.
func TestProfileForDefaultsAreNotTheZeroValue(t *testing.T) {
	got := profileFor(treesitter.LangElm)
	assert.Equal(t, []string{"."}, got.Separators)
	assert.True(t, got.ImportsBeatLocals)

	// A registered language keeps its own row, so the default is a fallback
	// rather than a value every lookup returns.
	assert.Equal(t, []string{"::", "."}, profileFor(treesitter.LangRust).Separators)
	assert.False(t, profileFor(treesitter.LangPython).ImportsBeatLocals)
}

// TestProfileFor_SiblingRungDefault pins the POLARITY of the sibling knob: the
// value a language reaches without a derivation is the behavior it had before
// the knob existed.
//
// It guards the defaulting rule from the other side. SkipSiblingRung's zero
// value is false, so an unlisted language keeps the sibling rung; an edit that
// inverted the polarity, or that "closed" the table by filling in rows the
// wrong way round, would retire that rung for every language nobody derived —
// twelve registered ones plus every unregistered one — with no other gate in
// this package going red.
//
// THE KNOWN-POSITIVE IS LOAD-BEARING, NOT DECORATION. Every assertion in the
// first half is a FALSE, and a field that was never wired at all reads false
// everywhere; the derived-skip languages asserting TRUE through the same
// accessor in the same run are what tell a live zero from a dead one.
func TestProfileFor_SiblingRungDefault(t *testing.T) {
	// UNLISTED — elm registers no row and reaches profileFor's default.
	assert.False(t, profileFor(treesitter.LangElm).SkipSiblingRung,
		"a language with no row keeps the sibling rung: the zero value is today's behavior")

	// LISTED BUT UNDERIVED — swift has a row and this work did not derive it,
	// so its omission of the field must read as unchanged, never as skip.
	assert.False(t, profileFor(treesitter.LangSwift).SkipSiblingRung,
		"an underived row is unchanged by omission")

	// DERIVED-KEEP — ruby and java were executed and concluded KEEP, which is
	// the same value. Asserted rather than only commented, so the derivation is
	// pinned by the suite and not by prose alone.
	assert.False(t, profileFor(treesitter.LangRuby).SkipSiblingRung,
		"ruby: a bare sibling call runs (implicit self)")
	assert.False(t, profileFor(treesitter.LangJava).SkipSiblingRung,
		"java: compiles and runs (implicit this)")

	// THE KNOWN-POSITIVE CONTROL: the five derived-skip rows read TRUE through
	// the same accessor.
	for _, lang := range []treesitter.Language{
		treesitter.LangGo, treesitter.LangJavaScript, treesitter.LangTypeScript,
		treesitter.LangTSX, treesitter.LangPython,
	} {
		assert.True(t, profileFor(lang).SkipSiblingRung, "%s is a derived-skip row", lang)
	}

	// THE FOUR NEW ROWS RESTATE THE DEFAULT'S OTHER TWO FIELDS EXACTLY. They had
	// no row at all before, so a row that dropped Separators would register an
	// EXPLICITLY EMPTY set — never split — and retire the qualified rungs for Go
	// and the whole TypeScript family while every sibling assertion above stayed
	// green.
	for _, lang := range []treesitter.Language{
		treesitter.LangGo, treesitter.LangJavaScript,
		treesitter.LangTypeScript, treesitter.LangTSX,
	} {
		assert.Equal(t, []string{"."}, profileFor(lang).Separators, "%s separators", lang)
		assert.True(t, profileFor(lang).ImportsBeatLocals, "%s import order", lang)
	}
}
