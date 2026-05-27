// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"testing"
)

// allFrameworks enumerates every Framework constant declared in framework.go
// (excluding FrameworkNone). Adding a new constant MUST extend this list, by
// design — reflection-free explicit enumeration produces predictable failure
// messages and forces a future contributor to think about coverage in the
// related TestDetectFrameworksTableCoverage criterion.
var allFrameworks = []Framework{
	// JS/TS.
	FrameworkJSJest, FrameworkJSVitest, FrameworkJSMocha, FrameworkJSJasmine,
	FrameworkJSAva, FrameworkJSTape, FrameworkJSNodeTest, FrameworkJSBunTest,
	FrameworkJSPlaywright, FrameworkJSCypress,
	// Java.
	FrameworkJavaJUnit4, FrameworkJavaJUnit5, FrameworkJavaTestNG,
	// Kotlin.
	FrameworkKotlinJUnit, FrameworkKotlinKotest, FrameworkKotlinSpek,
	// Scala.
	FrameworkScalaScalaTest, FrameworkScalaMUnit, FrameworkScalaSpecs2, FrameworkScalaJUnit,
	// Python.
	FrameworkPyPyTest, FrameworkPyUnittest,
	// Ruby.
	FrameworkRubyRSpec, FrameworkRubyMinitest, FrameworkRubyTestUnit,
	// C#.
	FrameworkCSNUnit, FrameworkCSXUnit, FrameworkCSMSTest,
	// Swift.
	FrameworkSwiftXCTest, FrameworkSwiftTesting,
	// Rust.
	FrameworkRustTest, FrameworkRustTokio, FrameworkRustRSTest,
	FrameworkRustProptest, FrameworkRustQuickcheck,
	// PHP.
	FrameworkPHPPHPUnit, FrameworkPHPPest, FrameworkPHPCodeception,
	// Elixir.
	FrameworkElixirExUnit,
	// C/C++.
	FrameworkCppGTest, FrameworkCppCatch2, FrameworkCppDoctest,
	FrameworkCppBoostTest, FrameworkCppUnity, FrameworkCppCMocka,
	// Lua.
	FrameworkLuaBusted, FrameworkLuaLuaUnit,
	// Bash.
	FrameworkBashBats,
	// Groovy.
	FrameworkGroovySpock, FrameworkGroovyJUnit,
	// Elm.
	FrameworkElmTest,
	// OCaml.
	FrameworkOCamlAlcotest, FrameworkOCamlPpxInlineTest,
	// Go.
	FrameworkGoTesting,
	// HCL.
	FrameworkHCLTfTest,
}

// TestFrameworkConstantsUnique asserts every declared Framework constant has
// a distinct string value. Catches accidental duplicates from copy-paste
// across the per-language blocks (e.g. a regression that gives
// FrameworkKotlinJUnit and FrameworkScalaJUnit the same string instead of
// distinct kotlin-junit / scala-junit values). Also asserts the fixed count
// of 55 — adding a constant must extend allFrameworks AND bump this number.
func TestFrameworkConstantsUnique(t *testing.T) {
	const expectedCount = 55
	if got := len(allFrameworks); got != expectedCount {
		t.Fatalf("allFrameworks has %d entries, expected %d — extend allFrameworks if you added a new Framework constant", got, expectedCount)
	}

	seen := make(map[Framework]bool, len(allFrameworks))
	for _, fw := range allFrameworks {
		if fw == FrameworkNone {
			t.Errorf("FrameworkNone leaked into allFrameworks")
			continue
		}
		if seen[fw] {
			t.Errorf("duplicate Framework value: %q", fw)
		}
		seen[fw] = true
	}

	// FrameworkNone must remain the empty string zero value.
	if FrameworkNone != "" {
		t.Errorf("FrameworkNone = %q, want empty string", FrameworkNone)
	}
}

