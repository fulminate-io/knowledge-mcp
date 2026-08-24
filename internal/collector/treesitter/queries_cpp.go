// SPDX-License-Identifier: Apache-2.0

package treesitter

func cppQueries() *QuerySet {
	return &QuerySet{
		TopLevel: `[
			(function_definition
				declarator: (function_declarator
					declarator: [(identifier) (field_identifier)] @name)) @decl
			(class_specifier name: (type_identifier) @name) @decl
			(struct_specifier name: (type_identifier) @name) @decl
			(namespace_definition name: (namespace_identifier) @name) @decl
			(template_declaration) @decl
			(enum_specifier name: (type_identifier) @name) @decl
			(translation_unit (declaration declarator: (init_declarator declarator: (identifier) @name)) @decl)
			(declaration_list (declaration declarator: (init_declarator declarator: (identifier) @name)) @decl)
			(field_declaration_list (field_declaration declarator: (field_identifier) @name) @decl)
			(field_declaration declarator: (function_declarator declarator: (field_identifier) @name)) @decl
			(function_definition declarator: (function_declarator declarator: (qualified_identifier) @name)) @decl
		]`,
		// The qualified_identifier arm is what makes `ns::g(3)` emit a callee at
		// all. The field_expression node spans the arrow form too, so `ptr->m2`
		// needs no separate arm; capturing it whole is what keeps the receiver
		// in the callee text.
		Calls: `(call_expression function: [
			(identifier) @callee
			(field_expression) @callee
			(qualified_identifier) @callee
		])`,
		Imports: `(preproc_include path: [
			(string_literal) @path
			(system_lib_string) @path
		])`,
		// TypeRefs is ANCHORED TO TYPE POSITIONS. An unanchored
		// `(qualified_identifier) @typeref` arm also fires in CALL position and
		// would emit a USES_TYPE edge to the FUNCTION `ns::g` — a type-reference
		// query must not claim a call target as a type. The Calls query's own
		// qualified_identifier arm sits under `call_expression function:` and
		// these sit under declaration/field/parameter `type:`, which is what
		// keeps the two apart.
		TypeRefs: `[
			(declaration type: [(type_identifier) @typeref (qualified_identifier) @typeref])
			(field_declaration type: [(type_identifier) @typeref (qualified_identifier) @typeref])
			(parameter_declaration type: [(type_identifier) @typeref (qualified_identifier) @typeref])
		]`,
		// TestBlocks: gtest TEST/TEST_F/TEST_P macros, Catch2 TEST_CASE,
		// Boost.Test, Google-Benchmark BENCHMARK, MOCK_METHOD declarations.
		//
		// IMPORTANT C++ grammar quirk (verified empirically): top-level
		// `TEST(Suite, Name) { ... }` parses as a (function_definition)
		// because tree-sitter C++ interprets identifier-prefixed-by-identifier
		// as a return-type declaration. The function_declarator wraps the
		// macro name and the parameter_list contains parameter_declarations
		// whose `type` field carries the suite/test names. When the macro's
		// arguments include string_literals (Catch2 TEST_CASE("name", "[tag]"))
		// the grammar correctly emits call_expression instead.
		//
		// Two patterns:
		//   (1) function_definition shape — gtest TEST/TEST_F/TEST_P with
		//       identifier-only args.
		//   (2) call_expression shape — Catch2 TEST_CASE / BOOST_AUTO_TEST_CASE /
		//       BENCHMARK / MOCK_METHOD with string-literal or identifier args.
		TestBlocks: `([
			(function_definition
				declarator: (function_declarator
					declarator: (identifier) @fn
					parameters: (parameter_list
						. (parameter_declaration type: (type_identifier) @suite)
						. (parameter_declaration type: (type_identifier) @name)
					)
				)
				body: (compound_statement)
			) @decl
			(function_definition
				declarator: (function_declarator
					declarator: (identifier) @fn
					parameters: (parameter_list
						. (parameter_declaration type: (type_identifier) @name) .
					)
				)
				body: (compound_statement)
			) @decl
			(call_expression
				function: (identifier) @fn
				arguments: (argument_list . (string_literal) @name)
			) @decl
			(call_expression
				function: (identifier) @fn
				arguments: (argument_list . (identifier) @suite . (identifier) @name)
			) @decl
			(call_expression
				function: (identifier) @fn
				arguments: (argument_list . (identifier) @name .)
			) @decl
		] (#match? @fn "^(TEST|TEST_F|TEST_P|TEST_CASE|SECTION|SCENARIO|GIVEN|WHEN|THEN|BOOST_AUTO_TEST_CASE|BOOST_FIXTURE_TEST_CASE|RUN_TEST|BENCHMARK|MOCK_METHOD|MOCK_CONST_METHOD|cmocka_unit_test|cmocka_unit_test_setup_teardown|TYPED_TEST|TYPED_TEST_P|INSTANTIATE_TEST_SUITE_P)$"))`,
	}
}
