// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"testing"
)

// TestClassifyTestKindKotlin drives the Kotlin predicate end-to-end. The
// Kotlin tree-sitter grammar emits `modifiers` children with `annotation`
// node entries on function_declaration; the JVM walker handles them.
func TestClassifyTestKindKotlin(t *testing.T) {
	cases := []struct {
		desc     string
		path     string
		src      string
		method   string
		wantTest bool
		wantKind TestKind
	}{
		{
			desc: "JUnit @Test on Kotlin fun",
			path: "src/test/kotlin/com/example/FooTest.kt",
			src: `package com.example
import org.junit.jupiter.api.Test
class FooTest {
	@Test
	fun shouldWork() {}
}
`,
			method:   "shouldWork",
			wantTest: true, wantKind: TestKindTest,
		},
		{
			desc: "JUnit 5 @BeforeEach",
			path: "src/test/kotlin/com/example/SetupTest.kt",
			src: `package com.example
import org.junit.jupiter.api.BeforeEach
class SetupTest {
	@BeforeEach
	fun setUp() {}
}
`,
			method:   "setUp",
			wantTest: true, wantKind: TestKindSetup,
		},
		{
			desc: "no annotation, in test file → helper",
			path: "src/test/kotlin/com/example/HelperTest.kt",
			src: `package com.example
class HelperTest {
	fun plain() {}
}
`,
			method:   "plain",
			wantTest: true, wantKind: TestKindHelper,
		},
		{
			desc: "@Test in non-test file → none",
			path: "src/main/kotlin/com/example/Service.kt",
			src: `package com.example
import org.junit.jupiter.api.Test
class Service {
	@Test
	fun weird() {}
}
`,
			method:   "weird",
			wantTest: false, wantKind: TestKindNone,
		},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			chunker := NewChunker()
			defer chunker.Close()
			res, err := chunker.ChunkFile(context.Background(), tc.path, []byte(tc.src))
			if err != nil {
				t.Fatalf("ChunkFile: %v", err)
			}
			var found *Chunk
			for i := range res.Chunks {
				if res.Chunks[i].Name == tc.method {
					found = &res.Chunks[i]
					break
				}
			}
			if found == nil {
				t.Fatalf("method %q not found", tc.method)
			}
			if found.IsTest != tc.wantTest {
				t.Errorf("IsTest = %v, want %v", found.IsTest, tc.wantTest)
			}
			if found.TestKind != tc.wantKind {
				t.Errorf("TestKind = %q, want %q", found.TestKind, tc.wantKind)
			}
		})
	}
}

// TestClassifyTestBlockKotlin exercises the Bucket B Kotest/Spek call-shape
// dispatch end-to-end (TestBlocks query → walkTestBlocks → classifyTestBlockKotlin).
func TestClassifyTestBlockKotlin(t *testing.T) {
	type expect struct {
		name string
		kind TestKind
	}
	cases := []struct {
		desc    string
		path    string
		src     string
		want    []expect
		wantNum int
	}{
		{
			desc: "kotest_funspec_test",
			path: "src/test/kotlin/FooTest.kt",
			src: `class FooTest : FunSpec({
  test("works") { }
})`,
			want: []expect{{name: "works", kind: TestKindTest}},
		},
		{
			desc: "kotest_describe_it_nested",
			path: "src/test/kotlin/FooTest.kt",
			src: `describe("Auth") {
  it("logs in") { }
}`,
			want: []expect{
				{name: "Auth", kind: TestKindTest},
				{name: "logs in", kind: TestKindTest},
			},
		},
		{
			desc: "kotest_beforeTest",
			path: "src/test/kotlin/FooTest.kt",
			src:  `beforeTest { }`,
			want: []expect{{name: "", kind: TestKindSetup}},
		},
		{
			desc: "kotest_afterTest",
			path: "src/test/kotlin/FooTest.kt",
			src:  `afterTest { }`,
			want: []expect{{name: "", kind: TestKindTeardown}},
		},
		{
			desc: "spek_beforeEachTest",
			path: "src/test/kotlin/FooTest.kt",
			src:  `beforeEachTest { }`,
			want: []expect{{name: "", kind: TestKindSetup}},
		},
		{
			desc: "behaviorspec_given_when_then",
			path: "src/test/kotlin/FooTest.kt",
			src: `given("x") {
  When("y") {
    Then("z") { }
  }
}`,
			want: []expect{
				{name: "x", kind: TestKindTest},
				{name: "y", kind: TestKindTest},
				{name: "z", kind: TestKindTest},
			},
		},
		{
			desc:    "non_test_file_drops_chunk",
			path:    "src/main/kotlin/Foo.kt",
			src:     `test("x") { }`,
			want:    nil,
			wantNum: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			chunker := NewChunker()
			defer chunker.Close()
			res, err := chunker.ChunkFile(context.Background(), tc.path, []byte(tc.src))
			if err != nil {
				t.Fatalf("ChunkFile: %v", err)
			}
			var blocks []Chunk
			for _, c := range res.Chunks {
				if c.ChunkType == "test_block" {
					blocks = append(blocks, c)
				}
			}
			if tc.want == nil {
				if len(blocks) != tc.wantNum {
					t.Fatalf("expected %d test_block chunks; got %d: %+v", tc.wantNum, len(blocks), blocks)
				}
				return
			}
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
					found = true
					break
				}
				if !found {
					t.Errorf("expected chunk Name=%q kind=%q not found; got %v", exp.name, exp.kind, blocks)
				}
			}
		})
	}
}
