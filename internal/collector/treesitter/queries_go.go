// SPDX-License-Identifier: Apache-2.0

package treesitter

func goQueries() *QuerySet {
	return &QuerySet{
		TopLevel: `[
			(function_declaration name: (identifier) @name) @decl
			(method_declaration
				receiver: (parameter_list) @receiver
				name: (field_identifier) @name) @decl
			(type_declaration (type_spec name: (type_identifier) @name)) @decl
		]`,
		Calls: `(call_expression function: [
			(identifier) @callee
			(selector_expression) @callee
		])`,
		// THE WHOLE import_spec, ONE CAPTURE. A registered importParsers arm is
		// invoked ONCE PER CAPTURE rather than once per match
		// (chunker_imports.go), so a two-capture spec binding `name:` and
		// `path:` separately would invoke parseGoImport twice for every aliased
		// import. The arm reads both fields off the captured node instead.
		Imports: `(import_spec) @import`,
		// A QUALIFIED type keeps its package: `store.Node` is captured whole so
		// the resolution walk can split it at its last dot and bind the
		// reference through the file's imports, instead of seeing a bare `Node`
		// that can only match a same-package declaration. The alternation
		// captures BOTH kinds and the inner type_identifier of a qualified_type
		// survives with a different text, so extractTypeRefEdges keeps only the
		// OUTERMOST capture per type expression.
		TypeRefs: `[
			(qualified_type) @typeref
			(type_identifier) @typeref
		]`,
	}
}
