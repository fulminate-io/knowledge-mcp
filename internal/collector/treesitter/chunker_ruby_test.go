// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"testing"
)

func TestIsRubyTestFile(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"spec/foo_spec.rb", true},
		{"test/foo_test.rb", true},
		{"lib/foo.rb", false},
		{"spec/models/user_spec.rb", true},
		{"app/services/foo.rb", false},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if got := isRubyTestFile(tc.path); got != tc.want {
				t.Errorf("isRubyTestFile(%q) = %v; want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestClassifyTestBlockRuby(t *testing.T) {
	type expect struct {
		name string
		kind TestKind
	}
	cases := []struct {
		desc string
		path string
		src  string
		want []expect
	}{
		{
			desc: "rspec_it",
			path: "spec/foo_spec.rb",
			src: `it "rejects expired" do
end`,
			want: []expect{{name: "rejects expired", kind: TestKindTest}},
		},
		{
			desc: "rspec_describe",
			path: "spec/foo_spec.rb",
			src: `describe "Auth" do
end`,
			want: []expect{{name: "Auth", kind: TestKindTest}},
		},
		{
			desc: "rspec_context",
			path: "spec/foo_spec.rb",
			src: `context "authenticated" do
end`,
			want: []expect{{name: "authenticated", kind: TestKindTest}},
		},
		{
			desc: "rspec_before_each",
			path: "spec/foo_spec.rb",
			src: `before(:each) do
end`,
			want: []expect{{name: "", kind: TestKindSetup}},
		},
		{
			desc: "rspec_after_all",
			path: "spec/foo_spec.rb",
			src: `after(:all) do
end`,
			want: []expect{{name: "", kind: TestKindTeardown}},
		},
		{
			desc: "rspec_let",
			path: "spec/foo_spec.rb",
			src:  `let(:foo) { bar }`,
			want: []expect{{name: "", kind: TestKindFixture}},
		},
		{
			desc: "rspec_subject",
			path: "spec/foo_spec.rb",
			src:  `subject { Service.new }`,
			want: []expect{{name: "", kind: TestKindFixture}},
		},
		{
			desc: "rspec_instance_double",
			path: "spec/foo_spec.rb",
			src:  `instance_double(Foo)`,
			want: []expect{{name: "", kind: TestKindMock}},
		},
		{
			desc: "minitest_block_form",
			path: "test/foo_test.rb",
			src: `test "name" do
end`,
			want: []expect{{name: "name", kind: TestKindTest}},
		},
		{
			desc: "test_unit_setup",
			path: "test/foo_test.rb",
			src:  `setup { }`,
			want: []expect{{name: "", kind: TestKindSetup}},
		},
		{
			desc: "non_test_file_drops_chunk",
			path: "lib/foo.rb",
			src: `it "foo" do
end`,
			want: nil,
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
				if len(blocks) != 0 {
					t.Fatalf("expected 0 test_block chunks (drop branch); got %d: %+v", len(blocks), blocks)
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

// TestClassifyTestKindRuby exercises the Bucket A predicate end-to-end via
// ChunkFile, hitting Minitest::Test + Test::Unit::TestCase superclasses, the
// setup/teardown name dispatch, helper fall-through, and the file-path gate.
// Mirrors TestClassifyTestKindCSharp (chunker_csharp_test.go:9) shape.
func TestClassifyTestKindRuby(t *testing.T) {
	type want struct {
		name string
		kind TestKind
		test bool
	}
	cases := []struct {
		desc string
		path string
		src  string
		want []want
	}{
		{
			desc: "minitest_class_method_form",
			path: "test/foo_test.rb",
			src: `class FooTest < Minitest::Test
  def test_login
    assert_equal 2, 1 + 1
  end
end`,
			want: []want{{"test_login", TestKindTest, true}},
		},
		{
			desc: "test_unit_class_method_form",
			path: "test/foo_test.rb",
			src: `class FooTest < Test::Unit::TestCase
  def test_logout
    assert_equal 2, 1 + 1
  end
end`,
			want: []want{{"test_logout", TestKindTest, true}},
		},
		{
			desc: "setup_teardown_in_minitest_class",
			path: "test/foo_test.rb",
			src: `class FooTest < Minitest::Test
  def setup
    @x = 1
  end
  def teardown
    @x = nil
  end
end`,
			want: []want{
				{"setup", TestKindSetup, true},
				{"teardown", TestKindTeardown, true},
			},
		},
		{
			desc: "helper_method_in_test_class",
			path: "test/foo_test.rb",
			src: `class FooTest < Minitest::Test
  def make_user(name)
    {name: name}
  end
end`,
			want: []want{{"make_user", TestKindHelper, true}},
		},
		{
			desc: "production_path_gated_off",
			path: "lib/foo.rb",
			src: `class Foo
  def test_foo
    true
  end
end`,
			want: []want{{"test_foo", TestKindNone, false}},
		},
	}
	chunker := NewChunker()
	defer chunker.Close()
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			res, err := chunker.ChunkFile(context.Background(), tc.path, []byte(tc.src))
			if err != nil {
				t.Fatalf("ChunkFile: %v", err)
			}
			for _, exp := range tc.want {
				found := false
				for _, c := range res.Chunks {
					if c.ChunkType != "method" || c.Name != exp.name {
						continue
					}
					if c.IsTest != exp.test {
						t.Errorf("[%s] %q IsTest=%v; want %v", tc.desc, exp.name, c.IsTest, exp.test)
					}
					if c.TestKind != exp.kind {
						t.Errorf("[%s] %q TestKind=%q; want %q", tc.desc, exp.name, c.TestKind, exp.kind)
					}
					found = true
					break
				}
				if !found {
					t.Errorf("[%s] expected method chunk %q not found", tc.desc, exp.name)
				}
			}
		})
	}
}
