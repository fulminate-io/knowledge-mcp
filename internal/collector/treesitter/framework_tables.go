// SPDX-License-Identifier: Apache-2.0

package treesitter

import "strings"

// frameworkRule pairs a target Framework with an import-text matcher.
// Matchers use stdlib strings.HasPrefix / strings.Contains / equality —
// deliberately no regex (locked scope: "No fuzzy matching of import paths").
type frameworkRule struct {
	fw      Framework
	matches func(importText string) bool
}

// frameworkTables is the per-language ordered match list. Languages with
// nil tables (Go, HCL, Ruby, Lua, Bash, Elixir, and every non-test-bearing
// language) intentionally fall through DetectFrameworks → nil. Bucket A/B
// predicates produce frameworks for those languages via AST signals.
//
// Order discipline: within each language's rule list, the more-specific
// rule MUST come first when prefixes overlap. Java JUnit 5 (jupiter)
// before JUnit 4 generic (org.junit.) is the canonical example — without
// the order, an org.junit.jupiter.* import would false-match JUnit 4.
var frameworkTables = map[Language][]frameworkRule{
	LangJavaScript: jsFrameworkRules,
	LangTypeScript: jsFrameworkRules,
	LangTSX:        jsFrameworkRules, // .tsx shares the JS/TS import-matcher rules.
	LangJava:       javaFrameworkRules,
	LangKotlin:     kotlinFrameworkRules,
	LangScala:      scalaFrameworkRules,
	LangPython:     pythonFrameworkRules,
	LangCSharp:     csharpFrameworkRules,
	LangSwift:      swiftFrameworkRules,
	LangRust:       rustFrameworkRules,
	LangPHP:        phpFrameworkRules,
	LangC:          cppFrameworkRules,
	LangCPP:        cppFrameworkRules,
	LangGroovy:     groovyFrameworkRules,
	LangElm:        elmFrameworkRules,
	LangOCaml:      ocamlFrameworkRules,
	// Go, Ruby, Lua, Bash, Elixir, HCL: no entry — handled by Bucket A/B.
}

// JS/TS: @path captures clean module specifiers ("jest", "@vitest/spy").
var jsFrameworkRules = []frameworkRule{
	{FrameworkJSJest, func(s string) bool {
		return s == "jest" || strings.HasPrefix(s, "@jest/")
	}},
	{FrameworkJSVitest, func(s string) bool {
		return s == "vitest" || strings.HasPrefix(s, "@vitest/") || strings.HasPrefix(s, "vitest/")
	}},
	{FrameworkJSMocha, func(s string) bool { return s == "mocha" }},
	{FrameworkJSJasmine, func(s string) bool {
		return s == "jasmine" || s == "jasmine-core"
	}},
	{FrameworkJSAva, func(s string) bool { return s == "ava" }},
	{FrameworkJSTape, func(s string) bool { return s == "tape" }},
	{FrameworkJSNodeTest, func(s string) bool {
		return s == "node:test" || s == "test"
	}},
	{FrameworkJSBunTest, func(s string) bool { return s == "bun:test" }},
	{FrameworkJSPlaywright, func(s string) bool {
		return s == "@playwright/test" || strings.HasPrefix(s, "@playwright/") || s == "playwright"
	}},
	{FrameworkJSCypress, func(s string) bool {
		return s == "cypress" || strings.HasPrefix(s, "cypress/")
	}},
}

// Java: @import captures whole statement text ("import org.junit.Test;").
// Order: JUnit 5 (jupiter) before JUnit 4 (generic org.junit.).
var javaFrameworkRules = []frameworkRule{
	{FrameworkJavaJUnit5, func(s string) bool {
		return strings.Contains(s, "org.junit.jupiter.")
	}},
	{FrameworkJavaJUnit4, func(s string) bool {
		return strings.Contains(s, "org.junit.") && !strings.Contains(s, "org.junit.jupiter.")
	}},
	{FrameworkJavaTestNG, func(s string) bool {
		return strings.Contains(s, "org.testng.")
	}},
}

// Kotlin: @path captures clean dotted-identifier text ("org.junit.jupiter").
var kotlinFrameworkRules = []frameworkRule{
	{FrameworkKotlinKotest, func(s string) bool {
		return strings.HasPrefix(s, "io.kotest.")
	}},
	{FrameworkKotlinSpek, func(s string) bool {
		return strings.HasPrefix(s, "org.spekframework.")
	}},
	{FrameworkKotlinJUnit, func(s string) bool {
		return strings.HasPrefix(s, "org.junit.")
	}},
}

// Scala: @import captures whole statement text.
var scalaFrameworkRules = []frameworkRule{
	{FrameworkScalaScalaTest, func(s string) bool {
		return strings.Contains(s, "org.scalatest.")
	}},
	{FrameworkScalaMUnit, func(s string) bool {
		return strings.Contains(s, "munit.") || strings.Contains(s, "munit ")
	}},
	{FrameworkScalaSpecs2, func(s string) bool {
		return strings.Contains(s, "org.specs2.")
	}},
	{FrameworkScalaJUnit, func(s string) bool {
		return strings.Contains(s, "org.junit.")
	}},
}

