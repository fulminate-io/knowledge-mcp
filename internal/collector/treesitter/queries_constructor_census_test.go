// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sitter "github.com/smacker/go-tree-sitter"
)

// constructionForm classifies HOW a language spells object construction, which
// is the distinction that decides whether a constructor is invisible to the
// Calls query or was never a separate problem in the first place.
//
//	formDedicatedNode — the grammar has its own node kind for construction
//	                    (ECMAScript's new_expression, Java's
//	                    object_creation_expression). A Calls query that matches
//	                    only call nodes cannot see it, so it is a real gap.
//	formOrdinaryCall  — construction IS a call. Python's `Foo()`, Ruby's
//	                    `Foo.new`, Swift's and Kotlin's `Foo()` are ordinary
//	                    call nodes the Calls query already matches, so those
//	                    languages have nothing to fix. Recording them as "no
//	                    construct" would read as an oversight.
//	formNone          — the language constructs no objects at all: the config
//	                    and markup grammars, and the shell/query languages.
type constructionForm string

const (
	formDedicatedNode constructionForm = "dedicated-node"
	formOrdinaryCall  constructionForm = "ordinary-call"
	formNone          constructionForm = "none"
)

// Which query names the construct, and therefore which edge type a captured
// constructor becomes: Calls emits CALLS, TypeRefs emits USES_TYPE.
const (
	capturedByCalls    = "calls"
	capturedByTypeRefs = "typerefs"
	capturedByNothing  = ""
)

// constructorCensusRow is one language's finding.
//
// construct and wantCaptured are CHECKED against the grammar and the query
// source rather than believed: a construct naming a node kind its own grammar
// does not declare fails, and a wantCaptured disagreeing with what the language's
// own Calls/TypeRefs strings say fails. That is what stops this table drifting
// away from the query files beside it the first time one is edited.
type constructorCensusRow struct {
	form constructionForm
	// construct is the grammar's node kind for construction, empty for every
	// form other than formDedicatedNode.
	construct string
	// wantCapturedBy is WHICH of the language's own queries names that construct
	// today — capturedByCalls, capturedByTypeRefs, or capturedByNothing.
	//
	// WHICH ONE IS THE WHOLE FINDING, not a detail. A construct captured by
	// TypeRefs becomes a USES_TYPE edge and by Calls a CALLS edge, and those are
	// different answers to "how is a constructor represented" — java and csharp
	// already chose the type-reference representation that this ticket weighed
	// and declined for ECMAScript. Collapsing both into a bare captured/not
	// would erase exactly the distinction a future ticket has to decide.
	//
	// Empty for formOrdinaryCall, whose capture comes from the ordinary call arm
	// rather than from any query naming a construct.
	wantCapturedBy string
	// wantArm is whether the language has a registered BindsResolver, i.e.
	// whether a captured construct could reach an import rung at all.
	wantArm bool
	// corpusGap is the per-language figure from the committed corpus artifact,
	// or an explicit not-measured marker. A language absent from the corpus
	// gets "not measured" and NEVER a zero: zero would read as "no gap" when
	// the truth is that nothing was ever measured.
	corpusGap string
}

// ecmaBindsArmLanguages are the three languages whose BindsResolver is
// installed by cmd/knowledge/internal/collector/jsmodule rather than by this
// package's own init.
//
// THEIR ARM IS DECLARED RATHER THAN OBSERVED, and that is a package-boundary
// fact rather than a shortcut: jsmodule imports treesitter, so this test cannot
// import jsmodule to trigger its init without an import cycle, and inside this
// package's test binary hasBindsResolver reports false for all three while
// production has them armed. Every other language's arm IS observed.
var ecmaBindsArmLanguages = []Language{LangTypeScript, LangTSX, LangJavaScript}

