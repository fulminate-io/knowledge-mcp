// SPDX-License-Identifier: Apache-2.0

package treesitter

func rubyQueries() *QuerySet {
	return &QuerySet{
		TopLevel: `[
			(method name: (identifier) @name) @decl
			(class name: (constant) @name) @decl
			(module name: (constant) @name) @decl
			(singleton_method name: (identifier) @name) @decl
		]`,
		// TWO CAPTURES NAMED @callee IN ONE MATCH, composed into one callee by
		// the source-span rule — Ruby flattens the receiver and the method into
		// siblings, so no single node spans `obj.do_thing`.
		//
		// The receiver is the WILDCARD because it may be a `constant`
		// (`Helper.stat`) as readily as an identifier. `a.b.deep(4)` yields TWO
		// matches, `a.b` from the inner call node and `a.b.deep` from the
		// outer: Ruby's grammar models `a.b` as a call in its own right, so
		// both are genuine references rather than a duplicate.
		Calls: `[
			(call receiver: (_) @callee method: (identifier) @callee)
			(call !receiver method: (identifier) @callee)
		]`,
		// Ruby has NO static import form — `require` is a runtime call — so
		// there is nothing to capture and no BindsResolver arm to register.
		// Its residue is tracked as dynamic candidate sets.
		Imports: "",
		// TypeRefs is ANCHORED TO REFERENCE POSITIONS. Ruby has no type syntax,
		// so a bare `(constant) @typeref` matched every constant — measured on
		// a class holding `MAX = 1`, it emitted `MAX` TWICE, once for the
		// assignment and once for the use. Constants are not types, and a
		// USES_TYPE edge to a numeric constant is a wrong edge.
		//
		// THE scope_resolution ARM IS WHOLE-NODE DELIBERATELY: capturing only
		// its `scope:` field yields `Foo` from `Foo::Bar` and drops the
		// referenced name, which is the stripped-qualifier defect itself.
		TypeRefs: `[
			(superclass (constant) @typeref)
			(scope_resolution) @typeref
			(call receiver: (constant) @typeref)
		]`,
		// TestBlocks: RSpec describe/context/it/specify, Minitest block-form
		// `test "name" do ... end`, test-unit setup/teardown, and the
		// fixture/mock idioms (let/subject/instance_double).
		//
		// The query covers (1) calls with a string argument and a block (the
		// describe/context/it case), (2) calls with a block but no string
		// argument (let { ... } / subject { ... } / before/after hooks with
		// symbol arg or no arg), and (3) calls without a block (allow / expect
		// / instance_double mock factories).
		//
		// Documented gap: nested @parent_name binding is not captured here.
		// A nested pattern that ALSO captures the inner it call would
		// double-emit (the inner call also matches the bare pattern).
		// Ruby's call shape with `do_block` body containing nested calls
		// has no clean tree-sitter S-expression to exclude the inner call
		// from also matching at top level. Chunk.ParentName == "" for
		// nested it() inside describe() blocks; astPathHash uniquely
		// identifies each chunk.
		TestBlocks: `([
			(call
				method: (identifier) @fn
				arguments: (argument_list (string) @name)
				block: [(do_block) (block)]
			) @decl
			(call
				method: (identifier) @fn
				block: [(do_block) (block)]
			) @decl
			(call
				method: (identifier) @fn
				arguments: (argument_list)
			) @decl
		] (#match? @fn "^(it|test|specify|example|focus|fit|fcontext|fdescribe|xit|xtest|xspecify|skip|pending|describe|context|before|after|setup|teardown|let|let!|subject|instance_double|class_double|double|spy|allow|expect)$"))`,
	}
}
