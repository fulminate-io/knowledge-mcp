// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// censusExclusions records every (language, kind) pair whose grammar DECLARES
// an admitted kind while that language deliberately does not admit it. Each
// carries the reason, because an exclusion without one is indistinguishable
// from an oversight — which is exactly how the TypeScript leak survived.
//
// TestClassLikeCrossGrammarCensus requires every entry to be LIVE: the named
// grammar must really declare the kind. A stale exclusion therefore fails
// rather than lingering as prose nobody rechecks.
var censusExclusions = map[[2]string]string{
	{"typescript", "module"}: "a TypeScript module block is a namespace, not a class-like container; " +
		"admitting it parented its members as web/mod.ts:Sink.write",
	{"tsx", "module"}: "a TypeScript module block is a namespace, not a class-like container; " +
		"admitting it parented its members as web/mod.tsx:Sink.write",
	{"python", "module"}: "python's module is the file ROOT node; containerName's third source scans " +
		"for type_identifier or simple_identifier and the python grammar declares neither, so it " +
		"can never name a container",
	{"elm", "module"}: "elm's module is a keyword leaf inside module_declaration and is never an " +
		"ancestor of a declaration",
}

// namedKindsByLanguage builds, once per language, the set of node kinds that
// language's grammar declares as a regular named symbol.
//
// WHAT THE PREBUILD SAVES IS THE GRAMMAR LOOKUP, NOT THE SYMBOL-TABLE WALK, and
// the distinction is worth stating because the walk is the expensive half.
// grammarHasNamedKind (queries_constructor_census_test.go:208) walks a whole
// symbol table per call and is still called once per (language, kind) pair, so
// the walk count is unchanged at one per pair. What collapses is LanguageGrammar,
// from one call per pair to one per language — and the census loop is left
// indexing a plain map instead of reaching for a grammar mid-walk, which is the
// readability point. The whole census test measures about 0.01s at 32 languages
// and 21 kinds, so this is shape rather than optimization.
func namedKindsByLanguage(t *testing.T, kinds []string) map[Language]map[string]bool {
	t.Helper()

	out := make(map[Language]map[string]bool)
	for _, lang := range RegisteredLanguages() {
		grammar, ok := LanguageGrammar(lang)
		require.True(t, ok, "control: every registered language must expose a grammar, %q did not", lang)
		declared := make(map[string]bool, len(kinds))
		for _, kind := range kinds {
			if grammarHasNamedKind(grammar, kind) {
				declared[kind] = true
			}
		}
		out[lang] = declared
	}
	return out
}

// admittedKinds returns the union of every kind spelled anywhere in
// classLikeByLang. It is DERIVED rather than hand-listed, so the census surface
// is re-enumerated on every run and cannot go stale as rows change.
func admittedKinds() []string {
	seen := map[string]bool{}
	var out []string
	for _, row := range classLikeByLang {
		for kind := range row {
			if !seen[kind] {
				seen[kind] = true
				out = append(out, kind)
			}
		}
	}
	return out
}

// TestClassLikeByLangMatchesGrammars is PARITY, table to grammar: every kind a
// row names must be a kind that language's own grammar actually declares.
//
// A row naming a kind the grammar does not declare is invisible at run time —
// the lookup simply never matches — so nothing else in the suite can see it.
// This is the test-failure form of the panic newSymbolClasses raises at
// chunker_kind_symbols.go for the same class of mistake, chosen over a panic
// because a correctness fix should not newly impose a process-fatal contract on
// 32 languages.
func TestClassLikeByLangMatchesGrammars(t *testing.T) {
	langs := RegisteredLanguages()
	require.GreaterOrEqual(t, len(langs), 32,
		"control: the registry must be readable, and it held 32 languages when this was written")

	for lang, row := range classLikeByLang {
		grammar, ok := LanguageGrammar(lang)
		require.True(t, ok, "classLikeByLang names %q, which is not a registered language", lang)
		for kind := range row {
			assert.True(t, grammarHasNamedKind(grammar, kind),
				"classLikeByLang[%q] admits %q, but the %s grammar declares no such named kind",
				lang, kind, lang)
		}
	}
}

