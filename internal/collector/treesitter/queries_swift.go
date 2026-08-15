// SPDX-License-Identifier: Apache-2.0

package treesitter

func swiftQueries() *QuerySet {
	return &QuerySet{
		TopLevel: `[
			(function_declaration (simple_identifier) @name) @decl
			(class_declaration name: (type_identifier) @name) @decl
			(protocol_declaration name: (type_identifier) @name) @decl
		]`,
		// The navigation_expression is captured WHOLE rather than reaching past
		// it to the suffix's identifier: the wrapper's own text IS the
		// qualified callee (`obj.doThing`, `a.b.c`), and capturing the suffix
		// alone discarded the qualifier.
		Calls: `(call_expression [
			(simple_identifier) @callee
			(navigation_expression) @callee
		])`,
		Imports:  `(import_declaration) @import`,
		TypeRefs: `(type_identifier) @typeref`,
		// TestBlocks: XCTest's measure { ... } and measureMetrics { ... }
		// performance benchmark blocks. Per locked Q3, these are LEAF
		// test_block chunks classified as TestKindBenchmark — Bucket A
		// keeps classifying the containing function_declaration normally.
		// Tree-sitter Swift parses `measure { body }` as
		// `(call_expression (simple_identifier "measure") (call_suffix (lambda_literal)))`.
		TestBlocks: `((call_expression
			(simple_identifier) @fn
			(call_suffix (lambda_literal) @params)
		) @decl
		(#match? @fn "^(measure|measureMetrics)$"))`,
	}
}