// constructorCensus is the per-language table. EVERY REGISTERED LANGUAGE HAS A
// ROW, including the ones whose finding is "nothing to do" — a census that
// omitted them could not be told apart from one that forgot them.
//
// The corpus figures were transcribed from
// parser/testdata/ful1334_corpus_verification.txt, measured over
// /Users/jonathan/code/agent at corpus commit 15313d4f0. They are DATA here
// rather than a file read at test time, so this test needs no corpus access and
// no second artifact parser.
// Each corpus figure is spelled out in full rather than composed from a shared
// constant, so a reader grepping this file for one language gets that
// language's whole finding on the line the grep hits.
func constructorCensus() map[Language]constructorCensusRow {
	return map[Language]constructorCensusRow{
		// ===== THE FAMILY THIS TICKET FIXED =====
		// new_expression is named by the Calls query as of this ticket, so
		// wantCaptured is true for all three. The corpus figures are the
		// post-change ones and the qualified-import zero is explained in the
		// finding attached to this ticket: the corpus's .ts constructors are
		// 559-of-566 bare, and its 7 qualified ones are ambient globals.
		LangTypeScript: {formDedicatedNode, "new_expression", capturedByCalls, true,
			"measured: bound_rule_qualified_import=0, bound_rule_unqualified_import=879, dynamic_groups=164, references=6818 (257 files)"},
		LangTSX: {formDedicatedNode, "new_expression", capturedByCalls, true,
			"measured: bound_rule_qualified_import=0, bound_rule_unqualified_import=2906, dynamic_groups=85, references=15379 (583 files)"},
		LangJavaScript: {formDedicatedNode, "new_expression", capturedByCalls, true,
			"measured: bound_rule_qualified_import=0, bound_rule_unqualified_import=0, dynamic_groups=6, references=796 (7 files)"},

		// ===== DEDICATED NODE ALREADY CAPTURED, AS A TYPE REFERENCE =====
		// java and csharp already answer the representation question, and they
		// answer it the OTHER way: their TypeRefs queries name
		// object_creation_expression and bind its type, so `new Foo()` emits a
		// USES_TYPE edge to Foo and no CALLS edge. Neither language is a gap,
		// and a future ticket that "fixed" them the ECMAScript way would be
		// adding a second edge beside an existing one — the count inflation this
		// ticket's own representation decision was careful to avoid.
		LangJava: {formDedicatedNode, "object_creation_expression", capturedByTypeRefs, true,
			"not measured: no files in the corpus"},
		LangCSharp: {formDedicatedNode, "object_creation_expression", capturedByTypeRefs, true,
			"not measured: no files in the corpus"},

		// ===== DEDICATED CONSTRUCTION NODE, CAPTURED BY NOTHING =====
		// The real gaps a future ticket would decide about. Each has its own
		// construction node kind that neither query names, so a constructor-only
		// reference is invisible exactly as ECMAScript's was.
		LangPHP: {formDedicatedNode, "object_creation_expression", capturedByNothing, true,
			"not measured: no files in the corpus"},
		LangCPP: {formDedicatedNode, "new_expression", capturedByNothing, true,
			"not measured: no files in the corpus"},
		LangRust: {formDedicatedNode, "struct_expression", capturedByNothing, true,
			"not measured: no files in the corpus"},
		LangScala: {formDedicatedNode, "instance_expression", capturedByNothing, true,
			"not measured: no files in the corpus"},

		// ===== CONSTRUCTION IS AN ORDINARY CALL — NOTHING TO FIX =====
		// The Calls query already matches these, so there is no gap to close
		// and a future ticket should not open one. Recording them explicitly is
		// the point: without a row, python and swift look like languages nobody
		// checked.
		LangPython: {formOrdinaryCall, "", capturedByNothing, true,
			"not measured: 2 files in the corpus, no per-language resolution rows in the artifact"},
		LangRuby: {formOrdinaryCall, "", capturedByNothing, false,
			"not measured: no files in the corpus"},
		LangSwift: {formOrdinaryCall, "", capturedByNothing, true,
			"not measured: no files in the corpus"},
		LangKotlin: {formOrdinaryCall, "", capturedByNothing, true,
			"not measured: no files in the corpus"},
		LangGroovy: {formOrdinaryCall, "", capturedByNothing, false,
			"not measured: no files in the corpus"},
		LangLua: {formOrdinaryCall, "", capturedByNothing, false,
			"not measured: no files in the corpus"},
		LangElixir: {formOrdinaryCall, "", capturedByNothing, false,
			"not measured: no files in the corpus"},
		LangElm: {formOrdinaryCall, "", capturedByNothing, false,
			"not measured: no files in the corpus"},
		LangOCaml: {formOrdinaryCall, "", capturedByNothing, false,
			"not measured: no files in the corpus"},

		// ===== NO CONSTRUCTION CONSTRUCT AT ALL =====
		// Go allocates with a builtin call or a composite literal and has no
		// constructor concept, so there is no constructor reference to lose.
		LangGo: {formNone, "", capturedByNothing, true,
			"measured: bound_rule_qualified_import=13297, dynamic_groups=11023 (3270 files)"},
		LangC: {formNone, "", capturedByNothing, true,
			"not measured: no files in the corpus"},
		LangBash: {formNone, "", capturedByNothing, false,
			"not measured: 40 files in the corpus, no per-language resolution rows in the artifact"},
		LangSQL: {formNone, "", capturedByNothing, false,
			"not measured: 126 files in the corpus, no per-language resolution rows in the artifact"},
		LangCSS: {formNone, "", capturedByNothing, false,
			"not measured: 2 files in the corpus, no per-language resolution rows in the artifact"},
		LangHTML: {formNone, "", capturedByNothing, false,
			"not measured: 9 files in the corpus, no per-language resolution rows in the artifact"},
		LangProtobuf: {formNone, "", capturedByNothing, false,
			"not measured: 20 files in the corpus, no per-language resolution rows in the artifact"},
		LangToml: {formNone, "", capturedByNothing, false,
			"not measured: 1 files in the corpus, no per-language resolution rows in the artifact"},
		LangYaml: {formNone, "", capturedByNothing, false,
			"not measured: 72 files in the corpus, no per-language resolution rows in the artifact"},
		LangDockerfile: {formNone, "", capturedByNothing, false,
			"not measured: 7 files in the corpus, no per-language resolution rows in the artifact"},
		LangHCL: {formNone, "", capturedByNothing, false,
			"not measured: no files in the corpus"},
		LangMarkdown: {formNone, "", capturedByNothing, false,
			"not measured: no files in the corpus"},
		LangCue: {formNone, "", capturedByNothing, false,
			"not measured: no files in the corpus"},
		LangSvelte: {formNone, "", capturedByNothing, false,
			"not measured: no files in the corpus"},
	}
}

