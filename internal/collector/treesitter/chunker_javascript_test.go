// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"strings"
	"testing"
)

// TestIsJSTestFile asserts the T3-A + T3-D filename discipline: only
// unambiguous test signals accept; generic segments (tests/, e2e-tests/,
// playwright/) reject so production code under such directories doesn't
// accidentally produce test_block chunks.
func TestIsJSTestFile(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		// Accept: filename suffix forms.
		{"foo.test.ts", true},
		{"foo.test.tsx", true},
		{"foo.test.js", true},
		{"foo.test.jsx", true},
		{"foo.test.mjs", true},
		{"foo.test.cjs", true},
		{"foo.spec.ts", true},
		{"foo.spec.tsx", true},
		{"foo.spec.js", true},
		{"foo.spec.jsx", true},
		{"foo.spec.mjs", true},
		{"foo.spec.cjs", true},
		// Accept: T3-D Cypress filename suffix.
		{"lib/auth.cy.ts", true},
		{"lib/auth.cy.tsx", true},
		{"lib/auth.cy.js", true},
		{"lib/auth.cy.jsx", true},
		// Accept: __tests__ segment.
		{"__tests__/foo.ts", true},
		{"src/__tests__/foo.tsx", true},
		// Accept: Cypress directory layout.
		{"cypress/e2e/login.cy.ts", true},
		{"cypress/integration/auth.spec.js", true},
		{"cypress/component/widget.test.tsx", true},
		// Reject: generic segments collide with non-test directories.
		{"tests/foo.ts", false},
		{"test/foo.tsx", false},
		{"e2e-tests/foo.ts", false},
		{"integration-tests/auth.tsx", false},
		{"lib/test/foo.ts", false},
		{"playwright/foo.ts", false},
		{"production.ts", false},
		{"lib/utils.ts", false},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if got := isJSTestFile(tc.path); got != tc.want {
				t.Errorf("isJSTestFile(%q) = %v; want %v", tc.path, got, tc.want)
			}
		})
	}
}

// TestIsJSMockFile asserts mock-file detection covers __mocks__ and
// .mock.{ts,tsx,js,jsx} suffix forms.
func TestIsJSMockFile(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"__mocks__/foo.ts", true},
		{"src/__mocks__/foo.tsx", true},
		{"lib/auth.mock.ts", true},
		{"lib/auth.mock.tsx", true},
		{"lib/auth.mock.js", true},
		{"lib/auth.mock.jsx", true},
		// Reject: not a mock file.
		{"foo.test.ts", false},
		{"production.ts", false},
		{"mocks/foo.ts", false},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if got := isJSMockFile(tc.path); got != tc.want {
				t.Errorf("isJSMockFile(%q) = %v; want %v", tc.path, got, tc.want)
			}
		})
	}
}

