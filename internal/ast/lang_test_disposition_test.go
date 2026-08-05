// SPDX-License-Identifier: Apache-2.0

// lang_test_disposition_test.go — the per-language test-file disposition table.
//
// Every registered language must make an explicit choice: a predicate that says
// what test source looks like, or a documented nil that says this language has
// no unambiguous filename convention. The table below is the assertion of that
// choice, and it is checked against the LIVE registry rather than a count — a
// newly registered language that decides nothing fails here rather than
// inheriting "no filtering" by accident.
//
// Each language with a predicate carries sample paths in both directions. A
// presence-only check would pass against a predicate that returns false for
// everything, which is precisely the silently-inert filter this work removes.

package ast

import (
	"maps"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// testFileDisposition is one language's decision plus the samples that prove the
// predicate discriminates. tests/nonTests are empty for a nil disposition.
type testFileDisposition struct {
	// reason documents a nil disposition. Empty for a language with a predicate.
	reason   string
	tests    []string
	nonTests []string
}

// hasPredicate reports whether this disposition expects a non-nil IsTestFile.
func (d testFileDisposition) hasPredicate() bool { return d.reason == "" }

// langTestDispositions is the full partition of the registered languages. Paths
// are drawn from the fixture corpora the conventions were confirmed against, so
// the negative samples include the real traps: Django's shipped django/test/
// package, Phoenix's shipped lib/phoenix/test/ modules, and a JVM main source
// set holding a file whose NAME says Test.
var langTestDispositions = map[treesitter.Language]testFileDisposition{
	treesitter.LangGo: {
		tests:    []string{"main_test.go", "pkg/a/b_test.go"},
		nonTests: []string{"main.go", "pkg/a/b.go", "pkg/testing.go"},
	},
	treesitter.LangRuby: {
		tests:    []string{"activerecord/test/cases/base_test.rb", "spec/models/user_spec.rb", "spec/spec_helper.rb"},
		nonTests: []string{"activerecord/lib/active_record/base.rb", "lib/protest.rb", "specs/legacy.rb"},
	},
	treesitter.LangPython: {
		tests:    []string{"tests/utils_tests/test_html.py", "pkg/handler_test.py"},
		nonTests: []string{"django/test/client.py", "django/test/runner.py", "tests/urls.py", "pkg/latest.py"},
	},
	treesitter.LangElixir: {
		tests:    []string{"test/phoenix/router_test.exs", "installer/test/mix_helper.exs"},
		nonTests: []string{"lib/phoenix/test/conn_test.ex", "lib/phoenix.ex", "config/config.exs"},
	},
	treesitter.LangJava: {
		tests:    []string{"core/src/test/java/com/x/FooTests.java", "plugin/src/dockerTest/java/com/x/BuildImageTests.java"},
		nonTests: []string{"core/src/main/java/com/x/Foo.java", "core/src/main/java/com/x/TestSupport.java"},
	},
	treesitter.LangKotlin: {
		tests:    []string{"okhttp/src/test/kotlin/okhttp3/CallTest.kt", "okhttp/src/jvmTest/kotlin/okhttp3/CacheTest.kt"},
		nonTests: []string{"okhttp/src/main/kotlin/okhttp3/Call.kt"},
	},
	treesitter.LangScala: {
		tests:    []string{"akka-actor/src/test/scala/akka/ActorSpec.scala", "akka-actor/src/testFixtures/scala/akka/Fixtures.scala"},
		nonTests: []string{"akka-actor/src/main/scala/akka/Actor.scala", "akka-actor/src/multi-jvm/scala/akka/X.scala"},
	},
	treesitter.LangGroovy: {
		tests:    []string{"subprojects/core/src/test/groovy/org/gradle/XTest.groovy", "subprojects/core/src/integTest/groovy/org/gradle/YIntegrationTest.groovy"},
		nonTests: []string{"subprojects/core/src/main/groovy/org/gradle/X.groovy"},
	},
	treesitter.LangJavaScript: {
		tests:    []string{"packages/react/src/__tests__/React-test.js", "src/util.test.js", "src/util.spec.jsx"},
		nonTests: []string{"packages/react/src/React.js", "src/__tests__extra/util.js", "src/testUtils.js"},
	},
	treesitter.LangTypeScript: {
		tests:    []string{"src/vs/base/common/uri.test.ts", "src/__tests__/uri.ts"},
		nonTests: []string{"src/vs/base/common/uri.ts", "src/testRunner.ts"},
	},
	treesitter.LangTSX: {
		tests:    []string{"src/Button.test.tsx", "src/__tests__/Button.tsx"},
		nonTests: []string{"src/Button.tsx"},
	},

	treesitter.LangRust: {reason: "tests are an in-file `mod tests`, not a filename"},
	treesitter.LangCSharp: {
		reason: "dotnet test discovers by attribute in a test assembly; the *Tests.cs spelling is a habit no runner enforces",
	},
	treesitter.LangSwift: {reason: "SwiftPM names test target paths in Package.swift, and shipped Sources/*Testing* would be misread"},
	treesitter.LangC:     {reason: "harnesses are wired by the build; no filename rule in the corpus"},
	treesitter.LangCPP:   {reason: "GoogleTest/Catch2 register by macro; the *_test.cc spelling is unused in the corpus"},
	treesitter.LangBash:  {reason: "no runner selects shell files by name; the corpus uses two ad-hoc spellings"},
	treesitter.LangLua:   {reason: "no standard runner; *_spec.lua belongs to busted rather than to Lua"},
	treesitter.LangOCaml: {reason: "dune declares tests in a build stanza, so the .ml file name says nothing"},
	treesitter.LangElm:   {reason: "elm-test discovers exposed Test values inside modules, not by file name"},
}

// TestLangConfig_EveryLanguageHasTestFileDisposition walks the live registry and
// requires a decision for every language in it, then exercises each predicate in
// both directions.
func TestLangConfig_EveryLanguageHasTestFileDisposition(t *testing.T) {
	registered := registrySnapshot()
	require.NotEmpty(t, registered, "setup: the registry must be populated by init")

	var withPredicate, withoutPredicate int
	for lang, cfg := range registered {
		want, known := langTestDispositions[lang]
		require.True(t, known,
			"language %s is registered but has no test-file disposition: add a predicate, or a documented nil with its reason, in %s's config and in langTestDispositions", lang, lang)

		if !want.hasPredicate() {
			assert.Nil(t, cfg.IsTestFile, "language %s is documented as having no convention (%s) but carries a predicate", lang, want.reason)
			assert.False(t, HasTestFilePredicate(lang), "HasTestFilePredicate must agree with the config for %s", lang)
			withoutPredicate++
			continue
		}

		require.NotNil(t, cfg.IsTestFile, "language %s is expected to carry a test-file predicate", lang)
		assert.True(t, HasTestFilePredicate(lang), "HasTestFilePredicate must agree with the config for %s", lang)
		withPredicate++

		require.NotEmpty(t, want.tests, "disposition for %s must carry at least one test-side sample", lang)
		require.NotEmpty(t, want.nonTests, "disposition for %s must carry at least one production-side sample, or the predicate is only checked for presence", lang)
		for _, p := range want.tests {
			assert.True(t, cfg.IsTestFile(p), "%s: %q is test source under this language's convention", lang, p)
		}
		for _, p := range want.nonTests {
			assert.False(t, cfg.IsTestFile(p), "%s: %q is production source and must not be filtered as a test", lang, p)
		}
	}

	// The reverse direction: a table entry for a language nothing registers is a
	// disposition about nothing, and would quietly survive that language's
	// removal.
	for lang := range langTestDispositions {
		_, ok := registered[lang]
		assert.True(t, ok, "langTestDispositions carries %s, which is not registered", lang)
	}

	// Known positives against a table that agrees with itself: both shapes must
	// actually occur, so neither an all-nil registry nor an all-predicate one
	// can satisfy the walk above.
	assert.Positive(t, withPredicate, "at least one language must carry a predicate")
	assert.Positive(t, withoutPredicate, "at least one language must carry a documented nil")
	assert.Equal(t, len(registered), withPredicate+withoutPredicate, "every registered language lands in exactly one disposition")

	// The two dispositions the plan names explicitly, pinned by name so a later
	// edit that flips either one has to say so here.
	assert.True(t, HasTestFilePredicate(treesitter.LangGo), "Go keeps its predicate")
	assert.False(t, HasTestFilePredicate(treesitter.LangRust), "Rust is nil deliberately: its tests are an in-file mod")

	// TestFilePredicateLanguages is what the tool layer offers a caller whose
	// language has no convention, so it must be exactly the predicate set.
	assert.Len(t, TestFilePredicateLanguages(), withPredicate,
		"TestFilePredicateLanguages must name every language carrying a predicate and no others")
}

// TestHasTestFilePredicate_UnregisteredLanguage pins the answer for a language
// the registry has never heard of: no convention, same as a documented nil.
func TestHasTestFilePredicate_UnregisteredLanguage(t *testing.T) {
	assert.False(t, HasTestFilePredicate(treesitter.Language("klingon")))
	assert.False(t, HasTestFilePredicate(treesitter.LangYaml), "a denied language carries no config and so no predicate")
}

// registrySnapshot copies the live registry under its read lock.
func registrySnapshot() map[treesitter.Language]LangConfig {
	langRegistryMu.RLock()
	defer langRegistryMu.RUnlock()
	out := make(map[treesitter.Language]LangConfig, len(langRegistry))
	maps.Copy(out, langRegistry)
	return out
}

// TestLangConfig_EveryLanguageHasCommentKinds asserts every registered language
// declares a non-empty CommentKinds, and that the declared set matches EXACTLY
// the kinds the Step 1.1 census measured for that language. Equality in both
// directions is the check that stops the registration and the measurement from
// drifting apart: a declared kind the census never saw is a hand-guess, and a
// measured kind the registration omits is a comment class the matcher would fail
// to skip. Reading the committed artifact rather than re-parsing keeps this test
// pinned to the same source Phase 2 populated the registrations from.
func TestLangConfig_EveryLanguageHasCommentKinds(t *testing.T) {
	registered := registrySnapshot()
	require.NotEmpty(t, registered, "setup: the registry must be populated by init")

	// The registry-size floor is its OWN NAMED SUBTEST because an
	// exhaustive-over-the-registry assertion passes vacuously against an empty or
	// truncated registry. The name is LOCKED cross-phase vocabulary.
	t.Run("registry_holds_at_least_21_languages", func(t *testing.T) {
		require.GreaterOrEqualf(t, len(registered), registeredLangFloor,
			"the registry holds %d languages, below the floor of %d — a dropped registration makes this exhaustive check weaker",
			len(registered), registeredLangFloor)
	})

	census := loadCommentKindsArtifact(t)
	for lang, cfg := range registered {
		require.NotEmptyf(t, cfg.CommentKinds,
			"language %s declares no CommentKinds; every registered grammar emits at least one comment kind, so a nil list is a missing registration, not a documented disposition", lang)

		measured, ok := census[string(lang)]
		require.Truef(t, ok, "language %s has no comment_kinds row in %s — regenerate the Step 1.1 census", lang, commentCensusName)

		declared := map[string]struct{}{}
		for _, kind := range cfg.CommentKinds {
			declared[kind] = struct{}{}
			if _, found := measured[kind]; !found {
				t.Errorf("language %s declares CommentKinds entry %q that the census did not measure; the registration is a hand-guess — regenerate the census or fix the registration", lang, kind)
			}
		}
		for kind := range measured {
			if _, found := declared[kind]; !found {
				t.Errorf("language %s's census measured comment kind %q that the registration omits; the matcher would fail to skip it — add it to this language's CommentKinds", lang, kind)
			}
		}
	}
}
