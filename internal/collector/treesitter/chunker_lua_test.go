// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"strings"
	"testing"
)

func TestIsLuaTestFile(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"spec/auth_spec.lua", true},
		{"tests/foo_test.lua", true},
		{"test_foo.lua", true},
		{"production.lua", false},
		{"lib/utils.lua", false},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if got := isLuaTestFile(tc.path); got != tc.want {
				t.Errorf("isLuaTestFile(%q) = %v; want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestClassifyTestKindLua(t *testing.T) {
	type expect struct {
		kind   TestKind
		isTest bool
	}
	// LuaUnit declarations: function_statement chunks. Look up by source-line
	// position to verify (since the existing TopLevel query for Lua doesn't
	// extract @name for function_statement).
	cases := []struct {
		desc      string
		path      string
		src       string
		findTable string
		findMeth  string
		want      expect
	}{
		{
			desc:      "luaunit_test_method",
			path:      "test_foo.lua",
			src:       `TestSuite = {}` + "\n" + `function TestSuite:testFoo() end`,
			findTable: "TestSuite",
			findMeth:  "testFoo",
			want:      expect{kind: TestKindTest, isTest: true},
		},
		{
			desc:      "luaunit_setup",
			path:      "test_foo.lua",
			src:       `TestSuite = {}` + "\n" + `function TestSuite:setUp() end`,
			findTable: "TestSuite",
			findMeth:  "setUp",
			want:      expect{kind: TestKindSetup, isTest: true},
		},
		{
			desc:      "luaunit_teardown",
			path:      "test_foo.lua",
			src:       `TestSuite = {}` + "\n" + `function TestSuite:tearDown() end`,
			findTable: "TestSuite",
			findMeth:  "tearDown",
			want:      expect{kind: TestKindTeardown, isTest: true},
		},
		{
			desc:      "non_test_table_method_helper",
			path:      "test_foo.lua",
			src:       `Foo = {}` + "\n" + `function Foo:doStuff() end`,
			findTable: "Foo",
			findMeth:  "doStuff",
			want:      expect{kind: TestKindHelper, isTest: true},
		},
		{
			desc:      "top_level_helper",
			path:      "test_foo.lua",
			src:       `function helper() end`,
			findTable: "",
			findMeth:  "helper",
			want:      expect{kind: TestKindHelper, isTest: true},
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
			// Find the function_statement chunk whose content references the
			// expected table/method pair.
			var found bool
			for _, c := range res.Chunks {
				if c.ChunkType != "function_statement" {
					continue
				}
				if !containsTableMethod(c.Content, tc.findTable, tc.findMeth) {
					continue
				}
				if c.IsTest != tc.want.isTest {
					t.Errorf("chunk IsTest=%v; want %v (content=%q)", c.IsTest, tc.want.isTest, c.Content)
				}
				if c.TestKind != tc.want.kind {
					t.Errorf("chunk TestKind=%q; want %q (content=%q)", c.TestKind, tc.want.kind, c.Content)
				}
				found = true
				break
			}
			if !found {
				t.Errorf("no function_statement chunk for table=%q method=%q in chunks=%+v",
					tc.findTable, tc.findMeth, res.Chunks)
			}
		})
	}
}

// containsTableMethod returns true when the chunk content references the
// expected (table, method) pair via the `Table:method` form (or just method
// when table is empty).
func containsTableMethod(content, table, method string) bool {
	if table == "" {
		return method != "" && strings.Contains(content, "function") && strings.Contains(content, method)
	}
	return strings.Contains(content, table) && strings.Contains(content, method)
}

func TestClassifyTestBlockLua(t *testing.T) {
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
			desc: "busted_it",
			path: "spec/auth_spec.lua",
			src: `it("logs in", function()
end)`,
			want: []expect{{name: "logs in", kind: TestKindTest}},
		},
		{
			desc: "busted_describe",
			path: "spec/auth_spec.lua",
			src: `describe("Auth", function()
end)`,
			want: []expect{{name: "Auth", kind: TestKindTest}},
		},
		{
			desc: "busted_before_each",
			path: "spec/auth_spec.lua",
			src: `before_each(function()
end)`,
			want: []expect{{name: "", kind: TestKindSetup}},
		},
		{
			desc: "busted_after_each",
			path: "spec/auth_spec.lua",
			src: `after_each(function()
end)`,
			want: []expect{{name: "", kind: TestKindTeardown}},
		},
		{
			desc: "busted_pending_helper",
			path: "spec/foo_spec.lua",
			src: `pending("WIP", function()
end)`,
			want: []expect{{name: "WIP", kind: TestKindHelper}},
		},
		{
			desc:    "non_test_file_drops_chunk",
			path:    "production.lua",
			src:     `it("foo", function() end)`,
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
