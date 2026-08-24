// SPDX-License-Identifier: Apache-2.0

package treesitter

func cQueries() *QuerySet {
	return &QuerySet{
		// THE STRUCT ROW MATCHES A DEFINITION, NOT A MENTION, and the `body:`
		// requirement is the whole of that. Without it `struct http_ops ops =
		// {...}` chunks its own TYPE REFERENCE as a second declaration named
		// http_ops, so the struct is ambiguous in its own file and every lookup
		// that has to name it declines rather than picking one — which is what
		// left the slot-bind owner lookup with two candidates and no edge. A
		// forward declaration `struct foo;` is dropped by the same rule, and
		// that is correct: it declares nothing a reference could resolve to,
		// and the definition it points at is captured wherever it lives.
		//
		// A C VARIABLE'S NAME COMES FROM resolveDeclNameC, NOT FROM A CAPTURE.
		// The `(declaration) @decl` row below deliberately binds no @name: a
		// pattern that named a field would filter on it, and C's declarator
		// shapes are open-ended, so an arm per shape would silently delete
		// every shape no arm covers.
		TopLevel: `[
			(function_definition
				declarator: (function_declarator
					declarator: (identifier) @name)) @decl
			(struct_specifier name: (type_identifier) @name body: (field_declaration_list)) @decl
			(enum_specifier name: (type_identifier) @name) @decl
			(type_definition) @decl
			(declaration) @decl
			(field_declaration declarator: (function_declarator declarator: (parenthesized_declarator (pointer_declarator declarator: (field_identifier) @name)))) @decl
		]`,
		// The identifier arm is FIRST and unchanged, so today's bare-call
		// capture is byte-identical. The field_expression arm spans both
		// dispatch forms — `h->flush(c)` and `ops.flush(c)` — because the
		// wrapper's own text IS the qualified callee.
		//
		// The second pattern binds the INNER field_expression rather than the
		// parenthesized wrapper, and that is its whole point: capturing the
		// wrapper would hand the resolver `(*h->close)`, whose leading
		// punctuation the qualifier split would tear into a qualifier naming
		// nothing.
		//
		// DO NOT CARRY THE GO RULE ACROSS. goCalleeText declines a
		// parenthesized callee deliberately, because in Go `(*Wrapped)(nil)` is
		// a CONVERSION and unwrapping it would bind a type being converted to.
		// In C the same syntax is a legitimate deref-call.
		Calls: `[
			(call_expression function: [
				(identifier) @callee
				(field_expression) @callee
			])
			(call_expression function: (parenthesized_expression (pointer_expression (field_expression) @callee)))
		]`,
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
		TestBlocks: `([
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
		] (#match? @fn "^(TEST|TEST_F|TEST_P|TEST_CASE|SECTION|SCENARIO|GIVEN|WHEN|THEN|BOOST_AUTO_TEST_CASE|BOOST_FIXTURE_TEST_CASE|RUN_TEST|BENCHMARK|MOCK_METHOD|MOCK_CONST_METHOD|cmocka_unit_test|cmocka_unit_test_setup_teardown|TYPED_TEST|TYPED_TEST_P|INSTANTIATE_TEST_SUITE_P)$"))`,
	}
}
