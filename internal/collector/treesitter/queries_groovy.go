// SPDX-License-Identifier: Apache-2.0

package treesitter

func groovyQueries() *QuerySet {
	return &QuerySet{
		TopLevel: `[
			(class_definition) @decl
			(function_definition) @decl
		]`,
		// Ordinary Groovy calls parse as `function_call` with a `function:`
		// field, which the previous juxt_function_call-only capture matched not
		// at all — the language emitted no callee for `obj.doThing(1)` or
		// `plain(3)`. The juxt arm is KEPT rather than dropped because Gradle
		// build scripts and Jenkinsfiles route here and their whole content is
		// command-form calls; giving it a `function:` field turns it from
		// whole-expression noise into a real callee.
		Calls: `[
			(function_call function: [ (identifier) @callee (dotted_identifier) @callee ])
			(juxt_function_call function: [ (identifier) @callee (dotted_identifier) @callee ])
		]`,
		Imports:  `(groovy_import) @import`,
		TypeRefs: "",
	}
}
