// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"path/filepath"
	"slices"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// extractGoReceiver returns the receiver type name from a method declaration.
// Handles pointer receivers: *Type → Type.
func extractGoReceiver(node *sitter.Node, src []byte) string {
	if node.Type() != "method_declaration" {
		return ""
	}

	receiverNode := node.ChildByFieldName("receiver")
	if receiverNode == nil {
		return ""
	}

	// Walk into the parameter_list to find the type.
	return findTypeIdentifier(receiverNode, src)
}

// findTypeIdentifier recursively finds the first type_identifier in a subtree.
func findTypeIdentifier(node *sitter.Node, src []byte) string {
	if node.Type() == "type_identifier" {
		return node.Content(src)
	}
	for i := range int(node.ChildCount()) {
		if result := findTypeIdentifier(node.Child(i), src); result != "" {
			return result
		}
	}
	return ""
}

// extractGoEmbeds finds embedded structs/interfaces in a type declaration.
// Returns the names of embedded types (without pointer markers).
func extractGoEmbeds(node *sitter.Node, src []byte) []string {
	// Find the struct_type or interface_type within the type_declaration.
	structNode := findNodeByType(node, "struct_type")
	if structNode == nil {
		return nil
	}

	fieldListNode := findNodeByType(structNode, "field_declaration_list")
	if fieldListNode == nil {
		return nil
	}

	var embeds []string
	for i := range int(fieldListNode.NamedChildCount()) {
		field := fieldListNode.NamedChild(i)
		if field.Type() != "field_declaration" {
			continue
		}

		// An embedded field has a type but no explicit name.
		// In tree-sitter Go grammar, embedded fields have the type as the only
		// named child (no "name" field).
		nameNode := field.ChildByFieldName("name")
		if nameNode != nil {
			continue // Has a name — not embedded.
		}

		typeNode := field.ChildByFieldName("type")
		if typeNode == nil {
			continue
		}

		typeName := findTypeIdentifier(typeNode, src)
		if typeName != "" {
			embeds = append(embeds, typeName)
		}
	}

	return embeds
}

// extractGoSignature returns the full function/method signature without body.
func extractGoSignature(node *sitter.Node, src []byte) string {
	bodyNode := node.ChildByFieldName("body")
	if bodyNode == nil {
		// No body (e.g., interface method) — return the whole thing.
		return node.Content(src)
	}

	// Content from start of node to start of body block.
	startByte := node.StartByte()
	bodyStart := bodyNode.StartByte()
	if bodyStart > startByte {
		sig := string(src[startByte:bodyStart])
		return strings.TrimRight(sig, " \t\n")
	}

	return ""
}

// findNodeByType finds the first descendant node with the given type.
func findNodeByType(node *sitter.Node, nodeType string) *sitter.Node {
	if node.Type() == nodeType {
		return node
	}
	for i := range int(node.ChildCount()) {
		if result := findNodeByType(node.Child(i), nodeType); result != nil {
			return result
		}
	}
	return nil
}

// isGoTestFile returns true for files Go's `go test` would compile into the
// test binary: filenames ending in `_test.go`. Mock conventions (mock_*.go,
// *_mock.go, mocks/ directory) are detected separately for TestKind=mock
// classification — those files are NOT in the test binary but contain test-
// support code that should be rerank-deprioritized when searching for
// implementations.
func isGoTestFile(path string) bool {
	base := filepath.Base(path)
	return strings.HasSuffix(base, "_test.go")
}

// isGoMockFile returns true for files whose location/name marks them as
// generated or hand-rolled mocks. Filename-only — generated-by-content
// detection is explicitly out-of-scope for this ticket.
func isGoMockFile(path string) bool {
	base := filepath.Base(path)
	if strings.HasPrefix(base, "mock_") || strings.HasSuffix(base, "_mock.go") {
		return true
	}
	return slices.Contains(strings.Split(filepath.ToSlash(path), "/"), "mocks")
}

// isUpperOrUnderscore checks whether the byte after a Test/Benchmark/Example/
// Fuzz prefix is an underscore or an ASCII uppercase letter. The Go testing
// package reads the prefix that way: `TestFoo`, `Test_Foo` are tests;
// `Testify`, `Testing` are NOT (lowercase letter after "Test").
func isUpperOrUnderscore(b byte) bool {
	return b == '_' || (b >= 'A' && b <= 'Z')
}

// classifyTestKindGo maps a Go declaration to (IsTest, TestKind) following
// the stdlib `testing` package conventions: TestXxx / BenchmarkXxx /
// ExampleXxx / FuzzXxx as test/benchmark/example/fuzz; TestMain as setup;
// everything else in a _test.go file as helper. Mock files (mock_*.go /
// *_mock.go / mocks/) classify as mock regardless of name.
//
// fileCtx and src are unused for Go (no annotations); kept in signature for
// uniformity with other predicates.
func classifyTestKindGo(
	_ *sitter.Node,
	_ []byte,
	chunkType, name string,
	_ ChunkContext,
	filePath string,
) (bool, TestKind) {
	if isGoMockFile(filePath) {
		return true, TestKindMock
	}
	if !isGoTestFile(filePath) {
		return false, TestKindNone
	}
	if chunkType != "function_declaration" {
		return true, TestKindHelper
	}
	switch {
	case name == "TestMain":
		return true, TestKindSetup
	case strings.HasPrefix(name, "Test") && len(name) > 4 && isUpperOrUnderscore(name[4]):
		return true, TestKindTest
	case strings.HasPrefix(name, "Benchmark") && len(name) > 9 && isUpperOrUnderscore(name[9]):
		return true, TestKindBenchmark
	case strings.HasPrefix(name, "Example") && (len(name) == 7 || isUpperOrUnderscore(name[7])):
		return true, TestKindExample
	case strings.HasPrefix(name, "Fuzz") && len(name) > 4 && isUpperOrUnderscore(name[4]):
		return true, TestKindFuzz
	}
	return true, TestKindHelper
}

// extendFrameworksGo appends FrameworkGoTesting to the file's framework set
// when the file is a Go test file (_test.go suffix).
func extendFrameworksGo(
	_ *sitter.Node,
	_ []byte,
	filePath string,
	detected []Framework,
) []Framework {
	if isGoTestFile(filePath) {
		return append(detected, FrameworkGoTesting)
	}
	return detected
}
