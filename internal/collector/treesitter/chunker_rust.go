// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"path/filepath"
	"slices"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// isRustTestFile returns true when the path lives under a `tests/` directory
// (Cargo's integration-test convention).
func isRustTestFile(path string) bool {
	return slices.Contains(strings.Split(filepath.ToSlash(path), "/"), "tests")
}

// extractRustAttributes walks the PrevNamedSibling chain collecting source
// text of every attribute_item / inner_attribute_item that precedes the
// declaration. Returns the attributes in source order (closest to declNode
// last). Each entry is the literal `#[...]` source text.
//
// Tree-sitter Rust shape:
//
//	attribute_item: "#[test]"
//	  attribute: "test"
//	    identifier: "test"
//	function_item: "fn t() {}"
//
// Multiple attributes stack; we walk siblings until we hit a non-attribute
// node, preserving order for diagnostics. Comments between attributes are
// rare but we tolerate them by continuing past comment / line_comment
// siblings.
func extractRustAttributes(declNode *sitter.Node, src []byte) []string {
	if declNode == nil {
		return nil
	}
	var collected []string
	for sib := declNode.PrevNamedSibling(); sib != nil; sib = sib.PrevNamedSibling() {
		t := sib.Type()
		if t == "attribute_item" || t == "inner_attribute_item" {
			collected = append([]string{sib.Content(src)}, collected...)
			continue
		}
		if t == "line_comment" || t == "block_comment" || t == "comment" {
			continue
		}
		break
	}
	return collected
}

// rustAttributeHeadName parses a Rust attribute string and returns the head
// path identifier suitable for allowlist comparison.
//
// Handled forms:
//
//	#[test]                                  -> "test"
//	#[tokio::test]                           -> "tokio::test"
//	#[cfg(test)]                             -> "cfg(test)"
//	#[cfg(fuzzing)]                          -> "cfg(fuzzing)"
//	#[serde(rename = "test_name")]           -> "serde"
//	#[divan::bench]                          -> "divan::bench"
//	#[criterion::benchmark]                  -> "criterion::benchmark"
//	#[doc = "test helpers"]                  -> "doc"
//
// Strips: leading `#[` / `#![`, trailing `]`, leading whitespace.
//
// `cfg(...)` is special-cased: we preserve the inner token (e.g. "test" or
// "fuzzing") as `cfg(<inner>)`. For any other path-with-args attribute, the
// args are dropped and only the head path is returned (`serde(rename=...)` →
// `serde`).
func rustAttributeHeadName(attr string) string {
	s := strings.TrimSpace(attr)
	s = strings.TrimPrefix(s, "#![")
	s = strings.TrimPrefix(s, "#[")
	s = strings.TrimSuffix(s, "]")
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// `#[doc = "..."]` — head is everything before the first `=` or whitespace.
	if eqIdx := strings.IndexByte(s, '='); eqIdx >= 0 {
		// But ONLY when the `=` precedes any `(` (otherwise the `=` is inside
		// a function-style attribute argument list like `cfg(target = "x")`).
		parenIdx := strings.IndexByte(s, '(')
		if parenIdx < 0 || eqIdx < parenIdx {
			head := strings.TrimSpace(s[:eqIdx])
			// Trim a trailing whitespace-token so `doc = "..."` -> "doc".
			if sp := strings.IndexAny(head, " \t"); sp >= 0 {
				head = head[:sp]
			}
			return head
		}
	}
	// Find the open-paren if any.
	headRaw, rest, hasParen := strings.Cut(s, "(")
	if !hasParen {
		return s
	}
	head := strings.TrimSpace(headRaw)
	if head != "cfg" {
		return head
	}
	// cfg(...) — extract the gated condition. Find the matching close-paren
	// and read the first identifier-ish token inside.
	closeIdx := strings.LastIndexByte(rest, ')')
	if closeIdx < 0 {
		return head
	}
	inner := strings.TrimSpace(rest[:closeIdx])
	// Only the leading identifier matters for cfg gating (cfg(test, foo=bar)
	// → "test"). Splitting on the first non-identifier byte gives us that.
	end := 0
	for end < len(inner) {
		b := inner[end]
		if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_' {
			end++
			continue
		}
		break
	}
	if end == 0 {
		return head
	}
	return "cfg(" + inner[:end] + ")"
}