// TestClassLikeByLangCoversEveryRegisteredLanguage is COMPLETENESS: the
// declared-versus-consumed partition between the registry and the table.
//
// AN EMPTY ROW IS A RECORDED DECISION; A MISSING ROW IS AN UNDECIDED ONE. Both
// behave identically at run time — a nil map indexes to false — which is
// precisely why the difference has to be asserted rather than observed. This
// fails the build when language 33 is registered with no admission decision
// recorded for it.
func TestClassLikeByLangCoversEveryRegisteredLanguage(t *testing.T) {
	langs := RegisteredLanguages()
	require.GreaterOrEqual(t, len(langs), 32,
		"control: the registry must be readable, and it held 32 languages when this was written")

	registered := make(map[Language]bool, len(langs))
	for _, lang := range langs {
		registered[lang] = true
		_, ok := classLikeByLang[lang]
		assert.True(t, ok,
			"language %q is registered but has no classLikeByLang row — an empty row records a "+
				"decision, a missing one records nothing", lang)
	}

	for lang := range classLikeByLang {
		assert.True(t, registered[lang],
			"classLikeByLang carries a row for %q, which is not a registered language", lang)
	}

	// Known positive: the partition above is only meaningful if some rows are
	// genuinely non-empty and some are genuinely empty. Without this, a table
	// of 32 empty rows would satisfy every assertion above.
	assert.NotEmpty(t, classLikeByLang[LangRuby], "known positive: ruby's row carries kinds")
	assert.Empty(t, classLikeByLang[LangGo], "known positive: go's row is empty by construction")
}

// TestClassLikeCrossGrammarCensus is THE REVERSE PARTITION — kind to grammars —
// and it is the test that would have caught the defect this table's language
// dimension exists to fix.
//
// For every kind admitted anywhere, every grammar that DECLARES that kind must
// either admit it too or carry a reason in censusExclusions. A bare-spelling
// table made that question unanswerable: admitting `module` for Ruby silently
// admitted it for the four other grammars declaring the spelling, and no test
// looking at one language could see it.
func TestClassLikeCrossGrammarCensus(t *testing.T) {
	langs := RegisteredLanguages()
	require.GreaterOrEqual(t, len(langs), 32,
		"control: the registry must be readable, and it held 32 languages when this was written")

	kinds := admittedKinds()
	require.NotEmpty(t, kinds, "control: the table must yield a non-empty kind union")

	declared := namedKindsByLanguage(t, kinds)

	seenExclusion := map[[2]string]bool{}
	for _, kind := range kinds {
		for _, lang := range langs {
			if !declared[lang][kind] {
				continue
			}
			if classLikeByLang[lang][kind] {
				continue
			}
			key := [2]string{string(lang), kind}
			reason, ok := censusExclusions[key]
			assert.True(t, ok,
				"the %s grammar declares %q and the table admits that kind for another language, "+
					"but %q neither admits it nor records why not", lang, kind, lang)
			assert.NotEmpty(t, reason,
				"the exclusion of (%q, %q) carries an empty reason", lang, kind)
			seenExclusion[key] = true
		}
	}

	// Every exclusion must be LIVE — its grammar really does declare the kind
	// and the pair really is unadmitted — so a pair that stops being excluded,
	// or a kind that leaves the table entirely, fails here instead of lingering
	// as prose nobody rechecks.
	for key := range censusExclusions {
		assert.True(t, seenExclusion[key],
			"censusExclusions carries (%q, %q), but that pair was not reached by the census — "+
				"either the grammar no longer declares the kind, the kind left the table, or the "+
				"pair is now admitted", key[0], key[1])
	}

	// Known positive: the census must actually have reached exclusions, or
	// every assertion above passes over an empty walk.
	assert.Len(t, seenExclusion, len(censusExclusions),
		"known positive: the census reached every recorded exclusion")
	assert.NotEmpty(t, seenExclusion, "known positive: the census reached at least one exclusion")
}

// TestNonTypeContainerKindsSubsetOfClassLike pins the relation between the two
// container tables.
//
// nonTypeContainerKinds stays keyed on the bare spelling, because it answers a
// question about the KIND rather than about a language: every grammar sharing
// one of its spellings agrees the kind names a block a type reference cannot
// mean. The relation to classLikeByLang is SUBSET, not equality — the
// typescript and tsx `module` spellings sit in nonTypeContainerKinds while
// carrying no class-like admission at all.
func TestNonTypeContainerKindsSubsetOfClassLike(t *testing.T) {
	union := map[string]bool{}
	for _, row := range classLikeByLang {
		for kind := range row {
			union[kind] = true
		}
	}
	require.NotEmpty(t, union, "control: the table must yield a non-empty kind union")

	for kind := range nonTypeContainerKinds {
		assert.True(t, union[kind],
			"nonTypeContainerKinds holds %q, which no language's classLikeByLang row admits — "+
				"the filter must be over the union of those rows", kind)
	}

	// Known positive: the subset assertion is only meaningful if the union is
	// STRICTLY larger, so a table that had collapsed to exactly these eight
	// kinds would not read as a pass.
	assert.Greater(t, len(union), len(nonTypeContainerKinds),
		"known positive: the admitted union is strictly larger than the non-type filter")
	assert.True(t, union["class_declaration"],
		"known positive: a type-bearing kind is in the union and out of the filter")
	assert.False(t, nonTypeContainerKinds["class_declaration"],
		"known positive: a class declaration IS a type a reference can name")
}