// TestClassifyTestBlockJS exercises the per-framework dispatch + chained
// variant suffix-strip + Pattern C outer-call unwrap + Playwright
// namespaced explicit arms end-to-end (TestBlocks query → walkTestBlocks
// → emitTestBlockChunk → classifyTestBlockJS). Each case parses a small
// fixture and asserts the test_block chunks produced.
func TestClassifyTestBlockJS(t *testing.T) {
	type expect struct {
		name     string
		kind     TestKind
		contains string // substring chunk.Content must contain (Pattern C verification)
	}
	cases := []struct {
		desc    string
		path    string
		src     string
		want    []expect
		wantNum int // exact test_block count expected
	}{
		// Pattern A — bare identifier, baseline frameworks.
		{
			desc: "jest_it",
			path: "auth.test.ts",
			src:  `it("rejects expired", () => {});`,
			want: []expect{{name: "rejects expired", kind: TestKindTest}},
		},
		{
			desc: "jest_describe",
			path: "auth.test.ts",
			src:  `describe("Group", () => {});`,
			want: []expect{{name: "Group", kind: TestKindTest}},
		},
		{
			desc: "jest_beforeEach",
			path: "auth.test.ts",
			src:  `beforeEach(() => {});`,
			want: []expect{{name: "", kind: TestKindSetup}},
		},
		{
			desc: "jest_afterAll",
			path: "auth.test.ts",
			src:  `afterAll(() => {});`,
			want: []expect{{name: "", kind: TestKindTeardown}},
		},
		{
			desc: "vitest_bench",
			path: "perf.test.ts",
			src:  `bench("perf", () => {});`,
			want: []expect{{name: "perf", kind: TestKindBenchmark}},
		},
		{
			desc: "mocha_specify",
			path: "auth.spec.js",
			src:  `specify("foo", () => {});`,
			want: []expect{{name: "foo", kind: TestKindTest}},
		},
		{
			desc: "jasmine_xit",
			path: "auth.spec.js",
			src:  `xit("skip", () => {});`,
			want: []expect{{name: "skip", kind: TestKindTest}},
		},
		{
			desc: "ava_test",
			path: "auth.test.js",
			src:  `test("foo", t => {});`,
			want: []expect{{name: "foo", kind: TestKindTest}},
		},
		{
			desc: "node_test",
			path: "auth.test.mjs",
			src:  `test("foo", () => {});`,
			want: []expect{{name: "foo", kind: TestKindTest}},
		},
		{
			desc: "bun_it",
			path: "auth.test.ts",
			src:  `it("foo", () => {});`,
			want: []expect{{name: "foo", kind: TestKindTest}},
		},
		{
			desc: "cypress_it_in_e2e_dir",
			path: "cypress/e2e/auth.cy.ts",
			src:  `it("login", () => {});`,
			want: []expect{{name: "login", kind: TestKindTest}},
		},
		{
			desc: "cypress_it_via_cy_suffix_t3d",
			path: "lib/auth.cy.ts",
			src:  `it("login", () => {});`,
			want: []expect{{name: "login", kind: TestKindTest}},
		},
		// Strict-positive gate: no test signal in path → drop.
		{
			desc:    "production_file_drops_chunk",
			path:    "production.ts",
			src:     `it("foo", () => {});`,
			want:    nil,
			wantNum: 0,
		},
		// Mock files: any captured call → TestKindMock (locked Q4).
		{
			desc: "mock_underscore_dir",
			path: "__mocks__/foo.ts",
			src:  `it("foo", () => {});`,
			want: []expect{{name: "foo", kind: TestKindMock}},
		},
		{
			desc: "mock_suffix",
			path: "lib/auth.mock.ts",
			src:  `it("foo", () => {});`,
			want: []expect{{name: "foo", kind: TestKindMock}},
		},
		// Pattern B — chained-single member_expression + suffix-strip.
		{
			desc: "it_skip_chained",
			path: "auth.test.ts",
			src:  `it.skip("foo", () => {});`,
			want: []expect{{name: "foo", kind: TestKindTest}},
		},
		{
			desc: "it_only_chained",
			path: "auth.test.ts",
			src:  `it.only("foo", () => {});`,
			want: []expect{{name: "foo", kind: TestKindTest}},
		},
		{
			desc: "describe_skip_chained",
			path: "auth.test.ts",
			src:  `describe.skip("group", () => {});`,
			want: []expect{{name: "group", kind: TestKindTest}},
		},
		{
			desc: "describe_only_chained",
			path: "auth.test.ts",
			src:  `describe.only("group", () => {});`,
			want: []expect{{name: "group", kind: TestKindTest}},
		},
		{
			desc: "test_skip_chained",
			path: "auth.test.ts",
			src:  `test.skip("foo", () => {});`,
			want: []expect{{name: "foo", kind: TestKindTest}},
		},
		{
			desc: "test_only_chained",
			path: "auth.test.ts",
			src:  `test.only("foo", () => {});`,
			want: []expect{{name: "foo", kind: TestKindTest}},
		},
		// Pattern C — parameterized-double `.each` outer-call unwrap.
		// Chunk content range MUST cover the OUTER call so it includes
		// both the string-literal name and the test body.
		{
			desc: "it_each_parameterized",
			path: "auth.test.ts",
			src:  `it.each([[1]])("foo", (n) => {});`,
			want: []expect{{name: "foo", kind: TestKindTest, contains: `it.each([[1]])("foo"`}},
		},
		{
			desc: "test_each_parameterized",
			path: "auth.test.ts",
			src:  `test.each([[1],[2]])("name %s", (n) => {});`,
			want: []expect{{name: "name %s", kind: TestKindTest, contains: `test.each([[1],[2]])("name %s"`}},
		},
		{
			desc: "describe_each_parameterized",
			path: "auth.test.ts",
			src:  `describe.each([[1]])("group %s", (n) => {});`,
			want: []expect{{name: "group %s", kind: TestKindTest, contains: `describe.each([[1]])("group %s"`}},
		},
		// Playwright namespaced — explicit arms.
		{
			desc: "playwright_test_describe",
			path: "auth.spec.ts",
			src:  `test.describe("group", () => {});`,
			want: []expect{{name: "group", kind: TestKindTest}},
		},
		{
			desc: "playwright_test_beforeEach",
			path: "auth.spec.ts",
			src:  `test.beforeEach(async ({ page }) => {});`,
			want: []expect{{name: "", kind: TestKindSetup}},
		},
		{
			desc: "playwright_test_beforeAll",
			path: "auth.spec.ts",
			src:  `test.beforeAll(async () => {});`,
			want: []expect{{name: "", kind: TestKindSetup}},
		},
		{
			desc: "playwright_test_afterEach",
			path: "auth.spec.ts",
			src:  `test.afterEach(async ({ page }) => {});`,
			want: []expect{{name: "", kind: TestKindTeardown}},
		},
		{
			desc: "playwright_test_afterAll",
			path: "auth.spec.ts",
			src:  `test.afterAll(async () => {});`,
			want: []expect{{name: "", kind: TestKindTeardown}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			chunker := NewChunker()
			defer chunker.Close()

			res, err := chunker.ChunkFile(context.Background(), tc.path, []byte(tc.src))
			if err != nil {
				t.Fatalf("ChunkFile(%q): %v", tc.path, err)
			}

			var blocks []Chunk
			for _, c := range res.Chunks {
				if c.ChunkType == "test_block" {
					blocks = append(blocks, c)
				}
			}

			if tc.wantNum > 0 || tc.want == nil {
				if len(blocks) != tc.wantNum {
					t.Fatalf("expected %d test_block chunks; got %d", tc.wantNum, len(blocks))
				}
				return
			}
			if len(blocks) < len(tc.want) {
				t.Fatalf("expected at least %d test_block chunks; got %d (chunks=%v)",
					len(tc.want), len(blocks), blocks)
			}

			// Each expected chunk must have a matching emitted chunk.
			for _, exp := range tc.want {
				var found bool
				for _, c := range blocks {
					if c.Name != exp.name {
						continue
					}
					if !c.IsTest {
						t.Errorf("chunk %q IsTest=false; want true", c.Name)
					}
					if c.TestKind != exp.kind {
						t.Errorf("chunk %q TestKind=%q; want %q", c.Name, c.TestKind, exp.kind)
					}
					if exp.contains != "" && !strings.Contains(c.Content, exp.contains) {
						t.Errorf("chunk %q content does NOT contain %q (chunk content=%q)",
							c.Name, exp.contains, c.Content)
					}
					found = true
					break
				}
				if !found {
					t.Errorf("expected chunk Name=%q kind=%q not found; got %v",
						exp.name, exp.kind, blocks)
				}
			}
		})
	}
}
