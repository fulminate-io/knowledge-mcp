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
		Calls:    `(call method: (identifier) @callee)`,
		Imports:  "",
		TypeRefs: `(constant) @typeref`,
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
		TestBlocks: `[
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
		] (#match? @fn "^(it|test|specify|example|focus|fit|fcontext|fdescribe|xit|xtest|xspecify|skip|pending|describe|context|before|after|setup|teardown|let|let!|subject|instance_double|class_double|double|spy|allow|expect)$")`,
	}
}