// TestDetectFrameworks exercises the (lang, imports) → []Framework mapping
// across every language with detection rules. Order-discipline cases (Java
// JUnit5 before JUnit4), shape variance (Python mixed, Rust whole-statement,
// C++ angle-bracket includes), language-without-table sanity (Ruby/Lua/Bash/
// Elixir/HCL/Go return nil), and duplicate-import dedup are covered.
func TestDetectFrameworks(t *testing.T) {
	tests := []struct {
		name    string
		lang    Language
		imports []string
		want    []Framework
	}{
		// JS/TS — clean module specifiers.
		{"js-jest-bare", LangJavaScript, []string{"jest"}, []Framework{FrameworkJSJest}},
		{"js-jest-scoped", LangJavaScript, []string{"@jest/globals"}, []Framework{FrameworkJSJest}},
		{"js-vitest", LangJavaScript, []string{"vitest"}, []Framework{FrameworkJSVitest}},
		{"js-vitest-scoped", LangJavaScript, []string{"@vitest/spy"}, []Framework{FrameworkJSVitest}},
		{"js-vitest-subpath", LangJavaScript, []string{"vitest/config"}, []Framework{FrameworkJSVitest}},
		{"js-mocha", LangJavaScript, []string{"mocha"}, []Framework{FrameworkJSMocha}},
		{"js-jasmine", LangJavaScript, []string{"jasmine"}, []Framework{FrameworkJSJasmine}},
		{"js-jasmine-core", LangJavaScript, []string{"jasmine-core"}, []Framework{FrameworkJSJasmine}},
		{"js-ava", LangJavaScript, []string{"ava"}, []Framework{FrameworkJSAva}},
		{"js-tape", LangJavaScript, []string{"tape"}, []Framework{FrameworkJSTape}},
		{"js-node-test-prefix", LangJavaScript, []string{"node:test"}, []Framework{FrameworkJSNodeTest}},
		{"js-node-test-bare", LangJavaScript, []string{"test"}, []Framework{FrameworkJSNodeTest}},
		{"js-bun-test", LangJavaScript, []string{"bun:test"}, []Framework{FrameworkJSBunTest}},
		{"js-playwright-test", LangJavaScript, []string{"@playwright/test"}, []Framework{FrameworkJSPlaywright}},
		{"js-playwright-bare", LangJavaScript, []string{"playwright"}, []Framework{FrameworkJSPlaywright}},
		{"js-cypress", LangJavaScript, []string{"cypress"}, []Framework{FrameworkJSCypress}},
		{"js-cypress-subpath", LangJavaScript, []string{"cypress/plugins"}, []Framework{FrameworkJSCypress}},
		{"ts-jest", LangTypeScript, []string{"jest"}, []Framework{FrameworkJSJest}},
		{"ts-vitest", LangTypeScript, []string{"vitest"}, []Framework{FrameworkJSVitest}},
		{"js-multi", LangJavaScript, []string{"jest", "@playwright/test"}, []Framework{FrameworkJSJest, FrameworkJSPlaywright}},
		{"js-dedup", LangJavaScript, []string{"jest", "@jest/globals"}, []Framework{FrameworkJSJest}},
		{"js-no-match", LangJavaScript, []string{"react", "lodash"}, nil},

		// Java — whole-statement text. Order: JUnit5 before JUnit4.
		{"java-junit5", LangJava, []string{"import org.junit.jupiter.api.Test;"}, []Framework{FrameworkJavaJUnit5}},
		{"java-junit4", LangJava, []string{"import org.junit.Test;"}, []Framework{FrameworkJavaJUnit4}},
		{"java-junit4-not-jupiter", LangJava, []string{"import org.junit.Assert;"}, []Framework{FrameworkJavaJUnit4}},
		{"java-testng", LangJava, []string{"import org.testng.annotations.Test;"}, []Framework{FrameworkJavaTestNG}},
		{"java-junit5-only-not-junit4", LangJava, []string{"import org.junit.jupiter.api.Test;"}, []Framework{FrameworkJavaJUnit5}},
		{"java-mixed", LangJava, []string{"import org.junit.jupiter.api.Test;", "import org.testng.annotations.Test;"}, []Framework{FrameworkJavaJUnit5, FrameworkJavaTestNG}},

		// Kotlin — clean dotted-identifier text.
		{"kotlin-junit", LangKotlin, []string{"org.junit.Test"}, []Framework{FrameworkKotlinJUnit}},
		{"kotlin-kotest", LangKotlin, []string{"io.kotest.core.spec.style.FunSpec"}, []Framework{FrameworkKotlinKotest}},
		{"kotlin-spek", LangKotlin, []string{"org.spekframework.spek2.Spek"}, []Framework{FrameworkKotlinSpek}},

		// Scala — whole-statement text.
		{"scala-scalatest", LangScala, []string{"import org.scalatest.funsuite.AnyFunSuite"}, []Framework{FrameworkScalaScalaTest}},
		{"scala-munit", LangScala, []string{"import munit.FunSuite"}, []Framework{FrameworkScalaMUnit}},
		{"scala-specs2", LangScala, []string{"import org.specs2.mutable.Specification"}, []Framework{FrameworkScalaSpecs2}},
		{"scala-junit", LangScala, []string{"import org.junit.Test"}, []Framework{FrameworkScalaJUnit}},

		// Python — mixed shape.
		{"py-pytest-from", LangPython, []string{"pytest"}, []Framework{FrameworkPyPyTest}},
		{"py-pytest-bare-import", LangPython, []string{"import pytest"}, []Framework{FrameworkPyPyTest}},
		{"py-pytest-dotted", LangPython, []string{"pytest.fixture"}, []Framework{FrameworkPyPyTest}},
		{"py-unittest-from", LangPython, []string{"unittest"}, []Framework{FrameworkPyUnittest}},
		{"py-unittest-bare-import", LangPython, []string{"import unittest"}, []Framework{FrameworkPyUnittest}},
		{"py-unittest-mock", LangPython, []string{"unittest.mock"}, []Framework{FrameworkPyUnittest}},

		// C# — whole-statement text.
		{"cs-nunit", LangCSharp, []string{"using NUnit.Framework;"}, []Framework{FrameworkCSNUnit}},
		{"cs-xunit", LangCSharp, []string{"using Xunit;"}, []Framework{FrameworkCSXUnit}},
		{"cs-mstest", LangCSharp, []string{"using Microsoft.VisualStudio.TestTools.UnitTesting;"}, []Framework{FrameworkCSMSTest}},

		// Swift — whole-statement text.
		{"swift-xctest", LangSwift, []string{"import XCTest"}, []Framework{FrameworkSwiftXCTest}},
		{"swift-testing", LangSwift, []string{"import Testing"}, []Framework{FrameworkSwiftTesting}},

		// Rust — whole use_declaration text.
		{"rust-tokio-test", LangRust, []string{"use tokio::test;"}, []Framework{FrameworkRustTokio}},
		{"rust-tokio-test-underscore", LangRust, []string{"use tokio_test::block_on;"}, []Framework{FrameworkRustTokio}},
		{"rust-rstest", LangRust, []string{"use rstest::rstest;"}, []Framework{FrameworkRustRSTest}},
		{"rust-proptest", LangRust, []string{"use proptest::prelude::*;"}, []Framework{FrameworkRustProptest}},
		{"rust-quickcheck", LangRust, []string{"use quickcheck::QuickCheck;"}, []Framework{FrameworkRustQuickcheck}},
		{"rust-stdlib-no-import-detection", LangRust, []string{"use std::collections::HashMap;"}, nil},

		// PHP — whole namespace_use_declaration.
		{"php-phpunit", LangPHP, []string{"use PHPUnit\\Framework\\TestCase;"}, []Framework{FrameworkPHPPHPUnit}},
		{"php-pest", LangPHP, []string{"use Pest\\TestCase;"}, []Framework{FrameworkPHPPest}},
		{"php-codeception", LangPHP, []string{"use Codeception\\Test\\Unit;"}, []Framework{FrameworkPHPCodeception}},

		// C/C++ — angle-bracket-tolerant via Contains. extractFileContext
		// strips quotes but not brackets, so both shapes can surface.
		{"cpp-gtest-bracket", LangCPP, []string{"<gtest/gtest.h>"}, []Framework{FrameworkCppGTest}},
		{"cpp-gtest-quoted", LangCPP, []string{"gtest/gtest.h"}, []Framework{FrameworkCppGTest}},
		{"cpp-catch2-v3", LangCPP, []string{"<catch2/catch_test_macros.hpp>"}, []Framework{FrameworkCppCatch2}},
		{"cpp-catch2-v2", LangCPP, []string{"catch.hpp"}, []Framework{FrameworkCppCatch2}},
		{"cpp-doctest", LangCPP, []string{"<doctest/doctest.h>"}, []Framework{FrameworkCppDoctest}},
		{"cpp-boost-test", LangCPP, []string{"<boost/test/unit_test.hpp>"}, []Framework{FrameworkCppBoostTest}},
		{"c-unity", LangC, []string{"unity.h"}, []Framework{FrameworkCppUnity}},
		{"c-cmocka", LangC, []string{"cmocka.h"}, []Framework{FrameworkCppCMocka}},

		// Groovy — whole groovy_import.
		{"groovy-spock", LangGroovy, []string{"import spock.lang.Specification"}, []Framework{FrameworkGroovySpock}},
		{"groovy-junit", LangGroovy, []string{"import org.junit.Test"}, []Framework{FrameworkGroovyJUnit}},

		// Elm — whole import_clause.
		{"elm-test", LangElm, []string{"import Test exposing (Test, test)"}, []Framework{FrameworkElmTest}},
		{"elm-expect", LangElm, []string{"import Expect"}, []Framework{FrameworkElmTest}},
		{"elm-fuzz", LangElm, []string{"import Fuzz"}, []Framework{FrameworkElmTest}},

		// OCaml — whole open_module.
		{"ocaml-alcotest", LangOCaml, []string{"open Alcotest"}, []Framework{FrameworkOCamlAlcotest}},
		{"ocaml-ppx-inline-test-lower", LangOCaml, []string{"open Ppx_inline_test"}, []Framework{FrameworkOCamlPpxInlineTest}},
		{"ocaml-ppx-inline-test-snake", LangOCaml, []string{"ppx_inline_test"}, []Framework{FrameworkOCamlPpxInlineTest}},

		// Languages with no detection table — return nil.
		{"go-no-table", LangGo, []string{"testing"}, nil},
		{"ruby-no-table", LangRuby, []string{"rspec"}, nil},
		{"lua-no-table", LangLua, []string{"busted"}, nil},
		{"bash-no-table", LangBash, []string{"bats"}, nil},
		{"elixir-no-table", LangElixir, []string{"ExUnit"}, nil},
		{"hcl-no-table", LangHCL, []string{"anything"}, nil},

		// Empty / nil inputs.
		{"empty-imports", LangJavaScript, nil, nil},
		{"empty-string-import", LangJavaScript, []string{}, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectFrameworks(tt.lang, tt.imports)
			if !frameworksEqual(got, tt.want) {
				t.Errorf("DetectFrameworks(%v, %v) = %v, want %v", tt.lang, tt.imports, got, tt.want)
			}
		})
	}
}

