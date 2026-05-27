// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// isHCLTftestFile recognizes the Terraform 1.6+ test-file extension
// `.tftest.hcl`. Centralizing the suffix check (rather than inlining
// strings.HasSuffix in two places) keeps classifyTestKindHCL and
// extendFrameworksHCL in lockstep.
func isHCLTftestFile(path string) bool {
	return strings.HasSuffix(path, ".tftest.hcl")
}

// classifyTestKindHCL: Terraform test files use the `.tftest.hcl` extension
// (introduced in Terraform 1.6). Every block in such a file (`run`,
// `variables`, `provider`, etc.) classifies as TestKindTest. Non-test HCL
// (`.tf`, `.tfvars`) classifies as none — there's no annotation system.
func classifyTestKindHCL(
	_ *sitter.Node,
	_ []byte,
	_, _ string,
	_ ChunkContext,
	filePath string,
) (bool, TestKind) {
	if isHCLTftestFile(filePath) {
		return true, TestKindTest
	}
	return false, TestKindNone
}

// extendFrameworksHCL appends FrameworkHCLTfTest when the file name ends in
// `.tftest.hcl`. HCL has no Imports query so DetectFrameworks cannot produce
// this signal; the extender is the only path. Preserves the input slice
// (locked extender contract — extenders MUST only append).
func extendFrameworksHCL(
	_ *sitter.Node,
	_ []byte,
	filePath string,
	detected []Framework,
) []Framework {
	if isHCLTftestFile(filePath) {
		return append(detected, FrameworkHCLTfTest)
	}
	return detected
}
