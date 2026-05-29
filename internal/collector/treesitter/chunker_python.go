// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"path/filepath"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// isPythonTestFile returns true for files pytest / unittest would discover:
// basename starts with `test_`, ends with `_test.py`, equals `conftest.py`,
// OR the path contains a `tests` / `test` segment.
func isPythonTestFile(path string) bool {
	base := filepath.Base(path)
	if base == "conftest.py" {
		return true
	}
	if strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py") {
		return true
	}
	if strings.HasSuffix(base, "_test.py") {
		return true
	}
	for seg := range strings.SplitSeq(filepath.ToSlash(path), "/") {
		if seg == "tests" || seg == "test" {
			return true
		}
	}
	return false
}

// extractPythonDecoratorNames returns the textual names of every decorator
// applied to a decorated_definition node, with the leading `@` stripped.
// E.g. `@pytest.fixture(autouse=True)` -> `pytest.fixture(autouse=True)`.
// Returns nil for non-decorated nodes.
func extractPythonDecoratorNames(declNode *sitter.Node, src []byte) []string {
	if declNode == nil || declNode.Type() != "decorated_definition" {
		return nil
	}
	var out []string
	for i := range int(declNode.NamedChildCount()) {
		child := declNode.NamedChild(i)
		if child.Type() != "decorator" {
			continue
		}
		text := strings.TrimSpace(child.Content(src))
		text = strings.TrimPrefix(text, "@")
		out = append(out, text)
	}
	return out
}

// pythonInnerDef descends a decorated_definition to its inner
// function_definition or class_definition NamedChild. Returns nil when the
// input is not a decorated_definition or the inner def is missing.
func pythonInnerDef(declNode *sitter.Node) *sitter.Node {
	if declNode == nil || declNode.Type() != "decorated_definition" {
		return nil
	}
	for i := range int(declNode.NamedChildCount()) {
		child := declNode.NamedChild(i)
		switch child.Type() {
		case "function_definition", "class_definition":
			return child
		}
	}
	return nil
}

// pythonNameFieldOrEmpty returns the value of node.ChildByFieldName("name")
// content, or "" if the node is nil or has no name field.
func pythonNameFieldOrEmpty(node *sitter.Node, src []byte) string {
	if node == nil {
		return ""
	}
	if nn := node.ChildByFieldName("name"); nn != nil {
		return nn.Content(src)
	}
	return ""
}

// pytestFixtureDecoratorKind matches a pytest fixture decorator string and
// returns the corresponding TestKind:
//
//   - `@pytest.fixture(autouse=True)` or `@fixture(autouse=True)` -> Setup
//     (auto-applied per test, equivalent semantically to a setup hook).
//   - any other `@pytest.fixture[(...)]` or `@fixture[(...)]` -> Fixture.
//
// Returns ("" /TestKindNone, false) when the decorator isn't a pytest fixture.
func pytestFixtureDecoratorKind(decorator string) (TestKind, bool) {
	d := strings.TrimSpace(decorator)
	// Strip the import-prefix variants we recognize.
	core := ""
	switch {
	case strings.HasPrefix(d, "pytest.fixture"):
		core = strings.TrimPrefix(d, "pytest.fixture")
	case strings.HasPrefix(d, "fixture"):
		core = strings.TrimPrefix(d, "fixture")
	default:
		return TestKindNone, false
	}
	// `core` is either "" (bare `@pytest.fixture`) or "(...)" (with arguments).
	if strings.Contains(core, "autouse=True") {
		return TestKindSetup, true
	}
	return TestKindFixture, true
}

// classifyTestKindPython classifies Python declarations.
//
// Test signals (in priority order):
//  1. Non-test-file: returns (false, TestKindNone). pytest's discovery rules
//     respect filename conventions; we do too.
//  2. Pytest fixture decorator on a decorated_definition: TestKindFixture
//     (autouse=True -> TestKindSetup).
//  3. Symbol name dispatch:
//     setUp/setUpClass     -> TestKindSetup
//     tearDown/tearDownClass -> TestKindTeardown
//     `test_*` prefix      -> TestKindTest
//     else                 -> TestKindHelper
//
// IMPORTANT — `decorated_definition` arrives with chunkType="decorated_definition"
// and name="" because queries_python.go:5 captures it as `(decorated_definition) @decl`
// with NO @name binding. The predicate descends via pythonInnerDef before any
// name-based check.
//
// Doctest detection (`>>>` blocks) is intentionally out of scope (locked Q3) —
// they're emitted as comment chunks, not declaration chunks.
func classifyTestKindPython(
	declNode *sitter.Node,
	src []byte,
	chunkType, name string,
	_ ChunkContext,
	filePath string,
) (bool, TestKind) {
	if !isPythonTestFile(filePath) {
		return false, TestKindNone
	}

	// Decorator-based check first. Operates on the OUTER decorated_definition.
	if chunkType == "decorated_definition" {
		for _, dec := range extractPythonDecoratorNames(declNode, src) {
			if kind, ok := pytestFixtureDecoratorKind(dec); ok {
				return true, kind
			}
		}
	}

	// effectiveName resolves the actual symbol name. For decorated_definition
	// `name` is empty because the @decl capture skips the inner @name binding;
	// descend to the inner function/class def to read its name field.
	effectiveName := name
	if chunkType == "decorated_definition" {
		effectiveName = pythonNameFieldOrEmpty(pythonInnerDef(declNode), src)
	}

	switch effectiveName {
	case "setUp", "setUpClass":
		return true, TestKindSetup
	case "tearDown", "tearDownClass":
		return true, TestKindTeardown
	}
	if strings.HasPrefix(effectiveName, "test_") {
		return true, TestKindTest
	}
	// Methods inside a Test* class without test_ prefix are still helpers
	// (unittest reads test_* prefix, not class membership).
	return true, TestKindHelper
}
