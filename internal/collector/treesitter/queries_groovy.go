// SPDX-License-Identifier: Apache-2.0

package treesitter

func groovyQueries() *QuerySet {
	return &QuerySet{
		// AN INTERFACE'S MEMBERS ARE function_declaration, A DIFFERENT KIND FROM
		// function_definition, so a query set carrying only the definition arm
		// leaves every contract member out of the graph entirely. Without those
		// nodes a call through an interface-typed value has no member
		// declaration to target, and a member-level conformance edge has no
		// supertype member to start from. Neither arm binds a @name, so both are
		// named by the per-language declaration-name resolver.
		TopLevel: `[
			(class_definition) @decl
			(function_definition) @decl
			(function_declaration) @decl
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
