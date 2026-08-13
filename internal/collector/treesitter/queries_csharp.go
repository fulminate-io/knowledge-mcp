// SPDX-License-Identifier: Apache-2.0

package treesitter

func csharpQueries() *QuerySet {
	return &QuerySet{
		// file_scoped_namespace_declaration is C# 10+'s file-scoped form and the
		// default in .NET 6 templates. It is a DISTINCT node kind from the
		// block-form namespace_declaration beside it, so without its own
		// pattern the namespace of a modern C# file is not chunked at all. It
		// carries the same name shapes, hence the same alternation.
		//
		// It is a SIBLING of the types below it, never their ancestor, so it
		// deliberately does NOT belong in classLikeTypes: adding it there would
		// not parent those types, because no upward walk from a class reaches a
		// node beside it. Their namespace reaches them through the file's
		// symbol namespace instead.
		//
		// Note for anyone extending this string: tree-sitter query syntax has
		// no // comments, and a query that fails to compile is logged and then
		// silently produces NO CHUNKS for the language.
		TopLevel: `[
			(class_declaration name: (identifier) @name) @decl
			(interface_declaration name: (identifier) @name) @decl
			(struct_declaration name: (identifier) @name) @decl
			(namespace_declaration name: [(identifier) (qualified_name)] @name) @decl
			(file_scoped_namespace_declaration name: [(identifier) (qualified_name)] @name) @decl
			(method_declaration name: (identifier) @name) @decl
			(enum_declaration name: (identifier) @name) @decl
		]`,
		Calls: `(invocation_expression function: [
			(identifier) @callee
			(member_access_expression name: (identifier) @callee)
		])`,
		Imports:  `(using_directive) @import`,
		TypeRefs: `(identifier) @typeref`,
	}
}
