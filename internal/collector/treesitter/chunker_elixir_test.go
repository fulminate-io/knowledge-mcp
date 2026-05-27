// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"testing"
)

// TestClassifyTestKindElixir exercises the predicate in isolation.
func TestClassifyTestKindElixir(t *testing.T) {
	cases := []struct {
		desc     string
		path     string
		name     string
		wantTest bool
		wantKind TestKind
	}{
		{"setup → setup", "test/foo_test.exs", "setup", true, TestKindSetup},
		{"setup_all → setup", "test/foo_test.exs", "setup_all", true, TestKindSetup},
		{"defmodule → helper", "test/foo_test.exs", "defmodule", true, TestKindHelper},
		{"def → helper", "test/foo_test.exs", "def", true, TestKindHelper},
		{"test (block) → helper (Bucket B handles via test_block)",
			"test/foo_test.exs", "test", true, TestKindHelper},
		{"describe → helper", "test/foo_test.exs", "describe", true, TestKindHelper},
		{"setup in non-test file → none", "lib/foo.ex", "setup", false, TestKindNone},
		{"def in non-test file → none", "lib/foo.ex", "def", false, TestKindNone},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			gotTest, gotKind := classifyTestKindElixir(nil, nil, "call", tc.name, ChunkContext{}, tc.path)
			if gotTest != tc.wantTest {
				t.Errorf("IsTest = %v, want %v", gotTest, tc.wantTest)
			}
			if gotKind != tc.wantKind {
				t.Errorf("TestKind = %q, want %q", gotKind, tc.wantKind)
			}
		})
	}
}

// TestClassifyTestBlockElixir exercises Bucket B Elixir block-form
// dispatch end-to-end (TestBlocks query → walkTestBlocks → classifyTestBlockElixir).
func TestClassifyTestBlockElixir(t *testing.T) {
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
			desc: "exunit_test_block",
			path: "test/foo_test.exs",
			src: `test "works" do
  assert true
end`,
			want: []expect{{name: "works", kind: TestKindTest}},
		},
		{
			desc: "exunit_describe_block",
			path: "test/foo_test.exs",
			src: `describe "X" do
end`,
			want: []expect{{name: "X", kind: TestKindTest}},
		},
		{
			desc: "exunit_setup_block",
			path: "test/foo_test.exs",
			src: `setup do
  :ok
end`,
			want: []expect{{name: "", kind: TestKindSetup}},
		},
		{
			desc: "exunit_setup_all_block",
			path: "test/foo_test.exs",
			src: `setup_all do
  :ok
end`,
			want: []expect{{name: "", kind: TestKindSetup}},
		},
		{
			desc:    "non_test_file_drops_chunk",
			path:    "lib/foo.ex",
			src:     `test "works" do; assert true; end`,
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

// TestIsElixirTestFile covers filename / path-segment discovery.
func TestIsElixirTestFile(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"test/foo_test.exs", true},
		{"test/foo_test.ex", true},
		{"apps/x/test/y_test.exs", true},
		{"lib/foo.ex", false},
		{"src/foo.exs", false},
	}
	for _, tc := range cases {
		got := isElixirTestFile(tc.path)
		if got != tc.want {
			t.Errorf("isElixirTestFile(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}
