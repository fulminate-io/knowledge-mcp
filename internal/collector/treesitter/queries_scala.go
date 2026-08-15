// SPDX-License-Identifier: Apache-2.0

package treesitter

func scalaQueries() *QuerySet {
	return &QuerySet{
		TopLevel: `[
			(function_definition name: (identifier) @name) @decl
			(class_definition name: (identifier) @name) @decl
			(trait_definition name: (identifier) @name) @decl
			(object_definition name: (identifier) @name) @decl
			(val_definition pattern: (identifier) @name) @decl
		]`,
		// The field_expression is captured WHOLE rather than reaching past it to
		// its field: the wrapper's own text IS the qualified callee
		// (`obj.doThing`, `a.b.c`), and capturing the field alone discarded the
		// qualifier.
		Calls: `(call_expression function: [
			(identifier) @callee
			(field_expression) @callee
		])`,
		// The arrow_renamed_identifier inside namespace_selectors is what
		// carries `import a.{D => F}`'s local name. One capture per statement.
		Imports:  `(import_declaration (namespace_selectors (arrow_renamed_identifier)?)?) @import`,
		TypeRefs: `(type_identifier) @typeref`,
		// TestBlocks: ScalaTest, MUnit, Specs2.
		//
		// Two call shapes (T3-E):
		//
		// (1) Direct call form (FunSuite/MUnit/DescribeSpec):
		//     `test("name") { ... }` parses as a nested call_expression where
		//     the outer's function field is itself a call_expression — the
		//     trailing-block argument is a block that's the second @arguments.
		//     Inner call has function=identifier and arguments=(arguments (string)).
		//
		// (2) Infix form (FlatSpec/FreeSpec/WordSpec):
		//     `"x" should "y" in { ... }` parses as nested infix_expression
		//     with operators `should`/`must`/`in`/`-`. Verified cleanly in
		//     tree-sitter Scala via empirical probe (outcome B.1 from plan
		//     T3-E decision tree). Both direct and infix shipped in this phase.
		//
		// Documented gap: nested @parent_name binding not bound in either
		// shape (mirrors JS/Kotlin gap). Tree-sitter has no clean
		// exclude-inner pattern; chunk.ParentName == "" for nested it()
		// inside describe(). astPathHash uniquely identifies each chunk.
		// Direct call form ships ONLY the outer-call (trailing-block) shape so
		// `test("name") { ... }` produces exactly one chunk. Calls without a
		// trailing block (rare in test-DSL idioms) are intentionally not
		// captured — they don't match the test_block abstraction (no body).
		// For setup/teardown hooks `beforeAll { ... }` (no string arg, just
		// trailing block), the no-string variant is included.
		TestBlocks: `([
			(call_expression
				function: (call_expression
					function: (identifier) @fn
					arguments: (arguments (string) @name)
				)
				arguments: (block) @params
			) @decl
			(call_expression
				function: (identifier) @fn
				arguments: (block) @params
			) @decl
			(infix_expression
				left: (infix_expression
					left: (string) @parent_name
					right: (string) @name
				)
				operator: (identifier) @fn
				right: (block) @params
			) @decl
			(infix_expression
				left: (string) @name
				operator: (identifier) @fn
				right: (block) @params
			) @decl
		] (#match? @fn "^(test|it|in|should|must|describe|context|feature|scenario|beforeAll|beforeEach|before|afterAll|afterEach|after)$"))`,
	}
}
