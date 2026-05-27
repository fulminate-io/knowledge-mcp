// SPDX-License-Identifier: Apache-2.0

package treesitter

// Framework identifies a test framework detected from a file's imports.
// The 54-value contract is a per-language-prefixed enum: each constant is
// named Framework<LangPrefix><Name> and its string value is the kebab-case
// rendering of the suffix (e.g. FrameworkJavaJUnit5 = "java-junit5"). The
// language prefix guarantees uniqueness — JUnit appears under java/kotlin/
// scala/groovy without value collision. The empty string FrameworkNone is
// the zero value meaning "no framework detected".
//
// Producer-side type only: `type Framework string` lives in this package.
// Frameworks are NOT persisted on the graph node — they are a per-file fact
// computed at chunk time (rule 42dba7de — store does not import domain
// packages). Predicates in sibling chunker_<lang>.go files read them from
// chunk.Context.Frameworks. Mirrors how `type TestKind string` is producer-
// typed and how `type Language string` is stored as plain string when it
// crosses the persistence boundary.
type Framework string

const (
	FrameworkNone Framework = ""

	// JS/TS — shared between LangJavaScript and LangTypeScript.
	FrameworkJSJest       Framework = "js-jest"
	FrameworkJSVitest     Framework = "js-vitest"
	FrameworkJSMocha      Framework = "js-mocha"
	FrameworkJSJasmine    Framework = "js-jasmine"
	FrameworkJSAva        Framework = "js-ava"
	FrameworkJSTape       Framework = "js-tape"
	FrameworkJSNodeTest   Framework = "js-node-test"
	FrameworkJSBunTest    Framework = "js-bun-test"
	FrameworkJSPlaywright Framework = "js-playwright"
	FrameworkJSCypress    Framework = "js-cypress"

	// Java.
	FrameworkJavaJUnit4 Framework = "java-junit4"
	FrameworkJavaJUnit5 Framework = "java-junit5"
	FrameworkJavaTestNG Framework = "java-testng"

	// Kotlin.
	FrameworkKotlinJUnit  Framework = "kotlin-junit"
	FrameworkKotlinKotest Framework = "kotlin-kotest"
	FrameworkKotlinSpek   Framework = "kotlin-spek"

	// Scala.
	FrameworkScalaScalaTest Framework = "scala-scalatest"
	FrameworkScalaMUnit     Framework = "scala-munit"
	FrameworkScalaSpecs2    Framework = "scala-specs2"
	FrameworkScalaJUnit     Framework = "scala-junit"

	// Python.
	FrameworkPyPyTest   Framework = "py-pytest"
	FrameworkPyUnittest Framework = "py-unittest"

	// Ruby.
	FrameworkRubyRSpec    Framework = "ruby-rspec"
	FrameworkRubyMinitest Framework = "ruby-minitest"
	FrameworkRubyTestUnit Framework = "ruby-test-unit"

	// C#.
	FrameworkCSNUnit  Framework = "cs-nunit"
	FrameworkCSXUnit  Framework = "cs-xunit"
	FrameworkCSMSTest Framework = "cs-mstest"

	// Swift.
	FrameworkSwiftXCTest  Framework = "swift-xctest"
	FrameworkSwiftTesting Framework = "swift-testing"

	// Rust. FrameworkRustTest is exported for Bucket A's stdlib #[test]
	// AST predicate to mark on the framework set; DetectFrameworks itself
	// does not produce it (Rust stdlib testing has no import to detect).
	FrameworkRustTest       Framework = "rust-test"
	FrameworkRustTokio      Framework = "rust-tokio"
	FrameworkRustRSTest     Framework = "rust-rstest"
	FrameworkRustProptest   Framework = "rust-proptest"
	FrameworkRustQuickcheck Framework = "rust-quickcheck"

	// PHP.
	FrameworkPHPPHPUnit     Framework = "php-phpunit"
	FrameworkPHPPest        Framework = "php-pest"
	FrameworkPHPCodeception Framework = "php-codeception"

	// Elixir.
	FrameworkElixirExUnit Framework = "elixir-exunit"

	// C/C++ — shared between LangC and LangCPP. gtest/Catch2/etc. are
	// usable in both languages, so the constants are Cpp-prefixed by
	// convention rather than split by language.
	FrameworkCppGTest     Framework = "cpp-gtest"
	FrameworkCppCatch2    Framework = "cpp-catch2"
	FrameworkCppDoctest   Framework = "cpp-doctest"
	FrameworkCppBoostTest Framework = "cpp-boost-test"
	FrameworkCppUnity     Framework = "cpp-unity"
	FrameworkCppCMocka    Framework = "cpp-cmocka"

	// Lua.
	FrameworkLuaBusted  Framework = "lua-busted"
	FrameworkLuaLuaUnit Framework = "lua-luaunit"

	// Bash.
	FrameworkBashBats Framework = "bash-bats"

	// Groovy.
	FrameworkGroovySpock Framework = "groovy-spock"
	FrameworkGroovyJUnit Framework = "groovy-junit"

	// Elm.
	FrameworkElmTest Framework = "elm-test"

	// OCaml.
	FrameworkOCamlAlcotest      Framework = "ocaml-alcotest"
	FrameworkOCamlPpxInlineTest Framework = "ocaml-ppx-inline-test"

	// Go. FrameworkGoTesting is exported for Bucket A's filename + AST
	// predicate to mark on the framework set; DetectFrameworks itself
	// does not produce it (Bucket A handles Go via filename+AST).
	FrameworkGoTesting Framework = "go-testing"

	// HCL. FrameworkHCLTfTest is exported for the framework extender to mark
	// when the file's name ends in `.tftest.hcl` (Terraform 1.6+ test files).
	// HCL has no Imports query so DetectFrameworks cannot produce it; the
	// extender uses the filename suffix as the only signal.
	FrameworkHCLTfTest Framework = "hcl-tftest"
)

// DetectFrameworks returns the set of test frameworks detected from the
// file's import strings, given the file's language. Empty result means
// no framework was detected — which can mean (a) the file has no test
// imports, (b) the language has no tree-sitter Imports query (Ruby, Lua,
// Bash, Elixir, HCL — those Bucket B predicates use AST signals), or (c)
// the language has no framework concept (every non-test-bearing language).
//
// Note: FrameworkRustTest and FrameworkGoTesting are exported constants
// but are NOT produced by this function — Bucket A's filename + AST
// predicates set them directly on the framework set.
//
// Imports content varies by tree-sitter @path vs @import capture style:
// Go/JS/TS/Python(partial)/Kotlin/C/C++ provide clean path strings
// ("fmt", "jest"); Java/Scala/PHP/Rust/Swift/Elm/OCaml/Groovy/C# provide
// the entire import statement text ("import org.junit.Test;",
// "use std::collections::HashMap;"). C/C++ retains angle brackets on
// system includes (extractFileContext strips quotes, not brackets); the
// per-language match rules in framework_tables.go account for both shapes.
func DetectFrameworks(lang Language, imports []string) []Framework {
	rules, ok := frameworkTables[lang]
	if !ok || len(rules) == 0 || len(imports) == 0 {
		return nil
	}
	seen := make(map[Framework]struct{}, len(rules))
	var out []Framework
	for _, imp := range imports {
		for _, r := range rules {
			if _, dup := seen[r.fw]; dup {
				continue
			}
			if r.matches(imp) {
				seen[r.fw] = struct{}{}
				out = append(out, r.fw)
			}
		}
	}
	return out
}
