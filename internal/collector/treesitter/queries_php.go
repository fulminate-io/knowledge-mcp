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
		// The FIRST arm is unchanged and was already correct — `qualified_name`
		// spans `\Foo\Bar` whole, so plain function calls never lost their
		// qualifier. The other two gain the object/scope capture and compose a
		// span across their two @callee captures: `$o->doThing`, `Bar::stat`.
		//
		// The wildcard on scope is required for `\Other\Thing::go()`, whose
		// scope is a qualified_name node rather than a plain name.
		Calls: `[
			(function_call_expression function: [(name) (qualified_name)] @callee)
			(member_call_expression object: (_) @callee name: (name) @callee)
			(scoped_call_expression scope: (_) @callee name: (name) @callee)
		]`,
		// The namespace_aliasing_clause child is what carries `use Foo\Bar as
		// Qux`'s local name. One capture per statement.
		Imports:  `(namespace_use_declaration (namespace_use_clause (namespace_aliasing_clause)?)) @import`,
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
