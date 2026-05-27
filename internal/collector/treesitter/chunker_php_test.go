// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"testing"
)

// TestClassifyTestKindPHP exercises all three detection paths:
// PHP-8 #[Test] attribute, PHPDoc @test, class-extends-TestCase + name prefix.
func TestClassifyTestKindPHP(t *testing.T) {
	const phpSrc = `<?php
namespace Tests;

use PHPUnit\Framework\TestCase;

class FooTest extends TestCase {
    #[Test]
    public function attributeTest(): void {}

    /** @test */
    public function annotatedTest(): void {}

    /** @dataProvider provideData */
    public function withData(): void {}

    public function testNamePrefix(): void {}

    public function helper(): void {}

    public function setUp(): void {}
    public function tearDown(): void {}
    public function setUpBeforeClass(): void {}
    public function tearDownAfterClass(): void {}
}

class StandaloneTest {
    public function testWeird(): void {}
}
`
	cases := []struct {
		desc     string
		method   string
		wantTest bool
		wantKind TestKind
	}{
		{"PHP-8 #[Test] attribute", "attributeTest", true, TestKindTest},
		{"PHPDoc @test", "annotatedTest", true, TestKindTest},
		{"PHPDoc @dataProvider", "withData", true, TestKindFixture},
		{"name prefix in TestCase subclass", "testNamePrefix", true, TestKindTest},
		{"helper method", "helper", true, TestKindHelper},
		{"setUp", "setUp", true, TestKindSetup},
		{"tearDown", "tearDown", true, TestKindTeardown},
		{"setUpBeforeClass", "setUpBeforeClass", true, TestKindSetup},
		{"tearDownAfterClass", "tearDownAfterClass", true, TestKindTeardown},
		{"name prefix in non-TestCase class → helper", "testWeird", true, TestKindHelper},
	}
	chunker := NewChunker()
	defer chunker.Close()
	res, err := chunker.ChunkFile(context.Background(), "tests/FooTest.php", []byte(phpSrc))
	if err != nil {
		t.Fatalf("ChunkFile: %v", err)
	}
	chunkByName := map[string]*Chunk{}
	for i := range res.Chunks {
		chunkByName[res.Chunks[i].Name] = &res.Chunks[i]
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			ch := chunkByName[tc.method]
			if ch == nil {
				t.Fatalf("method %q not found in chunks", tc.method)
			}
			if ch.IsTest != tc.wantTest {
				t.Errorf("IsTest = %v, want %v", ch.IsTest, tc.wantTest)
			}
			if ch.TestKind != tc.wantKind {
				t.Errorf("TestKind = %q, want %q", ch.TestKind, tc.wantKind)
			}
		})
	}
}

// TestClassifyTestBlockPHP exercises Pest call-style dispatch end-to-end.
func TestClassifyTestBlockPHP(t *testing.T) {
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
			desc: "pest_test",
			path: "tests/FooTest.php",
			src:  `<?php test('works', fn () => null);`,
			want: []expect{{name: "works", kind: TestKindTest}},
		},
		{
			desc: "pest_it",
			path: "tests/FooTest.php",
			src:  `<?php it('foo', fn () => null);`,
			want: []expect{{name: "foo", kind: TestKindTest}},
		},
		{
			desc: "pest_describe",
			path: "tests/FooTest.php",
			src:  `<?php describe('group', function () {});`,
			want: []expect{{name: "group", kind: TestKindTest}},
		},
		{
			desc: "pest_beforeEach",
			path: "tests/FooTest.php",
			src:  `<?php beforeEach(fn () => null);`,
			want: []expect{{name: "", kind: TestKindSetup}},
		},
		{
			desc: "pest_afterAll",
			path: "tests/FooTest.php",
			src:  `<?php afterAll(fn () => null);`,
			want: []expect{{name: "", kind: TestKindTeardown}},
		},
		{
			desc: "pest_dataset",
			path: "tests/FooTest.php",
			src:  `<?php dataset('numbers', [[1], [2]]);`,
			want: []expect{{name: "numbers", kind: TestKindFixture}},
		},
		{
			desc:    "non_test_file_drops_chunk",
			path:    "src/Service.php",
			src:     `<?php test('x', fn () => null);`,
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

// TestClassifyTestKindPHP_NonTestFile verifies non-test files return none.
func TestClassifyTestKindPHP_NonTestFile(t *testing.T) {
	src := `<?php
namespace App;

class Service {
    #[Test]
    public function weird(): void {}
}
`
	chunker := NewChunker()
	defer chunker.Close()
	res, err := chunker.ChunkFile(context.Background(), "src/Service.php", []byte(src))
	if err != nil {
		t.Fatalf("ChunkFile: %v", err)
	}
	for _, ch := range res.Chunks {
		if ch.IsTest {
			t.Errorf("chunk %q (%s) IsTest=true in non-test file; want false", ch.Name, ch.ChunkType)
		}
	}
}
