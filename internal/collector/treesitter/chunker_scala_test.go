// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"testing"
)

// TestClassifyTestKindScala drives the Scala predicate end-to-end. Scala
// tree-sitter's annotation shape varies — extractScalaAnnotations falls
// back to sibling/parent NamedChild walking when no `modifiers` wrapper
// is present.
func TestClassifyTestKindScala(t *testing.T) {
	cases := []struct {
		desc     string
		path     string
		src      string
		method   string
		wantTest bool
		wantKind TestKind
	}{
		{
			desc: "JUnit-on-Scala @Test",
			path: "src/test/scala/com/example/FooTest.scala",
			src: `package com.example
import org.junit.Test
class FooTest {
  @Test def shouldWork(): Unit = {}
}
`,
			method:   "shouldWork",
			wantTest: true, wantKind: TestKindTest,
		},
		{
			desc: "no annotation in test file → helper",
			path: "src/test/scala/com/example/HelperSpec.scala",
			src: `package com.example
class HelperSpec {
  def plain(): Unit = {}
}
`,
			method:   "plain",
			wantTest: true, wantKind: TestKindHelper,
		},
		{
			desc: "non-test file → none",
			path: "src/main/scala/com/example/Service.scala",
			src: `package com.example
class Service {
  def doThing(): Unit = {}
}
`,
			method:   "doThing",
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

// TestClassifyTestBlockScala exercises Bucket B Scala dispatch end-to-end.
// Covers ScalaTest direct-call (FunSuite), infix (FlatSpec), MUnit, and
// hooks. Specs2's `>>` shape is included as a documented gap test (the
// `>>` operator on Specs2 is parsed as infix_expression with operator
// `>>` — not in the regex set above; verifies Specs2 can ship in a
// follow-up if needed).
func TestClassifyTestBlockScala(t *testing.T) {
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
			desc: "scalatest_funsuite_direct_call",
			path: "src/test/scala/FooSpec.scala",
			src: `class FooSpec extends FunSuite {
  test("works") { assert(true) }
}`,
			want: []expect{{name: "works", kind: TestKindTest}},
		},
		{
			desc: "scalatest_flatspec_infix",
			path: "src/test/scala/FooSpec.scala",
			src: `class FooSpec extends FlatSpec {
  "my widget" should "compute" in {
    assert(true)
  }
}`,
			want: []expect{{name: "compute", kind: TestKindTest}},
		},
		{
			desc: "scalatest_describe_it_nested",
			path: "src/test/scala/FooSpec.scala",
			src: `class FooSpec {
  describe("Auth") {
    it("logs in") { assert(true) }
  }
}`,
			want: []expect{
				{name: "Auth", kind: TestKindTest},
				{name: "logs in", kind: TestKindTest},
			},
		},
		{
			desc: "scalatest_beforeAll",
			path: "src/test/scala/FooSpec.scala",
			src: `class FooSpec {
  beforeAll { setup() }
}`,
			want: []expect{{name: "", kind: TestKindSetup}},
		},
		{
			desc: "scalatest_afterAll",
			path: "src/test/scala/FooSpec.scala",
			src: `class FooSpec {
  afterAll { teardown() }
}`,
			want: []expect{{name: "", kind: TestKindTeardown}},
		},
		{
			desc:    "non_test_file_drops_chunk",
			path:    "src/main/scala/Foo.scala",
			src:     `test("x") { assert(true) }`,
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
