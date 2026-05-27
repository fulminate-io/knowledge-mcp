// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// isBashTestFile recognizes ONLY the bats convention: filename suffix
// `.bats`. Per locked Q5 + T3-B: do NOT match `test_*.sh` / `test_*.bash`
// shapes — bats is the only Bash test framework in scope, and bats files
// always use the `.bats` extension. Generic shell-style heuristics produce
// false positives.
func isBashTestFile(filePath string) bool {
	return strings.HasSuffix(filePath, ".bats")
}

// classifyTestKindBash classifies declarations in `.bats` files. Per Q5
// degraded-path verification (tree-sitter-bash fragments `@test "name" { ... }`
// into three separate `command` nodes that don't compose into a clean
// test_block chunk), Bucket B's TestBlocks pass cannot extract per-test
// chunks for Bash. Instead, Bucket A applies a filename-only classification:
// any declaration in a `.bats` file becomes TestKindTest, with `setup`/
// `teardown`/`setup_file`/`teardown_file` function_definition names mapped
// to setup/teardown.
func classifyTestKindBash(
	_ *sitter.Node,
	_ []byte,
	_, name string,
	_ ChunkContext,
	filePath string,
) (bool, TestKind) {
	if !isBashTestFile(filePath) {
		return false, TestKindNone
	}
	switch name {
	case "setup", "setup_file":
		return true, TestKindSetup
	case "teardown", "teardown_file":
		return true, TestKindTeardown
	}
	return true, TestKindTest
}

func init() {
	testKindClassifiers[LangBash] = classifyTestKindBash
}
