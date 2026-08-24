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

// goInterfaceParentName returns the name of the interface a method spec belongs
// to, ascending method_elem → interface_type → type_spec and reading that spec's
// `name:` field. Returns "" if any link in that chain is absent.
//
// IT IS THE SIBLING OF extractGoReceiver: that helper answers "which type does
// this method_declaration hang off", and this one answers the same question for
// a method_elem. The ascent is EXACT rather than a search — every link is
// checked by kind — so an anonymous interface, whose interface_type has no
// type_spec parent, yields "" and never borrows an unrelated name.
//
// findEnclosingScope is deliberately NOT reused: classLikeByLang's Go row is
// empty, and admitting a kind there would change parent resolution for every Go
// declaration.
//
// A GENERIC INTERFACE STILL RESOLVES. `type Gen[T any] interface{...}` puts a
// type_parameter_list beside the name, but the `name:` FIELD still binds the
// type_identifier, so the field read is unaffected by the sibling.
func goInterfaceParentName(declNode *sitter.Node, src []byte) string {
	if declNode == nil || declNode.Type() != "method_elem" {
		return ""
	}
	ifaceNode := declNode.Parent()
	if ifaceNode == nil || ifaceNode.Type() != "interface_type" {
		return ""
	}
	specNode := ifaceNode.Parent()
	if specNode == nil || specNode.Type() != "type_spec" {
		return ""
	}
	nameNode := specNode.ChildByFieldName("name")
	if nameNode == nil {
		return ""
	}
	return nameNode.Content(src)
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

// qualifiedTypeName returns an embedded field's target with its package
// qualifier intact — "pkg.Base" for a qualified embed, "Base" for a local
// one — so the resolution walk can bind it through the file's imports.
//
// IT IS A SIBLING OF findTypeIdentifier, NOT A REPLACEMENT FOR IT.
// findTypeIdentifier must keep returning the bare name because
// extractGoReceiver derives a METHOD RECEIVER's type from it, and a receiver
// type is always declared in the method's own package — bare is correct there,
// and a qualified receiver is not expressible in the language. Changing the
// shared helper would silently rewrite every Go method's ParentName and move
// the receiver-qualified node IDs TestChunkGoEdges pins.
func qualifiedTypeName(node *sitter.Node, src []byte) string {
	if node == nil {
		return ""
	}
	switch node.Type() {
	case "qualified_type":
		return node.Content(src)
	case "pointer_type":
		// `*pkg.Base` — the star is not part of the target name. The grammar
		// attaches no field name to the pointee, so it is the first named
		// child.
		if node.NamedChildCount() > 0 {
			return qualifiedTypeName(node.NamedChild(0), src)
		}
	case "generic_type":
		// `pkg.Base[T]` embeds pkg.Base; the type arguments are references of
		// their own and are captured separately as type references.
		if base := node.ChildByFieldName("type"); base != nil {
			return qualifiedTypeName(base, src)
		}
	}
	return findTypeIdentifier(node, src)
}

// extractGoEmbeds finds the embedded fields of a STRUCT type declaration and
// returns their type spellings, pointer markers stripped.
//
// THE STRUCT BODY IS BOUND FROM THE type_spec's `type` FIELD, NEVER SEARCHED
// FOR. A depth-first descent for a struct_type finds one nested ANYWHERE in the
// declaration, so `type IfaceWithAnonStructParam interface { F(x struct{ Base })
// error }` yielded [Base] — an embed credited to an interface that embeds
// nothing, and a false EMBEDS edge on the graph. Proven by executing both walks
// over that fixture: the descent returns [Base], the field read returns nothing.
// The field read is also the CHEAPER of the two, and it is generic-safe: `type
// Gen[T any] struct { EmbG }` binds its struct_type through the `type` field
// despite the sibling type_parameter_list.
//
// A GROUPED DECLARATION DECLINES. See goSoleTypeSpec: several type_specs share
// one type_declaration node, so an extractor handed only the declaration cannot
// tell which spec it is serving, and crediting every spec with the first one's
// embeds is a confident wrong answer where nil is an honest one.
func extractGoEmbeds(node *sitter.Node, src []byte) []string {
	spec := goSoleTypeSpec(node)
	if spec == nil {
		return nil
	}
	structNode := spec.ChildByFieldName("type")
	if structNode == nil || structNode.Type() != "struct_type" {
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

		typeName := qualifiedTypeName(typeNode, src)
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
