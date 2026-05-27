// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"path/filepath"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// isCppTestFile recognizes C/C++ test layouts: filename `_test.{c,cc,cpp,cxx}`
// suffix, `test_` prefix, OR a `tests/` path segment combined with a matching
// filename pattern. T3-A discipline: pure `tests/` segment alone is too broad
// (some projects have non-test tests/ directories).
func isCppTestFile(path string) bool {
	base := filepath.Base(path)
	suffixes := []string{
		"_test.c", "_test.cc", "_test.cpp", "_test.cxx", "_test.c++",
		"_unittest.c", "_unittest.cc", "_unittest.cpp", "_unittest.cxx",
	}
	for _, s := range suffixes {
		if strings.HasSuffix(base, s) {
			return true
		}
	}
	if strings.HasPrefix(base, "test_") {
		ext := filepath.Ext(base)
		switch ext {
		case ".c", ".cc", ".cpp", ".cxx", ".c++":
			return true
		}
	}
	return false
}

// isCppMockFile recognizes mock filename conventions in C/C++ projects:
// `mock_*.{cc,cpp,cxx,c,h,hpp,hh}` prefix, `*_mock.{...}` suffix, and the
// `mocks/` / `__mocks__/` path segments.
func isCppMockFile(path string) bool {
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	switch ext {
	case ".c", ".cc", ".cpp", ".cxx", ".h", ".hpp", ".hh":
		// proceed
	default:
		return false
	}
	stem := strings.TrimSuffix(base, ext)
	if strings.HasPrefix(stem, "mock_") || strings.HasSuffix(stem, "_mock") {
		return true
	}
	for seg := range strings.SplitSeq(filepath.ToSlash(path), "/") {
		if seg == "mocks" || seg == "__mocks__" {
			return true
		}
	}
	return false
}

// classifyTestKindCpp covers C and C++ test files via filename-driven
// classification. Mock files are TestKindMock; test files are TestKindHelper
// (the Bucket B test_block predicate overrides Helper for chunks that match
// gtest/Catch2/etc. macro shapes).
func classifyTestKindCpp(
	_ *sitter.Node,
	_ []byte,
	_, _ string,
	_ ChunkContext,
	filePath string,
) (bool, TestKind) {
	if isCppMockFile(filePath) {
		return true, TestKindMock
	}
	if isCppTestFile(filePath) {
		return true, TestKindHelper
	}
	return false, TestKindNone
}

// classifyTestBlockCpp covers gtest (TEST/TEST_F/TEST_P/TYPED_TEST), Catch2
// (TEST_CASE/SECTION/SCENARIO), Boost.Test (BOOST_AUTO_TEST_CASE), Unity
// (RUN_TEST), cmocka, doctest (TEST_CASE), Google-Benchmark (BENCHMARK), and
// gmock MOCK_METHOD declarations.
//
// The query's #match? filter pre-screens to the regex set, so we only need
// to dispatch on the macro name (BENCHMARK → benchmark, MOCK_METHOD → mock,
// everything else → test). The macro name is read from declNode directly
// because testBlockCaptures doesn't carry @fn — it's the chunker convention
// to keep that struct framework-neutral.
func classifyTestBlockCpp(
	declNode *sitter.Node,
	src []byte,
	_ testBlockCaptures,
	_ ChunkContext,
	filePath string,
) (bool, TestKind) {
	if isCppMockFile(filePath) {
		return true, TestKindMock
	}
	if !isCppTestFile(filePath) {
		return false, TestKindNone
	}
	macroName := cppMacroName(declNode, src)
	switch macroName {
	case "BENCHMARK":
		return true, TestKindBenchmark
	case "MOCK_METHOD", "MOCK_CONST_METHOD":
		return true, TestKindMock
	}
	return true, TestKindTest
}

// cppMacroName returns the macro identifier from a TestBlocks @decl node.
// Two shapes:
//
//   - call_expression (C grammar, plus C++ for string/identifier-arg macros):
//     read function field's identifier.
//   - function_definition (C++ grammar for TEST(Suite, Name) {} shape): read
//     declarator > function_declarator > declarator > identifier.
func cppMacroName(declNode *sitter.Node, src []byte) string {
	if declNode == nil {
		return ""
	}
	switch declNode.Type() {
	case "call_expression":
		return callExpressionName(declNode, src)
	case "function_definition":
		decl := declNode.ChildByFieldName("declarator")
		if decl == nil {
			return ""
		}
		// function_declarator > declarator: identifier
		inner := decl.ChildByFieldName("declarator")
		if inner != nil && inner.Type() == "identifier" {
			return inner.Content(src)
		}
	}
	return ""
}

func init() {
	testKindClassifiers[LangC] = classifyTestKindCpp
	testKindClassifiers[LangCPP] = classifyTestKindCpp
	testBlockClassifiers[LangC] = classifyTestBlockCpp
	testBlockClassifiers[LangCPP] = classifyTestBlockCpp
}
