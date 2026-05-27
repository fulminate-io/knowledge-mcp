// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"path/filepath"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// extractCSharpAttributes returns the simple names of every attribute on a
// C# declaration. Tree-sitter shape:
//
//	method_declaration
//	  attribute_list
//	    attribute
//	      identifier  ← simple name
//
// Multiple attribute_list children may appear (e.g. `[Test] [Category("x")]`)
// and each attribute_list may contain multiple attribute entries.
//
// Dot-qualified attribute usages (`[NUnit.Framework.Test]`) appear as a
// `qualified_name` instead of `identifier`. We strip the dot prefix and
// return the trailing simple name.
//
// Per locked Q6, ONLY short-form names are recognized — `[TestAttribute]`
// (long form, allowed by C# but rare in real code) is intentionally not
// matched. Adding it later is a one-line tweak in classifyTestKindCSharp.
func extractCSharpAttributes(declNode *sitter.Node, src []byte) []string {
	if declNode == nil {
		return nil
	}
	var out []string
	for i := 0; i < int(declNode.NamedChildCount()); i++ {
		child := declNode.NamedChild(i)
		if child.Type() != "attribute_list" {
			continue
		}
		for j := 0; j < int(child.NamedChildCount()); j++ {
			anno := child.NamedChild(j)
			if anno.Type() != "attribute" {
				continue
			}
			if name := csharpAttributeSimpleName(anno, src); name != "" {
				out = append(out, name)
			}
		}
	}
	return out
}

// csharpAttributeSimpleName extracts the trailing simple name from an
// attribute node, stripping any FQN prefix.
func csharpAttributeSimpleName(anno *sitter.Node, src []byte) string {
	for i := 0; i < int(anno.NamedChildCount()); i++ {
		child := anno.NamedChild(i)
		t := child.Type()
		if t != "identifier" && t != "qualified_name" {
			continue
		}
		full := child.Content(src)
		if i := strings.LastIndex(full, "."); i >= 0 {
			return full[i+1:]
		}
		return full
	}
	return ""
}

// isCSharpTestFile recognizes *Tests.cs / *.Tests.cs / *Test.cs basenames
// and a `Tests/` path segment.
func isCSharpTestFile(path string) bool {
	base := filepath.Base(path)
	if strings.HasSuffix(base, "Tests.cs") || strings.HasSuffix(base, "Test.cs") {
		return true
	}
	for seg := range strings.SplitSeq(filepath.ToSlash(path), "/") {
		if seg == "Tests" || seg == "tests" {
			return true
		}
	}
	return false
}

// classifyTestKindCSharp dispatches on attribute simple names.
//
// Short-form attribute matching only (locked Q6). The long form
// `[TestAttribute]` is NOT matched; users who care can add it as a one-line
// alias.
func classifyTestKindCSharp(
	declNode *sitter.Node,
	src []byte,
	chunkType, _ string,
	_ ChunkContext,
	filePath string,
) (bool, TestKind) {
	if !isCSharpTestFile(filePath) {
		return false, TestKindNone
	}
	if chunkType == "class_declaration" || chunkType == "interface_declaration" ||
		chunkType == "struct_declaration" || chunkType == "enum_declaration" ||
		chunkType == "namespace_declaration" {
		return true, TestKindHelper
	}
	for _, a := range extractCSharpAttributes(declNode, src) {
		switch a {
		case "Test", "Fact", "Theory", "TestMethod":
			return true, TestKindTest
		case "Benchmark":
			return true, TestKindBenchmark
		case "SetUp", "OneTimeSetUp", "TestInitialize":
			return true, TestKindSetup
		case "TearDown", "OneTimeTearDown", "TestCleanup":
			return true, TestKindTeardown
		}
	}
	return true, TestKindHelper
}
