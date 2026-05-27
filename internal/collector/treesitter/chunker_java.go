// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	sitter "github.com/smacker/go-tree-sitter"
)

// classifyTestKindJava maps Java declarations to (IsTest, TestKind) using
// JUnit 4 / JUnit 5 / TestNG / JMH annotations. The dispatch table works on
// simple annotation names returned by extractJVMAnnotations — FQN forms
// (`@org.junit.jupiter.api.Test`) and bare forms (`@Test`) both normalize to
// the same simple name (`Test`).
//
// Framework-namespace disambiguation: `@Test` exists in both JUnit and
// TestNG. We don't disambiguate — both produce TestKindTest. Per the locked
// scope, frameworks are detected via imports (chunk.Context.Frameworks) and
// surfaced separately; the test-kind classification is framework-agnostic.
//
// Mock-file detection lands first so non-test mock helpers classify as
// TestKindMock regardless of `tests/` location.
func classifyTestKindJava(
	declNode *sitter.Node,
	src []byte,
	chunkType, _ string,
	_ ChunkContext,
	filePath string,
) (bool, TestKind) {
	if isJavaMockFile(filePath) {
		return true, TestKindMock
	}
	if !isJavaTestFile(filePath) {
		return false, TestKindNone
	}

	// Class-level annotations on a type don't trigger TestKindTest by
	// themselves — only methods are tests. Class chunks in test files
	// classify as helper.
	if chunkType == "class_declaration" || chunkType == "interface_declaration" || chunkType == "enum_declaration" {
		return true, TestKindHelper
	}

	annos := extractJVMAnnotations(declNode, src)
	if kind, ok := jvmAnnotationKind(annos); ok {
		return true, kind
	}
	return true, TestKindHelper
}

// jvmAnnotationKind dispatches on the simple-name list returned by
// extractJVMAnnotations. Recognized:
//
//   - JUnit 5 / JUnit 4: Test, RepeatedTest, ParameterizedTest, TestFactory,
//     BeforeEach / Before, AfterEach / After, BeforeAll / BeforeClass,
//     AfterAll / AfterClass.
//   - TestNG: Test, BeforeMethod, AfterMethod, BeforeClass, AfterClass,
//     BeforeSuite, AfterSuite, DataProvider.
//   - JMH: Benchmark, Setup, TearDown.
//
// JUnit 4 `@Before`/`@After` and JUnit 5 `@BeforeEach`/`@AfterEach` are
// per-method setup/teardown. `@BeforeClass`/`@BeforeAll` and
// `@AfterClass`/`@AfterAll` are also setup/teardown — Bucket A's enum has
// no class-level distinction.
func jvmAnnotationKind(annos []string) (TestKind, bool) {
	for _, a := range annos {
		switch a {
		case "Test", "RepeatedTest", "ParameterizedTest", "TestFactory":
			return TestKindTest, true
		case "Benchmark":
			return TestKindBenchmark, true
		case "BeforeEach", "Before", "BeforeAll", "BeforeClass",
			"BeforeMethod", "BeforeSuite", "Setup":
			return TestKindSetup, true
		case "AfterEach", "After", "AfterAll", "AfterClass",
			"AfterMethod", "AfterSuite", "TearDown":
			return TestKindTeardown, true
		case "DataProvider":
			return TestKindFixture, true
		}
	}
	return TestKindNone, false
}
