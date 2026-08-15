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
		// Calls: the member arm captures the WHOLE member_expression, byte
		// identical to queries_typescript.go. Capturing the property identifier
		// alone dropped the qualifier before resolution ever saw it, so
		// `obj.foo()` arrived as the bare "foo" and could reach none of the
		// qualified rungs — it bound to any same-named declaration in its own
		// file instead.
		//
		// The new_expression arm is here because constructor-only usage was
		// otherwise invisible to resolution: a class referenced only as
		// `new Foo()` or `new nsAlias.Member()` produced no edge of any kind, so
		// nothing reached the resolution ladder. A constructor is emitted as
		// CALLS — not USES_TYPE — because `new Foo()` transfers control into
		// Foo's constructor, which is what makes the call-site Weight above true
		// rather than an approximation. The `constructor:` field names the
		// constructor position directly, so the arm cannot widen to argument
		// text if a future grammar pin flattens that subtree.
		Calls: `[
		(call_expression function: [
			(identifier) @callee
			(member_expression) @callee
		])
		(new_expression constructor: [(identifier) @callee (member_expression) @callee])
		]`,
		Imports: jsImportsQuery,
		// TypeRefs is empty BY DESIGN, not by omission. The plain JavaScript
		// grammar emits no type nodes at all: JSDoc arrives as one opaque
		// comment token, and the only structural type references in the tree are
		// new_expression and class_heritage. Capturing those two would not
		// restore parity with TypeScript either — the TypeScript TypeRefs query
		// captures neither `extends` nor `new`, so adding them here would BREAK
		// the parity it appears to restore. This zero is permanent at this
		// grammar. (The Calls query above captures new_expression in BOTH
		// languages; that is a call edge, not a type reference, and it leaves
		// this TypeRefs parity argument untouched.)
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

// jsImportsQuery is shared by JavaScript and TypeScript, in the same idiom as
// jsTestBlocksQuery below.
//
// IT CAPTURES WHOLE STATEMENTS AND THE CLAUSE IS WALKED IN GO. That shape is
// load-bearing rather than stylistic: a query that captured a specifier
// alongside clause sub-patterns would emit ONE MATCH PER CLAUSE FORM, each
// carrying its own specifier, so a single import statement would append its
// path several times and emit duplicate IMPORTS edges. Capturing the statement
// once and reading its fields in Go makes that impossible and puts the
// structure walk where it is testable. It is also the idiom the package already
// uses for a field-shaped read — declaredFileNamespace resolves PHP and C#
// namespaces by ChildByFieldName on a captured node for the same reason, since
// a query that filters on a field DELETES the forms that lack it.
//
// The export half is what makes a re-export a visible dependency:
// `export {X} from './y'` and `export * from './w'` make this file depend on
// that module exactly as an import does. The arm appends a ctx.Imports entry
// only when the export statement HAS a source.
const jsImportsQuery = `[
  (import_statement) @import
  (export_statement) @export
]`

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
