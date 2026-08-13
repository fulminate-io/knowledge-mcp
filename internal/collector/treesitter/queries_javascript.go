// SPDX-License-Identifier: Apache-2.0

package treesitter

func jsQueries() *QuerySet {
	return &QuerySet{
		TopLevel: `[
			(function_declaration name: (identifier) @name) @decl
			(class_declaration name: (identifier) @name) @decl
			(class_body (method_definition name: (property_identifier) @name) @decl)
			(export_statement declaration: [
				(function_declaration name: (identifier) @name)
				(class_declaration name: (identifier) @name)
			]) @decl
			(lexical_declaration) @decl
			(variable_declaration) @decl
		]`,
		Calls: `(call_expression function: [
			(identifier) @callee
			(member_expression property: (property_identifier) @callee)
		])`,
		Imports: `(import_statement source: (string) @path)`,
		// TypeRefs is empty BY DESIGN, not by omission. The plain JavaScript
		// grammar emits no type nodes at all: JSDoc arrives as one opaque
		// comment token, and the only structural type references in the tree are
		// new_expression and class_heritage. Capturing those two would not
		// restore parity with TypeScript either — the TypeScript query captures
		// neither `extends` nor `new`, so adding them here would BREAK the
		// parity it appears to restore. This zero is permanent at this grammar.
		TypeRefs: "",
		// TestBlocks: three-pattern union covering jest, vitest, mocha, jasmine,
		// AVA, tape, node:test, bun:test, Playwright, Cypress.
		//   Pattern A — bare identifier call:        it("foo", fn)
		//   Pattern B — chained-single member call:  it.skip("foo", fn) / test.describe(...)
		//   Pattern C — parameterized-double call:   it.each([rows])("name", fn)
		// Pattern C's @decl binds to the OUTER call_expression so the chunk
		// covers the form including the string-literal name and test body.
		// @fn binds to the INNER call's member_expression — classifyTestBlockJS
		// reads "it.each" / "test.each" / "describe.each" and the .each
		// suffix-strip normalizes to the bare base.
		// Regex alternants enumerated explicitly per round-5 fix to prevent
		// silent under-coverage when extending shapes.
		TestBlocks: jsTestBlocksQuery,
	}
}

// jsTestBlocksQuery is shared by JavaScript and TypeScript. The regex matches
// @fn's Content() verbatim — for Pattern A this is a bare identifier
// ("it"/"test"/"describe"/...), for Pattern B / Pattern C this is the
// member_expression text ("it.skip"/"it.each"/"test.describe"/...).
//
// Documented gap: nested describe→it does not currently bind @parent_name.
// A tree-sitter alternation pattern that ALSO matches the nested it call
// from inside a describe block would double-emit the inner chunk (one with
// parent_name, one without) because the inner call also matches the bare
// Pattern A. Tree-sitter S-expression has no clean dedup for this case.
// The chunk's ParentName == "" for nested it() calls; the indexer's
// astPathHash still uniquely identifies each chunk by AST position.
const jsTestBlocksQuery = `([
  (call_expression
    function: (identifier) @fn
    arguments: (arguments
      (string) @name
      [(arrow_function parameters: (formal_parameters) @params)
       (function_expression parameters: (formal_parameters) @params)
       (arrow_function)
       (function_expression)]
    )
  ) @decl
  (call_expression
    function: (identifier) @fn
    arguments: (arguments
      [(arrow_function parameters: (formal_parameters) @params)
       (function_expression parameters: (formal_parameters) @params)
       (arrow_function)
       (function_expression)]
    )
  ) @decl
  (call_expression
    function: (member_expression) @fn
    arguments: (arguments
      (string) @name
      [(arrow_function parameters: (formal_parameters) @params)
       (function_expression parameters: (formal_parameters) @params)
       (arrow_function)
       (function_expression)]
    )
  ) @decl
  (call_expression
    function: (member_expression) @fn
    arguments: (arguments
      [(arrow_function parameters: (formal_parameters) @params)
       (function_expression parameters: (formal_parameters) @params)
       (arrow_function)
       (function_expression)]
    )
  ) @decl
  (call_expression
    function: (call_expression
      function: (member_expression) @fn
      arguments: (arguments)
    )
    arguments: (arguments
      (string) @name
      [(arrow_function parameters: (formal_parameters) @params)
       (function_expression parameters: (formal_parameters) @params)
       (arrow_function)
       (function_expression)]
    )
  ) @decl
] (#match? @fn "^(it|test|specify|fit|xit|xtest|describe|context|fdescribe|xdescribe|suite|beforeAll|beforeEach|before|afterAll|afterEach|after|bench|it\\.skip|it\\.only|it\\.each|test\\.skip|test\\.only|test\\.each|describe\\.skip|describe\\.only|describe\\.each|test\\.describe|test\\.beforeEach|test\\.beforeAll|test\\.afterEach|test\\.afterAll)$"))`
