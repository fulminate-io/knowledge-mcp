// SPDX-License-Identifier: Apache-2.0

package treesitter

func goQueries() *QuerySet {
	return &QuerySet{
		// THE FOURTH ARM IS ANCHORED, AND THE ANCHOR IS THE POINT. Nesting the
		// method_elem under (type_declaration (type_spec name: ...)) is what
		// EXCLUDES an anonymous interface's method specs. Measured over a fixture
		// holding three named interfaces plus one anonymous interface in a
		// parameter (`func f(x interface{ Anon() error }) {}`): the bare arm
		// `(method_elem name: (field_identifier) @name) @decl` returned 5 matches
		// INCLUDING Anon, and this anchored arm returned exactly the 4
		// named-interface specs. An anonymous interface's spec has no enclosing
		// type_spec name, so under the bare arm it would chunk with ParentName=""
		// and take node ID `<file>:Anon` — colliding with any function of that
		// name in the file.
		//
		// THE type_spec's NAME IS DELIBERATELY UNCAPTURED. Capturing it would bind
		// a SECOND @name in the same alternation member, and extractDeclAndName
		// takes the last @name it sees — so the chunk would be named after the
		// INTERFACE rather than after the method.
		//
		// THE ARM STAYS ON ONE LINE, AND NO `;` COMMENT GOES INSIDE THIS RAW
		// STRING. A gate greps this exact byte sequence and separately counts how
		// many times the node kind `method_elem` appears in this file after
		// stripping Go `//` lines; a tree-sitter `;` comment naming the kind
		// survives that strip and takes the count to 2. Reason-bearing prose
		// belongs here, above the field, exactly as it does for every other field
		// in this set.
		//
		// USES_TYPE GRAIN CHANGES, AND THAT IS CORRECT. The enclosing
		// type_declaration already emits type references for the whole interface
		// body; each method_elem now ALSO emits them from its own FromID. Those
		// are DIFFERENT edges, not duplicates — a finer-grained addition, so a
		// higher USES_TYPE count after this arm is the intended result.
		TopLevel: `[
			(function_declaration name: (identifier) @name) @decl
			(method_declaration
				receiver: (parameter_list) @receiver
				name: (field_identifier) @name) @decl
			(type_declaration (type_spec name: (type_identifier) @name)) @decl
			(type_declaration (type_spec name: (type_identifier) (interface_type (method_elem name: (field_identifier) @name) @decl)))
		]`,
		// THE SECOND ARM IS NOT A CONVERSION ARM — it is the generic-CALL arm.
		// The Go grammar parses an explicitly instantiated generic call
		// `newPresizedMap[string, int](100)` as a type_conversion_expression
		// wrapping a generic_type, NOT as a call_expression, so without this arm
		// every such site emits no CALLS edge at all.
		//
		// The `type:` field anchor plus the (generic_type ...) node-kind
		// constraint is what confines the arm to the ONE type_conversion_expression
		// shape whose head can be a function name. Every other conversion the
		// grammar produces has a slice_type, map_type, array_type, interface_type,
		// channel_type or pointer child, none of which this pattern names. The
		// inner alternation matches DIRECT children of generic_type only, so the
		// type_identifier nodes nested inside the sibling type_arguments sit two
		// levels down and are never captured — the callee is the head, never an
		// argument.
		//
		// IRREDUCIBLE FALSE POSITIVE, stated because it cannot be overcome at this
		// layer: tree-sitter has no type information, so a generic TYPE conversion
		// `Pair[int, int](p)` parses IDENTICALLY to a generic function call and
		// emits its head as a callee. The alternative — leaving all such sites
		// emitting nothing — is strictly worse.
		//
		// Both captures are named `callee` because span composition in
		// extractCallEdges considers only that name; TestCalleeCaptureNameCensus
		// enforces it across every registered language.
		Calls: `[
		(call_expression function: [
			(identifier) @callee
			(selector_expression) @callee
		])
		(type_conversion_expression type: (generic_type [
			(type_identifier) @callee
			(qualified_type) @callee
		]))
		]`,
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
