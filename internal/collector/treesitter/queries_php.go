// SPDX-License-Identifier: Apache-2.0

package treesitter

func phpQueries() *QuerySet {
	return &QuerySet{
		TopLevel: `[
			(function_definition name: (name) @name) @decl
			(class_declaration name: (name) @name) @decl
			(interface_declaration name: (name) @name) @decl
			(method_declaration name: (name) @name) @decl
			(trait_declaration name: (name) @name) @decl
			(namespace_definition) @decl
		]`,
		Calls: `[
			(function_call_expression function: [(name) (qualified_name)] @callee)
			(member_call_expression name: (name) @callee)
			(scoped_call_expression name: (name) @callee)
		]`,
		Imports:  `(namespace_use_declaration) @import`,
		TypeRefs: `(named_type (name) @typeref)`,
		// TestBlocks: Pest's `test('name', fn () => ...)`,
		// `it('name', fn () => ...)`, `describe('group', function () {...})`,
		// `beforeEach`, `afterAll`, `dataset`. PHP's tree-sitter grammar
		// uses `function_call_expression` (not `call_expression`) and the
		// callee leaf is a `name` node.
		//
		// Documented gap: nested @parent_name binding not captured (mirrors
		// JS/Kotlin/Scala/Elixir gap). The inner test/it inside describe()
		// emits with ParentName="" — astPathHash uniquely identifies each.
		TestBlocks: `([
			(function_call_expression
				function: (name) @fn
				arguments: (arguments
					(argument (string) @name)
				)
			) @decl
			(function_call_expression
				function: (name) @fn
				arguments: (arguments
					(argument [(arrow_function) (anonymous_function_creation_expression)])
				)
			) @decl
		] (#match? @fn "^(test|it|describe|context|beforeEach|beforeAll|afterEach|afterAll|setUp|tearDown|setUpBeforeClass|tearDownAfterClass|dataset)$"))`,
	}
}
