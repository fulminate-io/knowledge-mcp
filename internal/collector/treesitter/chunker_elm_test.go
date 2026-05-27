// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"testing"
)

func TestIsElmTestFile(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"tests/Foo.elm", true},
		{"tests/sub/Foo.elm", true},
		{"src/Main.elm", false},
		{"foo.elm", false},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if got := isElmTestFile(tc.path); got != tc.want {
				t.Errorf("isElmTestFile(%q) = %v; want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestClassifyTestBlockElm(t *testing.T) {
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
			desc: "elm_test_test",
			path: "tests/Foo.elm",
			src: `module Foo exposing (..)
import Test
suite = Test.test "logs in" (\_ -> Expect.equal 1 1)
`,
			want: []expect{{name: "logs in", kind: TestKindTest}},
		},
		{
			desc: "elm_test_describe",
			path: "tests/Foo.elm",
			src: `module Foo exposing (..)
import Test
suite = Test.describe "Auth" []
`,
			want: []expect{{name: "Auth", kind: TestKindTest}},
		},
		{
			desc: "elm_test_fuzz",
			path: "tests/Foo.elm",
			src: `module Foo exposing (..)
import Test
suite = Test.fuzz Fuzz.int "f" (\val -> Expect.equal val val)
`,
			want: []expect{{name: "f", kind: TestKindFuzz}},
		},
		{
			desc:    "non_test_file_drops_chunk",
			path:    "src/Main.elm",
			src:     `suite = Test.test "x" (\_ -> Expect.equal 1 1)`,
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
