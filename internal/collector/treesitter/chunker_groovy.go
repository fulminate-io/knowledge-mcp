// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"path/filepath"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// isGroovyTestFile recognizes Spock layout: filename suffix `Spec.groovy`
// (idiomatic Spock — `class FooSpec extends Specification`) and the
// `src/test/groovy/` Maven/Gradle path segment.
func isGroovyTestFile(filePath string) bool {
	base := filepath.Base(filePath)
	if strings.HasSuffix(base, "Spec.groovy") {
		return true
	}
	parts := strings.Split(filepath.ToSlash(filePath), "/")
	for i := 0; i < len(parts)-2; i++ {
		if parts[i] == "src" && parts[i+1] == "test" && parts[i+2] == "groovy" {
			return true
		}
	}
	return false
}

// groovySpecAncestor walks the parent chain looking for a class_definition
// whose name has the `Spec` suffix and whose superclass is `Specification`.
// Matches Spock's idiom — `class FooSpec extends Specification { ... }`.
func groovySpecAncestor(node *sitter.Node, src []byte) bool {
	if node == nil {
		return false
	}
	for p := node.Parent(); p != nil; p = p.Parent() {
		if p.Type() != "class_definition" {
			continue
		}
		nameNode := p.ChildByFieldName("name")
		if nameNode == nil || !strings.HasSuffix(nameNode.Content(src), "Spec") {
			continue
		}
		superNode := p.ChildByFieldName("superclass")
		if superNode != nil && strings.Contains(superNode.Content(src), "Specification") {
			return true
		}
	}
	return false
}

// classifyTestKindGroovy classifies declarations in Spock spec files.
//
// Documented gap (tree-sitter-groovy quirk): Spock test methods declared
// via STRING-LITERAL names — `def "method name"()` — parse as ERROR nodes
// followed by a function_call with a string-literal callee. These don't
// surface as function_definition chunks at all, so this classifier never
// receives them. The Spock test-method detection path lives in Bucket B's
// scope but is OUT-of-scope for this ticket per locked Q10. Setup/cleanup
// hooks declared via `def setup()` / `def cleanup()` (identifier names)
// parse cleanly as function_definition and are classified here.
//
// Logic:
//   - Non-test file → (false, none).
//   - Function inside a `*Spec extends Specification` class:
//   - identifier `setup` / `setupSpec` → setup.
//   - identifier `cleanup` / `cleanupSpec` → teardown.
//   - any other identifier → helper (T3-C: prior version fell through to
//     test, mis-classifying utility methods).
//   - Function outside a Spec class in a test file → helper.
func classifyTestKindGroovy(
	declNode *sitter.Node,
	src []byte,
	chunkType, name string,
	_ ChunkContext,
	filePath string,
) (bool, TestKind) {
	if !isGroovyTestFile(filePath) {
		return false, TestKindNone
	}
	if chunkType == "class_definition" {
		return true, TestKindHelper
	}
	if chunkType != "function_definition" {
		return true, TestKindHelper
	}
	// Read the function name from declNode if it is still missing. Groovy's
	// TopLevel query binds no @name, so this used to be the only source; the
	// declNameResolver below now fills it before the chunk is built and this
	// branch is an unreached backstop rather than the live path. It is kept so
	// the classifier does not depend on the resolver having run.
	if name == "" {
		if fn := declNode.ChildByFieldName("function"); fn != nil && fn.Type() == "identifier" {
			name = fn.Content(src)
		}
	}
	if !groovySpecAncestor(declNode, src) {
		return true, TestKindHelper
	}
	switch name {
	case "setup", "setupSpec":
		return true, TestKindSetup
	case "cleanup", "cleanupSpec":
		return true, TestKindTeardown
	}
	// T3-C: helper methods inside Spec are HELPER, not TEST. The Spock
	// test path (string-literal names) is unreachable from this predicate
	// because tree-sitter-groovy parses those as ERROR.
	return true, TestKindHelper
}

// resolveDeclNameGroovy names the two kinds Groovy's TopLevel query chunks
// without capturing a name. BOTH HALVES ARE NEEDED, and the class half is the
// one that is easy to miss: an unnamed class chunk is skipped by recordSymbol,
// so the parent-to-member CONTAINS edge its methods emit has a FromID that
// resolves to nothing. Groovy members already reach their class through the
// ascent — the missing NAME was the only thing keeping the edge inert.
func resolveDeclNameGroovy(declNode *sitter.Node, src []byte, chunkType string) string {
	switch chunkType {
	case "class_definition":
		return fieldNamed(declNode, src, "name", "identifier")
	case "function_definition":
		return fieldNamed(declNode, src, "function", "identifier")
	}
	return ""
}

func init() {
	testKindClassifiers[LangGroovy] = classifyTestKindGroovy
	declNameResolvers[LangGroovy] = resolveDeclNameGroovy
}
