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
		// deliberately does NOT belong in classLikeByLang's C# row: adding it would
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
		// The member_access_expression is captured WHOLE rather than reaching
		// past it to its name field: the wrapper node's own text IS the
		// qualified callee (`obj.DoThing`, `a.b.C`), and capturing the name
		// alone discarded the qualifier.
		Calls: `(invocation_expression function: [
			(identifier) @callee
			(member_access_expression) @callee
		])`,
		Imports: `(using_directive) @import`,
		// TypeRefs is ANCHORED TO TYPE POSITIONS. A bare `(identifier) @typeref`
		// captured every identifier in the file — the class name, the method
		// name, the namespace, every variable declarator, both halves of a
		// using's qualified_name, and every receiver in every call — and made
		// all of them USES_TYPE targets. C# has no `type_identifier` node kind,
		// so unlike java/rust/cpp/scala/swift there is no single kind to
		// capture and the positions must be enumerated from the grammar.
		//
		// `predefined_type` is deliberately absent from every arm, so `void`,
		// `int` and `string` are excluded STRUCTURALLY rather than by the Go
		// builtin name list the extractor carries.
		//
		// type_argument_list is captured on its own rather than left to the
		// generic_name arms: a nested `Dictionary<string, List<Foo>>` puts
		// `List` inside an INNER type_argument_list that the outer arm's
		// (identifier) child pattern does not reach.
		TypeRefs: `[
			(variable_declaration type: [(identifier) @typeref (generic_name (identifier) @typeref)])
			(parameter type: [(identifier) @typeref (generic_name (identifier) @typeref)])
			(method_declaration returns: [(identifier) @typeref (generic_name (identifier) @typeref)])
			(base_list [(identifier) @typeref (generic_name (identifier) @typeref)])
			(object_creation_expression type: [(identifier) @typeref (generic_name (identifier) @typeref)])
			(type_argument_list [(identifier) @typeref (generic_name (identifier) @typeref)])
		]`,
	}
}
