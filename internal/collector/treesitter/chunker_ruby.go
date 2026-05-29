// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"path/filepath"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// isRubyTestFile recognizes RSpec / Minitest / test-unit layouts:
// `_spec.rb` / `_test.rb` basenames, and `spec/` / `test/` segments.
func isRubyTestFile(path string) bool {
	base := filepath.Base(path)
	if strings.HasSuffix(base, "_spec.rb") || strings.HasSuffix(base, "_test.rb") {
		return true
	}
	for seg := range strings.SplitSeq(filepath.ToSlash(path), "/") {
		if seg == "spec" || seg == "test" {
			return true
		}
	}
	return false
}

// rubyCallIdentifier extracts the callee identifier name from a Ruby
// tree-sitter `call` node.
//
// REUSE NOTE (T2-A): mirrors the SHAPE of callExpressionName at
// chunker_identity.go:121, but uses field name `method` because Ruby's
// tree-sitter grammar names the callee leaf via `method` rather than
// `function`. Genuine field-name divergence in the upstream grammar — not
// a copy-paste fork. Receiver is intentionally ignored: `RSpec.describe`
// is dispatched by the method identifier (`describe`); the receiver is
// noise for the (is_test, kind) decision.
func rubyCallIdentifier(declNode *sitter.Node, src []byte) string {
	if declNode == nil {
		return ""
	}
	method := declNode.ChildByFieldName("method")
	if method != nil && method.Type() == "identifier" {
		return method.Content(src)
	}
	return ""
}

// classifyTestBlockRuby covers RSpec, Minitest block-form, test-unit, and
// the common fixture/mock idioms.
func classifyTestBlockRuby(
	declNode *sitter.Node,
	src []byte,
	_ testBlockCaptures,
	_ ChunkContext,
	filePath string,
) (bool, TestKind) {
	if !isRubyTestFile(filePath) {
		return false, TestKindNone
	}
	switch rubyCallIdentifier(declNode, src) {
	case "it", "specify", "test", "example", "focus", "fit", "xit", "xtest", "xspecify", "skip", "pending":
		return true, TestKindTest
	case "describe", "context", "fcontext", "fdescribe":
		return true, TestKindTest
	case "before", "setup":
		return true, TestKindSetup
	case "after", "teardown":
		return true, TestKindTeardown
	case "let", "let!", "subject":
		return true, TestKindFixture
	case "instance_double", "class_double", "double", "spy":
		return true, TestKindMock
	case "allow", "expect":
		return true, TestKindMock
	}
	return false, TestKindNone
}

// rubyEnclosingClassSuperclass walks up from declNode to find the enclosing
// `class` node (tree-sitter-ruby type "class") and returns the textual content
// of its superclass field. Returns "" when there is no enclosing class or no
// superclass declaration. The returned string typically begins with `<` and
// whitespace (`< Minitest::Test`); callers should use strings.Contains rather
// than equality.
func rubyEnclosingClassSuperclass(node *sitter.Node, src []byte) string {
	if node == nil {
		return ""
	}
	for p := node.Parent(); p != nil; p = p.Parent() {
		if p.Type() != "class" {
			continue
		}
		sup := p.ChildByFieldName("superclass")
		if sup != nil {
			return sup.Content(src)
		}
		// Fallback: scan named children for a "superclass" type node, in case
		// a future tree-sitter-ruby release stops emitting the field name.
		for i := range int(p.NamedChildCount()) {
			c := p.NamedChild(i)
			if c.Type() == "superclass" {
				return c.Content(src)
			}
		}
		return ""
	}
	return ""
}

// classifyTestKindRuby is Ruby's Bucket A predicate. Recognizes class+method
// form for Minitest::Test and Test::Unit::TestCase. Ruby is a dual-bucket
// language (Bucket B already covers RSpec describe/it block form via
// classifyTestBlockRuby).
//
// Match priority:
//  1. Non-test file (per isRubyTestFile) → (false, TestKindNone).
//  2. Method inside class extending Minitest::Test or Test::Unit::TestCase:
//     - name `test_*` → TestKindTest
//     - name `setup` / `setup_all` → TestKindSetup
//     - name `teardown` / `teardown_all` → TestKindTeardown
//     - else → TestKindHelper
//  3. Method outside such a class but in a test file → (false, TestKindNone)
//     so RSpec block-form callers (Bucket B) handle the file. The enclosing-
//     class test (`< Minitest::Test`) is the explicit signal; absent it, leave
//     classification to Bucket B.
func classifyTestKindRuby(
	declNode *sitter.Node,
	src []byte,
	chunkType, name string,
	_ ChunkContext,
	filePath string,
) (bool, TestKind) {
	if !isRubyTestFile(filePath) {
		return false, TestKindNone
	}
	if chunkType != "method" && chunkType != "singleton_method" {
		return false, TestKindNone
	}
	sup := rubyEnclosingClassSuperclass(declNode, src)
	if !strings.Contains(sup, "Minitest::Test") && !strings.Contains(sup, "Test::Unit::TestCase") {
		return false, TestKindNone
	}
	switch name {
	case "setup", "setup_all":
		return true, TestKindSetup
	case "teardown", "teardown_all":
		return true, TestKindTeardown
	}
	if strings.HasPrefix(name, "test_") {
		return true, TestKindTest
	}
	return true, TestKindHelper
}

func init() {
	testBlockClassifiers[LangRuby] = classifyTestBlockRuby
}
