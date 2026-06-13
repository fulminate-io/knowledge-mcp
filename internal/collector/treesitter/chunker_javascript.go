// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"path/filepath"
	"slices"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// isJSTestFile recognizes UNAMBIGUOUS JS/TS test signals: filename suffix
// (.test|.spec|.cy).{ts,tsx,js,jsx,mjs,cjs}, segment __tests__/, or the
// Cypress directory layout cypress/{e2e,integration,component}/. Rejects
// generic segments (tests/, test/, e2e-tests/, integration-tests/,
// playwright/) — those collide with real-world non-test directory names.
//
// T3-A discipline: false positives here propagate test_block chunks across
// production code, polluting search reranking. Add a suffix or directory
// only when it is unambiguously test-only.
func isJSTestFile(filePath string) bool {
	base := filepath.Base(filePath)
	suffixes := []string{
		".test.ts", ".test.tsx", ".test.js", ".test.jsx", ".test.mjs", ".test.cjs",
		".spec.ts", ".spec.tsx", ".spec.js", ".spec.jsx", ".spec.mjs", ".spec.cjs",
		// T3-D: Cypress filename convention (lib/auth.cy.ts is unambiguous test).
		".cy.ts", ".cy.tsx", ".cy.js", ".cy.jsx",
	}
	for _, s := range suffixes {
		if strings.HasSuffix(base, s) {
			return true
		}
	}
	segs := strings.Split(filepath.ToSlash(filePath), "/")
	if slices.Contains(segs, "__tests__") {
		return true
	}
	for i := 0; i < len(segs)-1; i++ {
		if segs[i] == "cypress" && (segs[i+1] == "e2e" || segs[i+1] == "integration" || segs[i+1] == "component") {
			return true
		}
	}
	return false
}

// isJSMockFile recognizes mock-only filename conventions: __mocks__/ segment
// and *.mock.{ts,tsx,js,jsx} suffix. Per locked Q4: filename-only (no AST
// signal for vi.mock / jest.mock factory inspection in this pass).
func isJSMockFile(filePath string) bool {
	if slices.Contains(strings.Split(filepath.ToSlash(filePath), "/"), "__mocks__") {
		return true
	}
	base := filepath.Base(filePath)
	for _, ext := range []string{".mock.ts", ".mock.tsx", ".mock.js", ".mock.jsx"} {
		if strings.HasSuffix(base, ext) {
			return true
		}
	}
	return false
}

// classifyTestBlockJS dispatches test_block chunks for JavaScript and
// TypeScript. Reuses the shared callExpressionName helper at
// chunker_identity.go:121 (post-T2-A consolidation handles identifier and
// member_expression directly). Pattern C — `it.each([rows])("name", fn)` —
// has @decl bound to the OUTER call_expression whose function field is
// itself a call_expression; in that case callExpressionName returns "" and
// the classifier-local outer-call unwrap descends one level to read the
// inner member_expression's text ("it.each" / "test.each" / "describe.each").
//
// T2 round-4: chained-single shapes (.skip / .only / .each) normalized via
// suffix-strip so dispatch hits the bare-base switch arm. Playwright
// namespacing (test.describe / test.beforeEach / test.beforeAll /
// test.afterEach / test.afterAll) handled via explicit switch arms (not
// suffix-stripped; the prefix is the test runner namespace).
func classifyTestBlockJS(
	declNode *sitter.Node,
	src []byte,
	_ testBlockCaptures,
	_ ChunkContext,
	filePath string,
) (bool, TestKind) {
	// Mock-file gate first: any captured call in __mocks__/ or *.mock.{ts,...}
	// is classified as TestKindMock regardless of the call shape (locked Q4).
	if isJSMockFile(filePath) {
		return true, TestKindMock
	}
	// Strict-positive gate: outside test files, drop the chunk entirely.
	if !isJSTestFile(filePath) {
		return false, TestKindNone
	}

	fn := callExpressionName(declNode, src)
	// T2 round-5 — Pattern C unwrap. When declNode is the outer call of the
	// `it.each([rows])("name", fn)` form, its `function` field is itself a
	// `call_expression` (NOT identifier or member_expression), so the shared
	// helper returns "". Locally unwrap one level to the inner call and
	// re-extract via the same helper. Keeps the shared helper's contract
	// unchanged (its callers in anonymousFuncName etc. don't see the unwrap).
	if fn == "" {
		if outerFn := declNode.ChildByFieldName("function"); outerFn != nil && outerFn.Type() == "call_expression" {
			fn = callExpressionName(outerFn, src)
		}
	}

	// T2 round-4: strip Mocha/Jest/Vitest chainable modifiers so chained
	// variants dispatch identically to bare counterparts. Order matters
	// only insofar as suffixes are non-overlapping (.skip / .only / .each
	// don't share a prefix). The Playwright namespacing (test.describe /
	// test.beforeEach / etc.) does NOT match these suffixes — those flow
	// through to the explicit switch arms below.
	fn = strings.TrimSuffix(fn, ".skip")
	fn = strings.TrimSuffix(fn, ".only")
	fn = strings.TrimSuffix(fn, ".each")

	switch fn {
	case "it", "test", "specify", "fit", "xit", "xtest":
		return true, TestKindTest
	case "describe", "context", "fdescribe", "xdescribe", "suite":
		return true, TestKindTest
	case "beforeEach", "beforeAll", "before":
		return true, TestKindSetup
	case "afterEach", "afterAll", "after":
		return true, TestKindTeardown
	case "bench":
		return true, TestKindBenchmark
	// Playwright namespaced calls — explicit arms (the prefix is namespace,
	// not chainable modifier).
	case "test.describe":
		return true, TestKindTest
	case "test.beforeEach", "test.beforeAll":
		return true, TestKindSetup
	case "test.afterEach", "test.afterAll":
		return true, TestKindTeardown
	}
	return false, TestKindNone
}

func init() {
	testBlockClassifiers[LangJavaScript] = classifyTestBlockJS
	testBlockClassifiers[LangTypeScript] = classifyTestBlockJS
	// LangTSX shares the JS/TS classifier — .tsx test files (.test.tsx /
	// .spec.tsx) carry identical it()/describe() call shapes to .ts/.tsx.
	testBlockClassifiers[LangTSX] = classifyTestBlockJS
}
