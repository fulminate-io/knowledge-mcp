// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
)

// BenchmarkGoQualifierWalk measures the two Go arms ALONE, with parsing lifted
// out of the measured loop.
//
// WHY IT IS NEEDED. BenchmarkChunkFile measures the whole chunker, where
// parsing dominates: the arms moved ns/op by only 1.04-1.05x while moving
// allocs/op by 1.79x, which is precisely why a time-only budget could not see
// the regression. Comparing candidate shapes through that instrument means
// comparing numbers whose differences sit inside the parsing noise. This
// benchmark parses ONCE outside the loop and then calls only the arms, so a
// candidate's delta is signal rather than rounding.
//
// THE INPUT IS THE SAME FILE BenchmarkChunkFile USES — a different one would
// make the two instruments incomparable. A missing input is b.Fatalf and never
// b.Skip: the sibling benchmark's own header records that reading a nonexistent
// path skipped silently and left its perf guard permanently vacuous.
func BenchmarkGoQualifierWalk(b *testing.B) {
	root := repoRoot(b)
	path := filepath.Join(root, "internal", "collector", "treesitter", "chunker_identity.go")
	src, err := os.ReadFile(path)
	if err != nil {
		b.Fatalf("benchmark input not found: %s: %v", path, err)
	}

	p := NewParser()
	defer p.Close()
	tree, parseErr := p.Parse(context.Background(), src, LangGo)
	if parseErr != nil {
		b.Fatalf("parsing the benchmark input %s: %v", path, parseErr)
	}
	defer tree.Close()

	// Collected ONCE, before the measured loop: the slice, the node handles and
	// the kind strings are all setup, and only the arms may allocate inside.
	rootNode := tree.RootNode()
	var decls []*sitter.Node
	var kinds []string
	for i := range int(rootNode.NamedChildCount()) {
		child := rootNode.NamedChild(i)
		switch child.Type() {
		case "function_declaration", "method_declaration", "type_declaration":
			decls = append(decls, child)
			kinds = append(kinds, child.Type())
		}
	}

	// KNOWN-POSITIVE CONTROL. Without it a benchmark over an empty declaration
	// slice reports a beautiful zero and every candidate ties at it — the
	// measurement would be indistinguishable from a perfect result.
	if len(decls) == 0 {
		b.Fatalf("control: %s yielded no function, method or type declarations, so this benchmark would measure nothing", path)
	}
	bound := false
	for _, decl := range decls {
		if goQualifierTypes(decl, src) != nil {
			bound = true
			break
		}
	}
	if !bound {
		b.Fatalf("control: no declaration in %s produced a qualifier-type map, so the arm under measurement is doing nothing", path)
	}

	b.ReportAllocs()
	for b.Loop() {
		for i, decl := range decls {
			_ = goQualifierTypes(decl, src)
			_ = goTypeFacts(decl, kinds[i], src)
		}
	}
}
