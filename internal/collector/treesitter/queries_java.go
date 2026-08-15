// SPDX-License-Identifier: Apache-2.0

package treesitter

func javaQueries() *QuerySet {
	return &QuerySet{
		TopLevel: `[
			(class_declaration name: (identifier) @name) @decl
			(interface_declaration name: (identifier) @name) @decl
			(method_declaration name: (identifier) @name) @decl
			(constructor_declaration name: (identifier) @name) @decl
			(enum_declaration name: (identifier) @name) @decl
		]`,
		// TWO CAPTURES NAMED @callee IN ONE MATCH. Java's grammar flattens a
		// qualified callee into sibling children of the call node, so no single
		// node's text is `obj.doThing` and no query can select one; the
		// extractor composes the source span across both captures instead.
		//
		// `object: (_)` is the WILDCARD deliberately: the object field holds a
		// field_access node for `this.x.y`, and an identifier-only arm would
		// push every chained call into the bare arm — where the !object
		// negation rejects it — producing a silent hole rather than a visible
		// failure. The bare arm is MANDATORY: without it unqualified calls
		// vanish entirely, and without its negation it double-matches every
		// qualified call.
		Calls: `[
			(method_invocation object: (_) @callee name: (identifier) @callee)
			(method_invocation !object name: (identifier) @callee)
		]`,
		// ONE capture per statement, because a registered importParsers arm is
		// invoked once per CAPTURE and a second capture would record the same
		// import twice. The `asterisk` child is named so the arm's own reader
		// and this query agree about which shape carries no bound name:
		// `import x.y.*` binds an unbounded set under no local name.
		Imports: `(import_declaration (scoped_identifier) (asterisk)?) @import`,
		// TypeRefs is ANCHORED TO TYPE POSITIONS, and the anchoring fixes a
		// PRE-EXISTING defect as well as preventing a new one. A bare
		// `(scoped_type_identifier) @typeref` beside the bare type_identifier
		// arm was measured to emit SEVEN typerefs for
		// `java.util.List<String> x; Bar y;` — `java.util.List`, `java.util`,
		// `java`, `util`, `List`, `String`, `Bar` — because
		// scoped_type_identifier is RECURSIVE and matches at every nesting
		// depth while the bare arm then captures each segment.
		//
		// THE generic_type ARM MUST ADMIT scoped_type_identifier: a generic
		// type's own name is a scoped_type_identifier rather than a
		// type_identifier, so a narrower arm emitted `String` but dropped
		// `java.util.List` entirely.
		TypeRefs: `[
			(field_declaration type: [(type_identifier) @typeref (scoped_type_identifier) @typeref (generic_type [(type_identifier) @typeref (scoped_type_identifier) @typeref])])
			(formal_parameter type: [(type_identifier) @typeref (scoped_type_identifier) @typeref])
			(local_variable_declaration type: [(type_identifier) @typeref (scoped_type_identifier) @typeref])
			(method_declaration type: [(type_identifier) @typeref (scoped_type_identifier) @typeref])
			(type_arguments [(type_identifier) @typeref (scoped_type_identifier) @typeref])
			(superclass (type_identifier) @typeref)
			(super_interfaces (type_list (type_identifier) @typeref))
			(object_creation_expression type: [(type_identifier) @typeref (generic_type (type_identifier) @typeref)])
		]`,
	}
}
