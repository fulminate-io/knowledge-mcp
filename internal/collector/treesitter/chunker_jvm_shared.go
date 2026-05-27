// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"path/filepath"
	"slices"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// extractJVMAnnotations returns the simple names of every annotation present
// on a JVM declaration's `modifiers` child. Mirrors tree-sitter Java's shape
// where `class_declaration` / `method_declaration` / `constructor_declaration`
// have a `modifiers` NamedChild whose children include `marker_annotation`
// (e.g. `@Test`) and `annotation` (e.g. `@ParameterizedTest("name")`) entries.
//
// FQN names (`@org.junit.jupiter.api.Test`) are normalized to simple names
// via strings.LastIndex on `.`, yielding `Test`.
//
// Kotlin's tree-sitter grammar emits the same `modifiers` shape on
// function_declaration / class_declaration with `annotation` children, so
// the same walker handles Kotlin without divergence. Scala's tree-sitter
// grammar uses a different structure (annotations appear as siblings, not
// in a modifiers wrapper); the Scala predicate adds a fallback pass.
func extractJVMAnnotations(declNode *sitter.Node, src []byte) []string {
	if declNode == nil {
		return nil
	}
	var modifiers *sitter.Node
	for i := 0; i < int(declNode.NamedChildCount()); i++ {
		child := declNode.NamedChild(i)
		if child.Type() == "modifiers" {
			modifiers = child
			break
		}
	}
	if modifiers == nil {
		return nil
	}
	var out []string
	for i := 0; i < int(modifiers.NamedChildCount()); i++ {
		anno := modifiers.NamedChild(i)
		t := anno.Type()
		if t != "marker_annotation" && t != "annotation" {
			continue
		}
		if name := jvmAnnotationSimpleName(anno, src); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// jvmAnnotationSimpleName extracts the simple name from a marker_annotation
// or annotation node and strips any FQN prefix.
//
// Grammar variations handled:
//   - Java: marker_annotation/annotation has a `name` field (scoped_identifier
//     or identifier).
//   - Kotlin: annotation > user_type > type_identifier — no `name` field.
//   - Scala: annotation > type_identifier directly — no `name` field.
//
// Falls back to recursive descent finding the first `identifier` /
// `type_identifier` / `simple_identifier` when no `name` field is present.
func jvmAnnotationSimpleName(anno *sitter.Node, src []byte) string {
	if nameNode := anno.ChildByFieldName("name"); nameNode != nil {
		full := nameNode.Content(src)
		if i := strings.LastIndex(full, "."); i >= 0 {
			return full[i+1:]
		}
		return full
	}
	if id := findFirstIdentifier(anno, src); id != "" {
		if i := strings.LastIndex(id, "."); i >= 0 {
			return id[i+1:]
		}
		return id
	}
	return ""
}

// findFirstIdentifier descends the AST returning the content of the first
// `identifier` / `type_identifier` / `simple_identifier` node it sees.
// Used for grammars (Kotlin, Scala) whose annotation nodes don't have a
// `name` field but encode the annotation name as a descendant identifier.
func findFirstIdentifier(node *sitter.Node, src []byte) string {
	if node == nil {
		return ""
	}
	t := node.Type()
	if t == "identifier" || t == "type_identifier" || t == "simple_identifier" {
		return node.Content(src)
	}
	for i := 0; i < int(node.NamedChildCount()); i++ {
		if got := findFirstIdentifier(node.NamedChild(i), src); got != "" {
			return got
		}
	}
	return ""
}

// extractScalaAnnotations handles Scala's annotation shape, which differs
// from Java's `modifiers`-wrapper layout. In tree-sitter Scala:
//
//   - Inline annotations live as direct NamedChildren of function_definition
//     (e.g. `@Test def foo()` -> function_definition has an `annotation`
//     NamedChild before the `identifier` name).
//   - Top-level annotations preceding a def appear as PrevNamedSibling.
//   - Java-style `modifiers` wrappers occasionally appear too.
//
// All three shapes are walked.
func extractScalaAnnotations(declNode *sitter.Node, src []byte) []string {
	if declNode == nil {
		return nil
	}
	var out []string
	// 1) Direct NamedChildren of declNode (Scala inline-annotation shape).
	for i := 0; i < int(declNode.NamedChildCount()); i++ {
		child := declNode.NamedChild(i)
		if child.Type() == "annotation" || child.Type() == "marker_annotation" {
			if name := jvmAnnotationSimpleName(child, src); name != "" {
				out = append(out, name)
			}
		}
	}
	// 2) Java-style modifiers wrapper.
	if jvm := extractJVMAnnotations(declNode, src); len(jvm) > 0 {
		out = append(out, jvm...)
	}
	// 3) Sibling-preceding annotations.
	for sib := declNode.PrevNamedSibling(); sib != nil; sib = sib.PrevNamedSibling() {
		if sib.Type() != "annotation" && sib.Type() != "marker_annotation" {
			break
		}
		if name := jvmAnnotationSimpleName(sib, src); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// isJavaTestFile recognizes Maven/Gradle layout and standard naming
// conventions: src/test/java/ source roots, plus *Test.java / *Tests.java /
// *IT.java basenames (Surefire / Failsafe pickup conventions).
func isJavaTestFile(path string) bool {
	slash := filepath.ToSlash(path)
	if strings.Contains(slash, "/src/test/java/") || strings.HasPrefix(slash, "src/test/java/") {
		return true
	}
	base := filepath.Base(path)
	return strings.HasSuffix(base, "Test.java") ||
		strings.HasSuffix(base, "Tests.java") ||
		strings.HasSuffix(base, "IT.java")
}

// isJavaMockFile recognizes Mockito / hand-rolled mock files by basename or
// `__mocks__` segment (cross-language convention for fixture mocks).
func isJavaMockFile(path string) bool {
	base := filepath.Base(path)
	if strings.HasSuffix(base, "Mock.java") {
		return true
	}
	return slices.Contains(strings.Split(filepath.ToSlash(path), "/"), "__mocks__")
}

// isKotlinTestFile recognizes /src/test/ and /src/androidTest/ source roots
// plus *Test.kt / *Tests.kt basenames.
func isKotlinTestFile(path string) bool {
	slash := filepath.ToSlash(path)
	if strings.Contains(slash, "/src/test/") || strings.HasPrefix(slash, "src/test/") ||
		strings.Contains(slash, "/src/androidTest/") || strings.HasPrefix(slash, "src/androidTest/") {
		return true
	}
	base := filepath.Base(path)
	return strings.HasSuffix(base, "Test.kt") || strings.HasSuffix(base, "Tests.kt")
}

// isScalaTestFile recognizes /src/test/ source roots plus *Spec.scala /
// *Test.scala / *Spec.sc / *Test.sc basenames (ScalaTest convention).
func isScalaTestFile(path string) bool {
	slash := filepath.ToSlash(path)
	if strings.Contains(slash, "/src/test/") || strings.HasPrefix(slash, "src/test/") {
		return true
	}
	base := filepath.Base(path)
	return strings.HasSuffix(base, "Spec.scala") ||
		strings.HasSuffix(base, "Test.scala") ||
		strings.HasSuffix(base, "Spec.sc") ||
		strings.HasSuffix(base, "Test.sc")
}
