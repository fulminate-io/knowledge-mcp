// SPDX-License-Identifier: Apache-2.0

// long_tail_smoke_test.go — MUST-PASS smoke tests + shared walker helpers
// for the long-tail registered languages. The MUST-PASS tier (Java fn
// decl, Ruby method def, Bash function) is pinned by validation contract
// item 7; failures here block the phase.
//
// Best-effort smoke tests (the remaining 12 long-tail languages) live in
// long_tail_smoke_besteffort_test.go and skip with a finding pointer
// when wrapper iteration didn't converge.
//
// Engine constraint: walker's seq-shadow is depth=1 (Phase C finding
// 2224314716b17a0554f7b416c4ee6b72). Patterns requiring depth-2 descent
// for sequence captures are out of scope for this phase.

package ast

import (
	"context"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// runLongTailWalker compiles pattern under cfg, parses target under
// cfg.Lang, walks every named node, and returns the matches. Used for the
// MUST-PASS tier where compile failures are real bugs.
func runLongTailWalker(t *testing.T, cfg LangConfig, pattern, target string) []walkerMatch {
	t.Helper()
	pt, err := compilePattern(context.Background(), mustParse(t, pattern), cfg)
	if err != nil {
		t.Fatalf("compilePattern(lang=%q, %q): %v", cfg.Lang, pattern, err)
	}
	defer pt.Close()
	return walkLongTail(t, cfg, pt, target)
}

// runLongTailWalkerOrSkip compiles+walks like runLongTailWalker but skips
// the test (instead of failing) when compile fails. Used for best-effort
// tier where the validation contract authorizes skipping on
// non-convergence.
func runLongTailWalkerOrSkip(t *testing.T, cfg LangConfig, pattern, target, reason string) []walkerMatch {
	t.Helper()
	pt, err := compilePattern(context.Background(), mustParse(t, pattern), cfg)
	if err != nil {
		t.Skipf("%s smoke: compilePattern failed (%v); wrapper iteration did not converge — %s", cfg.Lang, err, reason)
	}
	defer pt.Close()
	return walkLongTail(t, cfg, pt, target)
}

// walkLongTail parses target under cfg.Lang and runs matchTree on every
// named node, returning successful matches.
func walkLongTail(t *testing.T, cfg LangConfig, pt *PatternTree, target string) []walkerMatch {
	t.Helper()
	parser := treesitter.NewParser()
	defer parser.Close()
	tree, err := parser.Parse(context.Background(), []byte(target), cfg.Lang)
	if err != nil {
		t.Fatalf("parse %q target: %v", cfg.Lang, err)
	}
	defer tree.Close()

	var out []walkerMatch
	walkAll(tree.RootNode(), func(n *sitter.Node) {
		caps := newCaptures()
		if matchTree(pt, n, []byte(target), caps) {
			out = append(out, walkerMatch{captures: caps.byName, outer: n.Type()})
		}
	})
	return out
}

// MUST-PASS smoke tests (per validation contract item 7).

func TestLongTail_Java_FunctionDeclaration(t *testing.T) {
	target := `class Calc {
  void alpha() { return; }
  void beta() { return; }
}
`
	matches := runLongTailWalker(t, javaLangConfig,
		"void $NAME() { return; }", target)
	if len(matches) < 1 {
		t.Fatalf("matches = %d, want ≥ 1 (Java method declaration)", len(matches))
	}
	gotName := false
	for _, m := range matches {
		if cap, ok := m.captures["NAME"]; ok && (cap.Text == "alpha" || cap.Text == "beta") {
			gotName = true
		}
	}
	if !gotName {
		t.Errorf("no Java match captured NAME=alpha|beta; matches=%v", matches)
	}
}

func TestLongTail_Ruby_MethodDef(t *testing.T) {
	target := `def alpha
  1
end

def beta(x)
  x + 1
end
`
	matches := runLongTailWalker(t, rubyLangConfig, "def $NAME\n  $$$BODY\nend", target)
	if len(matches) < 1 {
		t.Fatalf("matches = %d, want ≥ 1 (Ruby method def)", len(matches))
	}
	gotName := false
	for _, m := range matches {
		if cap, ok := m.captures["NAME"]; ok && (cap.Text == "alpha" || cap.Text == "beta") {
			gotName = true
		}
	}
	if !gotName {
		t.Errorf("no Ruby match captured NAME=alpha|beta; matches=%v", matches)
	}
}

func TestLongTail_Bash_FunctionDeclaration(t *testing.T) {
	target := `function alpha() {
  echo hi
}

function beta() {
  echo bye
}
`
	matches := runLongTailWalker(t, bashLangConfig,
		"function $NAME() {\n  $$$BODY\n}", target)
	if len(matches) < 1 {
		t.Fatalf("matches = %d, want ≥ 1 (Bash function)", len(matches))
	}
	gotName := false
	for _, m := range matches {
		if cap, ok := m.captures["NAME"]; ok && (cap.Text == "alpha" || cap.Text == "beta") {
			gotName = true
		}
	}
	if !gotName {
		t.Errorf("no Bash match captured NAME=alpha|beta; matches=%v", matches)
	}
}
