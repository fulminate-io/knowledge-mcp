// SPDX-License-Identifier: Apache-2.0

// layout_differentials_test.go — the tree differentials the layout census
// measures with, split out of layout_census_test.go to keep both files under the
// commit-blocking length cap.
//
// TWO DETECTORS, ANSWERING TWO DIFFERENT QUESTIONS, and neither subsumes the
// other. anonWhitespaceTokens finds a whitespace-ONLY anonymous child present in
// one spelling and not the other — the phenomenon a child-list skip fixes.
// absorbedWhitespaceTokens finds a token present in BOTH spellings whose text
// differs only by surrounding whitespace — the phenomenon a trimmed token
// comparison fixes, and one the first detector provably cannot see, because an
// absorbed token like "\n<" is not whitespace-only.
//
// The remaining helpers are shared measurement plumbing: a parse that refuses to
// report a verdict off an error-recovery tree, and the named-kind sequence the
// census uses to tell a layout difference from a structural one.

package ast

import (
	"context"
	"sort"
	"strings"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// parseClean parses src and reports ok=false when the parse fails or the tree
// carries ERROR nodes. The caller owns the returned tree.
func parseClean(t *testing.T, lang treesitter.Language, src string) (*sitter.Tree, []byte, bool) {
	t.Helper()
	parser := treesitter.NewParser()
	defer parser.Close()
	tree, err := parser.Parse(context.Background(), []byte(src), lang)
	if err != nil {
		return nil, nil, false
	}
	root := tree.RootNode()
	if root == nil || root.HasError() {
		tree.Close()
		return nil, nil, false
	}
	return tree, []byte(src), true
}

// anonWhitespaceTokens counts, across the whole tree, every anonymous childless
// token whose source text is whitespace only. Keyed by that text.
func anonWhitespaceTokens(root *sitter.Node, src []byte) map[string]int {
	out := map[string]int{}
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil {
			return
		}
		count := int(n.ChildCount())
		for i := range count {
			c := n.Child(i)
			if c == nil {
				continue
			}
			if !c.IsNamed() && c.ChildCount() == 0 {
				if text := c.Content(src); text != "" && strings.TrimSpace(text) == "" {
					out[text]++
				}
			}
			walk(c)
		}
	}
	walk(root)
	return out
}

// anonChildlessTexts returns, in document order, the source text of every
// anonymous childless token in the tree — the same population
// anonWhitespaceTokens counts, kept ordered rather than tallied so two trees can
// be compared position by position.
func anonChildlessTexts(root *sitter.Node, src []byte) []string {
	var out []string
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil {
			return
		}
		for i := range int(n.ChildCount()) {
			c := n.Child(i)
			if c == nil {
				continue
			}
			if !c.IsNamed() && c.ChildCount() == 0 {
				out = append(out, c.Content(src))
			}
			walk(c)
		}
	}
	walk(root)
	return out
}

// absorbedWhitespaceTokens returns, sorted and deduplicated, every multi-line
// token text that occupies the same position as a one-line token, differs from
// it, and is EQUAL once both are whitespace-trimmed. That is token-span
// absorption: layout whitespace living inside a token that also carries
// meaningful bytes.
//
// IT IS A DISTINCT DETECTOR, NOT A GENERALIZATION of the whitespace-only one,
// and the distinction is measured rather than argued. anonWhitespaceTokens
// counts tokens whose text is whitespace ONLY; an absorbed token like "\n<" is
// not one, so running that detector over a JSX probe pair returns an empty map
// and a verdict of layout=no. A census extended with JSX probes alone would have
// measured nothing.
//
// Positions are compared pairwise in document order, up to the shorter
// sequence. A length difference means the two spellings parsed to different
// token counts, which is a structural difference rather than a layout one — the
// layout verdict's own structural check is what reports that.
func absorbedWhitespaceTokens(multiRoot *sitter.Node, multiSrc []byte, singleRoot *sitter.Node, singleSrc []byte) []string {
	multi := anonChildlessTexts(multiRoot, multiSrc)
	single := anonChildlessTexts(singleRoot, singleSrc)
	seen := map[string]bool{}
	for i := 0; i < len(multi) && i < len(single); i++ {
		if multi[i] == single[i] {
			continue
		}
		if strings.TrimSpace(multi[i]) == strings.TrimSpace(single[i]) {
			seen[multi[i]] = true
		}
	}
	out := make([]string, 0, len(seen))
	for text := range seen {
		out = append(out, text)
	}
	sort.Strings(out)
	return out
}

// extraWhitespaceTokens returns, sorted, every token text the multi-line tree
// carries more often than the one-line tree.
func extraWhitespaceTokens(multi, single map[string]int) []string {
	out := make([]string, 0, len(multi))
	for text, n := range multi {
		if n > single[text] {
			out = append(out, text)
		}
	}
	sort.Strings(out)
	return out
}

// namedKinds returns the pre-order sequence of named node kinds in the tree.
// Two spellings that differ only in layout produce the same sequence; a
// difference means the layout change also changed what the source says.
func namedKinds(root *sitter.Node) []string {
	out := []string{}
	walkAll(root, func(n *sitter.Node) {
		if n != nil {
			out = append(out, n.Type())
		}
	})
	return out
}