// grammarHasNamedKind reports whether a grammar declares kind as a NAMED node.
// It reads the same symbol table cmd/knowledge/internal/ast enumerates for
// list_node_kinds, so a construct spelled here is a construct that language can
// actually produce.
func grammarHasNamedKind(grammar *sitter.Language, kind string) bool {
	for i := range int(grammar.SymbolCount()) {
		s := sitter.Symbol(uint16(i))
		if grammar.SymbolName(s) != kind {
			continue
		}
		if grammar.SymbolType(s) == sitter.SymbolTypeRegular {
			return true
		}
	}
	return false
}

// TestConstructorConstructCensus records, for EVERY registered language, how it
// spells object construction, whether the Calls or TypeRefs query captures that
// construct today, whether a captured construct could reach a binding rung, and
// what the committed corpus artifact measured for it.
//
// IT ASSERTS COMPLETENESS AND NON-DRIFT, NEVER A PREFERENCE. Nothing here says a
// gap SHOULD be closed — that is a future ticket's decision, and encoding it
// would be this ticket fixing a language it was told not to fix. What the test
// does enforce is that the table cannot quietly disagree with the grammar beside
// it, cannot quietly disagree with the query file beside it, and cannot shrink:
// the subject list is DERIVED from the registry, so a 33rd language fails here
// until someone gives it a row.
func TestConstructorConstructCensus(t *testing.T) {
	census := constructorCensus()
	registered := RegisteredLanguages()
	require.NotEmpty(t, registered, "control: the registry walk found no languages at all")

	// COMPLETENESS, BOTH DIRECTIONS. Every registered language needs a row, and
	// no row may name a language the registry does not have — the second half
	// is what catches a row left behind by a language that was removed.
	for _, lang := range registered {
		_, ok := census[lang]
		assert.True(t, ok, "%s is registered but has no constructor census row", lang)
	}
	for lang := range census {
		assert.Contains(t, registered, lang, "census row %q names an unregistered language", lang)
	}
	require.Len(t, census, len(registered),
		"the census must have exactly one row per registered language")

	// The derivation must not be constant: a broken capture check that read
	// false for everything, or true for everything, would otherwise pass the
	// per-language assertions below without measuring anything.
	capturedSeen, uncapturedSeen := 0, 0

	for _, lang := range registered {
		row, ok := census[lang]
		if !ok {
			continue // already reported above
		}
		t.Run(string(lang), func(t *testing.T) {
			entry := registry[lang]
			require.NotNil(t, entry, "control: %s has no registry entry", lang)
			qs := entry.Queries()
			require.NotNil(t, qs, "control: %s has no query set", lang)

			// The construct is spelled only for the dedicated-node form, and
			// when spelled it must be a kind the grammar actually declares.
			if row.form == formDedicatedNode {
				require.NotEmpty(t, row.construct,
					"a dedicated-node row must name the node kind")
				assert.True(t, grammarHasNamedKind(entry.lang, row.construct),
					"%s declares no named node kind %q — the census names a construct its own grammar cannot produce",
					lang, row.construct)
			} else {
				assert.Empty(t, row.construct,
					"%s is %s, so it must name no construction node kind", lang, row.form)
			}

			// CAPTURED_TODAY IS DERIVED FROM THE QUERY SOURCES, never asserted
			// by hand. Read off the language's own Calls and TypeRefs strings,
			// which is what makes a row go red when the query file beside it is
			// edited without updating the census — and it is how java's and
			// csharp's TypeRefs capture was found rather than assumed away.
			assert.Equal(t, row.wantCapturedBy, capturedBy(qs, row.construct),
				"%s: the census says captured-by %q but its own Calls/TypeRefs queries say %q",
				lang, row.wantCapturedBy, capturedBy(qs, row.construct))

			// RESOLVABLE_TODAY is OBSERVED from the resolver registry for every
			// language whose arm this package installs, and declared only for
			// the three the import cycle puts out of reach.
			if slices.Contains(ecmaBindsArmLanguages, lang) {
				assert.True(t, row.wantArm,
					"%s is armed by jsmodule in production; see ecmaBindsArmLanguages", lang)
			} else {
				assert.Equal(t, row.wantArm, hasBindsResolver(lang),
					"%s: the census disagrees with the registered BindsResolver set", lang)
			}

			// A gap figure is always stated, and a language the corpus never
			// measured says so in words rather than reporting a zero.
			require.NotEmpty(t, row.corpusGap, "%s states no corpus figure at all", lang)
			assert.True(t,
				strings.HasPrefix(row.corpusGap, "measured:") ||
					strings.HasPrefix(row.corpusGap, "not measured:"),
				"%s: a corpus figure must declare itself measured or not measured, got %q",
				lang, row.corpusGap)
		})

		if capturedBy(registry[lang].Queries(), row.construct) == capturedByNothing {
			uncapturedSeen++
		} else {
			capturedSeen++
		}
	}

	// THE KNOWN-POSITIVE CONTROLS for the capture derivation, in both
	// directions. Without them a derivation stuck at one value would satisfy
	// every per-language equality above, because the table would have been
	// written to match whatever it returned.
	assert.Positive(t, capturedSeen,
		"control: no language reads as capturing its construct — the derivation is stuck at false")
	assert.Positive(t, uncapturedSeen,
		"control: every language reads as capturing its construct — the derivation is stuck at true")
}

// capturedBy reports WHICH of a language's queries names the given construct.
// An empty construct is captured by nothing by definition: a language whose
// construction is an ordinary call has no construct for a query to name, and
// its capture comes from the ordinary call arm instead.
//
// Calls is checked before TypeRefs so a construct named by both reports the
// CALLS representation, which is the stronger claim of the two.
func capturedBy(qs *QuerySet, construct string) string {
	if construct == "" {
		return capturedByNothing
	}
	if strings.Contains(qs.Calls, construct) {
		return capturedByCalls
	}
	if strings.Contains(qs.TypeRefs, construct) {
		return capturedByTypeRefs
	}
	return capturedByNothing
}
