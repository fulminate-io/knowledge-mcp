// SPDX-License-Identifier: Apache-2.0

package treesitter

func kotlinQueries() *QuerySet {
	return &QuerySet{
		TopLevel: `[
			(function_declaration (simple_identifier) @name) @decl
			(class_declaration (type_identifier) @name) @decl
			(object_declaration (type_identifier) @name) @decl
		]`,
		Calls:    `(call_expression) @callee`,
		Imports:  `(import_header (identifier) @path)`,
		TypeRefs: `(user_type (type_identifier) @typeref)`,
		// TestBlocks: Kotest (FunSpec/DescribeSpec/BehaviorSpec/StringSpec)
		// and Spek. Kotlin grammar parses `test("name") { ... }` as nested
		// call_expressions: outer call wraps a call_suffix containing the
		// trailing lambda; inner call has the value_arguments. Both shapes
		// are captured — the outer (with trailing lambda) is preferred when
		// available because the chunk content range covers the whole call.
		//
		// Documented gap: nested @parent_name binding not captured (mirrors
		// JS/TS gap). Tree-sitter has no clean exclude-inner pattern; chunk
		// ParentName == "" for nested it() inside describe(). astPathHash
		// uniquely identifies each chunk by AST position.
		TestBlocks: `[
			(call_expression
				(call_expression
					(simple_identifier) @fn
					(call_suffix
						(value_arguments
							(value_argument (string_literal) @name)
						)
					)
				)
				(call_suffix (annotated_lambda) @params)
			) @decl
			(call_expression
				(simple_identifier) @fn
				(call_suffix
					(value_arguments
						(value_argument (string_literal) @name)
					)
				)
			) @decl
			(call_expression
				(simple_identifier) @fn
				(call_suffix (annotated_lambda) @params)
			) @decl
		] (#match? @fn "^(test|it|context|describe|by|given|when|then|Given|When|Then|should|expect|feature|scenario|FunSpec|DescribeSpec|BehaviorSpec|StringSpec|FreeSpec|WordSpec|beforeTest|beforeEach|beforeSpec|beforeEachTest|beforeAll|afterTest|afterEach|afterSpec|afterEachTest|afterAll)$")`,
	}
}
