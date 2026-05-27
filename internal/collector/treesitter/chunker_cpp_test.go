// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"testing"
)

func TestIsCppTestFile(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"foo_test.cpp", true},
		{"foo_test.cc", true},
		{"foo_test.cxx", true},
		{"foo_test.c", true},
		{"foo_unittest.cpp", true},
		{"test_foo.c", true},
		{"test_foo.cpp", true},
		{"tests/foo.cpp", false},
		{"tests/foo_test.cpp", true},
		{"lib/foo.cpp", false},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if got := isCppTestFile(tc.path); got != tc.want {
				t.Errorf("isCppTestFile(%q) = %v; want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestIsCppMockFile(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"mock_foo.cc", true},
		{"mock_foo.cpp", true},
		{"foo_mock.cpp", true},
		{"foo_mock.h", true},
		{"mocks/foo.cc", true},
		{"__mocks__/foo.h", true},
		{"production.cpp", false},
		{"foo_test.cpp", false},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if got := isCppMockFile(tc.path); got != tc.want {
				t.Errorf("isCppMockFile(%q) = %v; want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestClassifyTestBlockCpp(t *testing.T) {
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
			desc: "gtest_TEST_cpp",
			path: "math_test.cpp",
			src: `TEST(MathTest, AddIntegers) {
  EXPECT_EQ(1 + 1, 2);
}`,
			want: []expect{{name: "AddIntegers", kind: TestKindTest}},
		},
		{
			desc: "gtest_TEST_F_cpp",
			path: "fixture_test.cpp",
			src:  `TEST_F(FixtureTest, Method) {}`,
			want: []expect{{name: "Method", kind: TestKindTest}},
		},
		{
			desc: "gtest_TEST_c",
			path: "math_test.c",
			src: `TEST(MathTest, AddIntegers) {
  EXPECT_EQ(1 + 1, 2);
}`,
			want: []expect{{name: "AddIntegers", kind: TestKindTest}},
		},
		{
			desc: "catch2_TEST_CASE_cpp",
			path: "spec_test.cpp",
			src:  `TEST_CASE("rejects expired", "[auth]") { CHECK(true); }`,
			want: []expect{{name: "rejects expired", kind: TestKindTest}},
		},
		{
			desc: "boost_test_BOOST_AUTO_TEST_CASE",
			path: "auth_test.cpp",
			src:  `BOOST_AUTO_TEST_CASE(MyCase) {}`,
			want: []expect{{name: "MyCase", kind: TestKindTest}},
		},
		{
			desc: "google_benchmark",
			path: "perf_test.cpp",
			src:  `BENCHMARK(BM_Foo);`,
			want: []expect{{name: "BM_Foo", kind: TestKindBenchmark}},
		},
		{
			desc:    "TEST_in_production_drops_chunk",
			path:    "production.cpp",
			src:     `TEST(MathTest, AddIntegers) {}`,
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

func TestClassifyTestKindCpp(t *testing.T) {
	type expect struct {
		want bool
		kind TestKind
	}
	cases := []struct {
		path string
		src  string
		want expect
	}{
		// Mock file: any function classified as TestKindMock.
		{"mock_foo.cc", `class Foo { int helper() { return 0; } };`, expect{true, TestKindMock}},
		{"foo_mock.cpp", `int foo() { return 0; }`, expect{true, TestKindMock}},
		{"mocks/foo.cc", `int foo() { return 0; }`, expect{true, TestKindMock}},
		// Test file (non-mock): functions classified as TestKindHelper unless
		// they match a TestBlocks macro (covered separately above).
		{"foo_test.cpp", `int helper() { return 0; }`, expect{true, TestKindHelper}},
		{"test_foo.c", `int helper() { return 0; }`, expect{true, TestKindHelper}},
		// Non-test, non-mock: no classification.
		{"production.cpp", `int foo() { return 0; }`, expect{false, TestKindNone}},
		{"lib/foo.h", `int foo();`, expect{false, TestKindNone}},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			chunker := NewChunker()
			defer chunker.Close()
			res, err := chunker.ChunkFile(context.Background(), tc.path, []byte(tc.src))
			if err != nil {
				t.Fatalf("ChunkFile: %v", err)
			}
			for _, c := range res.Chunks {
				if c.ChunkType == "test_block" {
					continue
				}
				if c.IsTest != tc.want.want {
					t.Errorf("chunk %q IsTest=%v; want %v", c.Name, c.IsTest, tc.want.want)
				}
				if c.TestKind != tc.want.kind {
					t.Errorf("chunk %q TestKind=%q; want %q", c.Name, c.TestKind, tc.want.kind)
				}
			}
		})
	}
}