// isRustTestRelatedAttr returns true iff the head name is in the explicit
// allowlist of test/bench/fuzz attribute heads.
//
// Why allowlist: substring matching against the raw attribute string
// produces FALSE POSITIVES like `#[serde(rename = "test_name")]` and
// `#[doc = "test helpers"]` (both contain "test") AND FALSE NEGATIVES like
// `#[bench]`, `#[divan::bench]`, `#[criterion::benchmark]`, `#[cfg(fuzzing)]`
// (none contain "test"). Head-name-against-allowlist is the only correct
// shape.
func isRustTestRelatedAttr(headName string) bool {
	switch headName {
	case "test",
		"tokio::test",
		"rstest",
		"test_case",
		"bench",
		"divan::bench",
		"criterion::benchmark",
		"cfg(test)",
		"cfg(fuzzing)":
		return true
	}
	return false
}

// rustHasTestAttribute recursively scans the AST for any attribute_item or
// inner_attribute_item whose head name is in the test-attribute allowlist.
// Used by extendFrameworksRust to decide whether to add FrameworkRustTest
// to the file's framework set even when no `tests::test`-named import is
// detected (Rust's stdlib `#[test]` has no use-declaration to detect).
func rustHasTestAttribute(node *sitter.Node, src []byte) bool {
	if node == nil {
		return false
	}
	t := node.Type()
	if t == "attribute_item" || t == "inner_attribute_item" {
		head := rustAttributeHeadName(node.Content(src))
		if isRustTestRelatedAttr(head) {
			return true
		}
	}
	for i := 0; i < int(node.NamedChildCount()); i++ {
		if rustHasTestAttribute(node.NamedChild(i), src) {
			return true
		}
	}
	return false
}

// extendFrameworksRust appends FrameworkRustTest when the file contains any
// recognized test attribute. Preserves the input slice (locked extender
// contract — extenders MUST only append).
func extendFrameworksRust(
	root *sitter.Node,
	src []byte,
	_ string,
	detected []Framework,
) []Framework {
	if rustHasTestAttribute(root, src) {
		return append(detected, FrameworkRustTest)
	}
	return detected
}

// classifyTestKindRust classifies Rust declarations using sibling
// attribute_item walking + a closed-set head-name allowlist.
//
// Match priority (first match wins, descending specificity):
//
//	cfg(fuzzing)                                -> Fuzz
//	bench / divan::bench / criterion::benchmark -> Benchmark
//	test / tokio::test / rstest / test_case     -> Test
//
// If no allowlisted attribute matched but the file is in a `tests/`
// directory OR FrameworkRustTest is in fileCtx.Frameworks, the
// declaration classifies as Helper. Otherwise (false, TestKindNone).
//
// Doctests (`///` example blocks) are deferred per locked Q3.
func classifyTestKindRust(
	declNode *sitter.Node,
	src []byte,
	_, _ string,
	fileCtx ChunkContext,
	filePath string,
) (bool, TestKind) {
	attrs := extractRustAttributes(declNode, src)

	// First pass: collect head names by category for priority handling.
	hasFuzz, hasBench, hasTest := false, false, false
	for _, a := range attrs {
		head := rustAttributeHeadName(a)
		switch head {
		case "cfg(fuzzing)":
			hasFuzz = true
		case "bench", "divan::bench", "criterion::benchmark":
			hasBench = true
		case "test", "tokio::test", "rstest", "test_case", "quickcheck":
			hasTest = true
		}
	}
	switch {
	case hasFuzz:
		return true, TestKindFuzz
	case hasBench:
		return true, TestKindBenchmark
	case hasTest:
		return true, TestKindTest
	}

	// Fall-through: helper if the file is recognized as test-bearing.
	if isRustTestFile(filePath) || frameworksContain(fileCtx.Frameworks, FrameworkRustTest) {
		return true, TestKindHelper
	}
	return false, TestKindNone
}

// frameworksContain checks for the presence of fw in fws.
func frameworksContain(fws []Framework, fw Framework) bool {
	return slices.Contains(fws, fw)
}
