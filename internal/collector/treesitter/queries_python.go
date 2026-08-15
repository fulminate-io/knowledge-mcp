// SPDX-License-Identifier: Apache-2.0

package treesitter

func pythonQueries() *QuerySet {
	return &QuerySet{
		TopLevel: `[
			(function_definition name: (identifier) @name) @decl
			(class_definition name: (identifier) @name) @decl
			(decorated_definition) @decl
		]`,
		// The attribute node is captured WHOLE rather than reaching past it to
		// its attribute field: the wrapper's own text IS the qualified callee
		// (`obj.do_thing`, `a.b.c`), and capturing the trailing identifier
		// alone discarded the qualifier.
		Calls: `(call function: [
			(identifier) @callee
			(attribute) @callee
		])`,
		// ONE capture per statement. The previous form bound TWO captures to an
		// import_from_statement, which a registered importParsers arm — invoked
		// once per CAPTURE — would have run twice over the same statement. The
		// arm reproduces the second entry (the bare module path) itself, so
		// ctx.Imports keeps exactly the entries framework detection matched on.
		//
		// The aliased_import child is named so this query and the arm agree on
		// where `import json as j` and `from x import a as b` put their alias.
		Imports: `[
			(import_statement (aliased_import)?)
			(import_from_statement)
		] @import`,
		TypeRefs: `(type (identifier) @typeref)`,
	}
}
