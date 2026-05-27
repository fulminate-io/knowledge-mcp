// SPDX-License-Identifier: Apache-2.0

package treesitter

func dockerfileQueries() *QuerySet {
	return &QuerySet{
		TopLevel: `[
			(from_instruction) @decl
			(run_instruction) @decl
			(copy_instruction) @decl
			(env_instruction) @decl
		]`,
		Calls:    "",
		Imports:  "",
		TypeRefs: "",
	}
}
