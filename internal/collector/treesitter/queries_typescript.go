// SPDX-License-Identifier: Apache-2.0

package treesitter

func tsQueries() *QuerySet {
	return &QuerySet{
		TopLevel: `[
			(function_declaration name: (identifier) @name) @decl
			(class_declaration name: (type_identifier) @name) @decl
			(interface_declaration name: (type_identifier) @name) @decl
			(type_alias_declaration name: (type_identifier) @name) @decl
			(class_body (method_definition name: (property_identifier) @name) @decl)
			(export_statement declaration: [
				(function_declaration name: (identifier) @name)
				(class_declaration name: (type_identifier) @name)
				(interface_declaration name: (type_identifier) @name)
				(type_alias_declaration name: (type_identifier) @name)
			]) @decl
			(lexical_declaration) @decl
		]`,
		Calls: `[
		(call_expression function: [
			(identifier) @callee
			(member_expression) @callee
		])
		(new_expression constructor: [(identifier) @callee (member_expression) @callee])
		]`,
		// Imports: shared with JavaScript. See queries_javascript.go for the
		// whole-statement capture shape and why the clause is walked in Go
		// rather than matched by sub-pattern. This file serves BOTH typescript
		// and tsx, so the one reference covers two of the three languages the
		// ECMAScript import arm registers for.
		Imports:  jsImportsQuery,
		TypeRefs: `(type_identifier) @typeref`,
		// TestBlocks: shared with JavaScript. See queries_javascript.go for
		// the three-pattern union shape and regex alternant enumeration.
		TestBlocks: jsTestBlocksQuery,
	}
}