// Python: mixed shape per queries_python.go — `from X import Y` gives clean
// dotted_name ("pytest", "unittest"), bare `import X` gives full statement
// text ("import pytest"). Rules accept both.
var pythonFrameworkRules = []frameworkRule{
	{FrameworkPyPyTest, func(s string) bool {
		return s == "pytest" || strings.HasPrefix(s, "pytest.") || strings.Contains(s, "import pytest")
	}},
	{FrameworkPyUnittest, func(s string) bool {
		return s == "unittest" || strings.HasPrefix(s, "unittest.") || strings.Contains(s, "import unittest")
	}},
}

// C#: @import captures whole using_directive ("using NUnit.Framework;").
var csharpFrameworkRules = []frameworkRule{
	{FrameworkCSNUnit, func(s string) bool {
		return strings.Contains(s, "NUnit.Framework")
	}},
	{FrameworkCSXUnit, func(s string) bool {
		return strings.Contains(s, "Xunit")
	}},
	{FrameworkCSMSTest, func(s string) bool {
		return strings.Contains(s, "Microsoft.VisualStudio.TestTools")
	}},
}

// Swift: @import captures whole import_declaration ("import XCTest").
// Order: XCTest before Testing — "import Testing" alone could otherwise
// false-match a substring search on "Testing" inside the string "XCTesting".
var swiftFrameworkRules = []frameworkRule{
	{FrameworkSwiftXCTest, func(s string) bool {
		return strings.Contains(s, "import XCTest")
	}},
	{FrameworkSwiftTesting, func(s string) bool {
		return strings.Contains(s, "import Testing")
	}},
}

// Rust: @import captures whole use_declaration ("use tokio::test;").
// FrameworkRustTest (stdlib #[test]) is intentionally not produced here —
// Bucket A's AST predicate handles that.
var rustFrameworkRules = []frameworkRule{
	{FrameworkRustTokio, func(s string) bool {
		return strings.Contains(s, "tokio::test") || strings.Contains(s, "tokio_test")
	}},
	{FrameworkRustRSTest, func(s string) bool {
		return strings.Contains(s, "rstest")
	}},
	{FrameworkRustProptest, func(s string) bool {
		return strings.Contains(s, "proptest")
	}},
	{FrameworkRustQuickcheck, func(s string) bool {
		return strings.Contains(s, "quickcheck")
	}},
}

// PHP: @import captures whole namespace_use_declaration.
var phpFrameworkRules = []frameworkRule{
	{FrameworkPHPPHPUnit, func(s string) bool {
		return strings.Contains(s, "PHPUnit\\")
	}},
	{FrameworkPHPPest, func(s string) bool {
		return strings.Contains(s, "Pest\\")
	}},
	{FrameworkPHPCodeception, func(s string) bool {
		return strings.Contains(s, "Codeception\\")
	}},
}

// C/C++: @path captures the include path. extractFileContext strips quotes
// (`"`, `'`, “ ` “) but NOT angle brackets, so a `#include <gtest/gtest.h>`
// surfaces as "<gtest/gtest.h>". Rules use strings.Contains for tolerance —
// bracket-bearing or bracket-stripped both match.
var cppFrameworkRules = []frameworkRule{
	{FrameworkCppGTest, func(s string) bool {
		return strings.Contains(s, "gtest/") || strings.Contains(s, "gtest.h")
	}},
	{FrameworkCppCatch2, func(s string) bool {
		return strings.Contains(s, "catch2/") || strings.Contains(s, "catch.hpp") || strings.Contains(s, "catch_amalgamated.hpp")
	}},
	{FrameworkCppDoctest, func(s string) bool {
		return strings.Contains(s, "doctest")
	}},
	{FrameworkCppBoostTest, func(s string) bool {
		return strings.Contains(s, "boost/test/")
	}},
	{FrameworkCppUnity, func(s string) bool {
		return strings.Contains(s, "unity.h") || strings.Contains(s, "unity_internals.h")
	}},
	{FrameworkCppCMocka, func(s string) bool {
		return strings.Contains(s, "cmocka.h")
	}},
}

// Groovy: @import captures whole groovy_import.
var groovyFrameworkRules = []frameworkRule{
	{FrameworkGroovySpock, func(s string) bool {
		return strings.Contains(s, "spock.lang.")
	}},
	{FrameworkGroovyJUnit, func(s string) bool {
		return strings.Contains(s, "org.junit.")
	}},
}

// Elm: @path captures whole import_clause ("import Test exposing (...)").
var elmFrameworkRules = []frameworkRule{
	{FrameworkElmTest, func(s string) bool {
		return strings.Contains(s, "import Test") ||
			strings.Contains(s, "import Expect") ||
			strings.Contains(s, "import Fuzz")
	}},
}

// OCaml: @path captures whole open_module ("open Alcotest").
// Order: ppx_inline_test before Alcotest — "Ppx_inline_test" contains no
// "Alcotest" substring, but ordering keeps the more-specific match canonical.
var ocamlFrameworkRules = []frameworkRule{
	{FrameworkOCamlPpxInlineTest, func(s string) bool {
		return strings.Contains(s, "ppx_inline_test") || strings.Contains(s, "Ppx_inline_test")
	}},
	{FrameworkOCamlAlcotest, func(s string) bool {
		return strings.Contains(s, "Alcotest")
	}},
}
