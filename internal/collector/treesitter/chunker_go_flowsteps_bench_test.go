// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
)

// BenchmarkGoFlowWalk measures the Go flow arm AND the closure engine ALONE,
// with parsing lifted out of the measured loop.
//
// WHY IT IS NEEDED, and the reason is measured rather than asserted.
// BenchmarkChunkFile measures the whole chunker, where parsing dominates: the
// sibling qualifier arms moved ns/op by only 1.04-1.05x while moving allocs/op
// by 1.79x, which is precisely why a time-only budget could not see that
// regression. A FLOW ARM DOES STRICTLY MORE PER DECLARATION THAN A QUALIFIER
// ARM — five step kinds, a closure pass over them, and the facts that come out
// — so the same blindness applies with more to hide. This benchmark parses ONCE
// outside the loop and then calls only the arm and the engine, so a candidate's
// delta is signal rather than rounding.
//
// THE INPUT IS THE SAME FILE BenchmarkChunkFile AND BenchmarkGoQualifierWalk
// USE — a different one would make the three instruments incomparable. A missing
// input is b.Fatalf and NEVER b.Skip: the sibling benchmark's own header records
// that reading a nonexistent path skipped silently and left its perf guard
// permanently vacuous.
func BenchmarkGoFlowWalk(b *testing.B) {
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

	// Collected ONCE, before the measured loop: the slice and the node handles
	// are setup, and only the arm and the engine may allocate inside.
	rootNode := tree.RootNode()
	var decls []*sitter.Node
	for i := range int(rootNode.NamedChildCount()) {
		child := rootNode.NamedChild(i)
		switch child.Type() {
		case "function_declaration", "method_declaration", "type_declaration":
			decls = append(decls, child)
		}
	}

	// KNOWN-POSITIVE CONTROL, and it is a DISTINCT construct from the input guard
	// above rather than a second instance of it. Without this, a benchmark over
	// an empty declaration slice reports a beautiful zero, every candidate ties
	// at it, and the recorded baseline lands as a well-formed `0` — two gates
	// green over an instrument measuring nothing.
	if len(decls) == 0 {
		b.Fatalf("control: %s yielded no function, method or type declarations, so this benchmark would measure nothing", path)
	}
	produced := false
	for _, decl := range decls {
		if len(goFlowSteps(decl, src)) > 0 {
			produced = true
			break
		}
	}
	if !produced {
		b.Fatalf("control: no declaration in %s produced a flow step, so the arm under measurement is doing nothing", path)
	}

	b.ReportAllocs()
	for b.Loop() {
		for _, decl := range decls {
			_ = flowClosure(goFlowSteps(decl, src), src)
		}
	}
}
