// SPDX-License-Identifier: Apache-2.0

// no_walk_cap_test.go — regression pin for the walk-cap removal. The
// engine used to truncate the merged result slice at a per-walk cap and, on
// reaching it, short-circuit every file still queued — so a corpus carrying
// more matches than the old engine default of 100 reported BOTH a truncated
// match count and a truncated FilesScanned figure. This test walks a
// 150-match, 4-file corpus and asserts the walk covers all of it.
//
// The Scope literal below names no cap field on purpose: the file must
// compile against the engine before and after that field is deleted, so the
// pre-fix failure is an assertion failure (a real red) rather than a compile
// error.

package ast

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// manyCallFile builds a Go source file with n single-line call sites of
// oldName, plus the one declaration that makes the file self-consistent. The
// declaration is a function_declaration, never a call_expression, so the
// oldName($X) pattern does not count it.
func manyCallFile(pkg string, n int) string {
	var b strings.Builder
	b.WriteString("package " + pkg + "\n\n")
	b.WriteString("func oldName(x int) int { return x }\n\n")
	for i := range n {
		b.WriteString(fmt.Sprintf("func F%03d() int { return oldName(%d) }\n", i, i))
	}
	return b.String()
}

func TestMatch_WalkIsNeverCapped(t *testing.T) {
	dir := fixtureRepo(t, map[string]string{
		"a.go":         manyCallFile("a", 60),
		"b.go":         manyCallFile("b", 60),
		"c.go":         manyCallFile("c", 30),
		"untouched.go": "package untouched\n\nfunc Z() int { return 0 }\n",
	})

	pat, err := Parse("oldName($X)")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	cp, err := Compile(pat, treesitter.LangGo, "")
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	defer cp.Close()

	raws, walk, err := Match(context.Background(), dir, treesitter.LangGo, cp, nil, Scope{})
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if len(raws) != 150 {
		t.Errorf("matches = %d, want 150", len(raws))
	}
	if walk.FilesScanned != 4 {
		t.Errorf("FilesScanned = %d, want 4", walk.FilesScanned)
	}
}