func frameworksEqual(a, b []Framework) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestDetectFrameworksTableCoverage asserts every Framework constant is
// either reachable by at least one frameworkTables rule OR documented as
// detected via non-import signals (Bucket A/B AST predicates). Catches the
// failure mode where a constant is declared but no rule produces it.
func TestDetectFrameworksTableCoverage(t *testing.T) {
	// Frameworks intentionally not produced by DetectFrameworks — Bucket A's
	// AST/filename predicates set them via non-import paths.
	astOnly := map[Framework]string{
		FrameworkRustTest:     "Rust stdlib #[test] — Bucket A AST predicate",
		FrameworkGoTesting:    "Go stdlib testing — Bucket A filename + AST predicate",
		FrameworkRubyRSpec:    "Ruby has no Imports query — Bucket B AST signals",
		FrameworkRubyMinitest: "Ruby has no Imports query — Bucket B AST signals",
		FrameworkRubyTestUnit: "Ruby has no Imports query — Bucket B AST signals",
		FrameworkLuaBusted:    "Lua has no Imports query — Bucket B AST signals",
		FrameworkLuaLuaUnit:   "Lua has no Imports query — Bucket B AST signals",
		FrameworkBashBats:     "Bash has no Imports query — Bucket B AST signals",
		FrameworkElixirExUnit: "Elixir has no Imports query — Bucket B AST signals",
		FrameworkHCLTfTest:    "HCL has no Imports query — extender via *.tftest.hcl filename",
	}

	produced := make(map[Framework]bool)
	for _, rules := range frameworkTables {
		for _, r := range rules {
			produced[r.fw] = true
		}
	}

	for _, fw := range allFrameworks {
		if produced[fw] {
			continue
		}
		if _, allowed := astOnly[fw]; !allowed {
			t.Errorf("Framework %q is declared but not produced by any frameworkTables rule and not in the astOnly allowlist — either add a rule or document the non-import detection path in astOnly", fw)
		}
	}
}
