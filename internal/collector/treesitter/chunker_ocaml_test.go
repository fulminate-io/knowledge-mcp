// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"testing"
)

func TestIsOCamlTestFile(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"tests/foo.ml", true},
		{"test/foo.ml", true},
		{"foo_test.ml", true},
		{"test_foo.ml", true},
		{"lib/foo.ml", false},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if got := isOCamlTestFile(tc.path); got != tc.want {
				t.Errorf("isOCamlTestFile(%q) = %v; want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestClassifyTestBlockOCaml(t *testing.T) {
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
			desc: "alcotest_test_case",
			path: "tests/foo.ml",
			src:  "let test = Alcotest.test_case \"name\" `Quick (fun () -> ())",
			want: []expect{{name: "name", kind: TestKindTest}},
		},
		{
			desc: "ppx_inline_test_let_percent_test",
			path: "tests/foo.ml",
			src:  "let%test \"name\" = 1 = 1",
			want: []expect{{name: "name", kind: TestKindTest}},
		},
		// T3-D: plain let bindings (no extension) MUST NOT be classified.
		{
			desc:    "plain_let_no_extension_drops_chunk",
			path:    "tests/foo.ml",
			src:     "let foo = 42",
			want:    nil,
			wantNum: 0,
		},
		// Non-test file: even Alcotest call is dropped.
		{
			desc:    "non_test_file_drops_chunk",
			path:    "lib/foo.ml",
			src:     "let test = Alcotest.test_case \"name\" `Quick (fun () -> ())",
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
