// SPDX-License-Identifier: Apache-2.0

package treesitter

func bashQueries() *QuerySet {
	return &QuerySet{
		TopLevel: `(function_definition name: (word) @name) @decl`,
		Calls:    `(command name: (command_name) @callee)`,
		Imports:  "",
		TypeRefs: "",
	}
}
