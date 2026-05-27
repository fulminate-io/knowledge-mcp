// SPDX-License-Identifier: Apache-2.0

package treesitter

func cQueries() *QuerySet {
	return &QuerySet{
		TopLevel: `[
			(function_definition
				declarator: (function_declarator
					declarator: (identifier) @name)) @decl
			(struct_specifier name: (type_identifier) @name) @decl
			(enum_specifier name: (type_identifier) @name) @decl
			(type_definition) @decl
			(declaration) @decl
		]`,
		Calls: `(call_expression function: (identifier) @callee)`,
		Imports: `(preproc_include path: [
			(string_literal) @path
			(system_lib_string) @path
		])`,
		TypeRefs: `(type_identifier) @typeref`,
		// TestBlocks: gtest TEST/TEST_F/TEST_P macros (when grammar parses
		// them as call_expression — the C grammar does), Catch2 TEST_CASE,
		// Boost.Test BOOST_AUTO_TEST_CASE, Unity RUN_TEST, cmocka cmocka_unit_test,
		// Google-Benchmark BENCHMARK, doctest TEST_CASE.
		//
		// In the C tree-sitter grammar, `TEST(MathTest, AddIntegers) { ... }`
		// at the top level parses as a (call_expression) followed by a
		// (compound_statement) — verified empirically. The C++ grammar
		// instead parses the same source as a function_definition; the
		// queries_cpp.go variant captures that shape.
		// Patterns use the `.` anchor to bind @name on the first argument
		// only — Catch2 TEST_CASE("name", "[tag]") binds @name to "name",
		// not "[tag]". The two-identifier pattern (gtest TEST/TEST_F shape)
		// binds @suite to first identifier and @name to second; the
		// single-identifier pattern is unreachable because gtest macros
		// always have two identifiers and the query engine prefers the
		// first matching pattern. RUN_TEST(fn) and BENCHMARK(fn) are
		// single-identifier — handled by the single-identifier pattern.
		TestBlocks: `[
			(call_expression
				function: (identifier) @fn
				arguments: (argument_list . (identifier) @suite . (identifier) @name)
			) @decl
			(call_expression
				function: (identifier) @fn
				arguments: (argument_list . (string_literal) @name)
			) @decl
			(call_expression
				function: (identifier) @fn
				arguments: (argument_list . (identifier) @name .)
			) @decl
		] (#match? @fn "^(TEST|TEST_F|TEST_P|TEST_CASE|SECTION|SCENARIO|GIVEN|WHEN|THEN|BOOST_AUTO_TEST_CASE|BOOST_FIXTURE_TEST_CASE|RUN_TEST|BENCHMARK|MOCK_METHOD|MOCK_CONST_METHOD|cmocka_unit_test|cmocka_unit_test_setup_teardown|TYPED_TEST|TYPED_TEST_P|INSTANTIATE_TEST_SUITE_P)$")`,
	}
}
