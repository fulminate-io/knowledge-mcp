// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"testing"
)

// TestClassifyTestKindSwift exercises XCTestCase-based tests end-to-end.
// `measure { }` blocks are NOT in scope (locked Q4: Bucket B).
func TestClassifyTestKindSwift(t *testing.T) {
	swiftSrc := `import XCTest

class FooTests: XCTestCase {
    override func setUp() {}
    override func setUpWithError() throws {}
    override func tearDown() {}
    override func tearDownWithError() throws {}
    func testWorks() {}
    func helper() {}
}

class NotATestCase {
    func testWeird() {}
}
`
	cases := []struct {
		desc     string
		path     string
		method   string
		wantTest bool
		wantKind TestKind
	}{
		{"setUp in XCTestCase", "FooTests.swift", "setUp", true, TestKindSetup},
		{"setUpWithError in XCTestCase", "FooTests.swift", "setUpWithError", true, TestKindSetup},
		{"tearDown in XCTestCase", "FooTests.swift", "tearDown", true, TestKindTeardown},
		{"tearDownWithError in XCTestCase", "FooTests.swift", "tearDownWithError", true, TestKindTeardown},
		{"test prefix in XCTestCase", "FooTests.swift", "testWorks", true, TestKindTest},
		{"non-test name → helper", "FooTests.swift", "helper", true, TestKindHelper},
		{"test prefix in non-XCTestCase → helper", "FooTests.swift", "testWeird", true, TestKindHelper},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			chunker := NewChunker()
			defer chunker.Close()
			res, err := chunker.ChunkFile(context.Background(), tc.path, []byte(swiftSrc))
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

// TestClassifyTestKindSwift_TestMacro covers Swift Testing's @Test macro.
// The macro decorates a function_declaration; classifyTestKindSwift
// (Bucket A extension) recognizes it BEFORE the test-prefix-in-XCTestCase
// fallback, so arbitrarily-named functions become TestKindTest.
func TestClassifyTestKindSwift_TestMacro(t *testing.T) {
	const src = `import Testing

@Test func arbitraryName() { }

@Test func anotherTest() { }
`
	chunker := NewChunker()
	defer chunker.Close()
	res, err := chunker.ChunkFile(context.Background(), "Tests/FooTests.swift", []byte(src))
	if err != nil {
		t.Fatalf("ChunkFile: %v", err)
	}
	wantTests := map[string]bool{"arbitraryName": false, "anotherTest": false}
	for _, ch := range res.Chunks {
		if _, ok := wantTests[ch.Name]; !ok {
			continue
		}
		if !ch.IsTest {
			t.Errorf("chunk %q IsTest=false; want true (@Test macro)", ch.Name)
		}
		if ch.TestKind != TestKindTest {
			t.Errorf("chunk %q TestKind=%q; want %q", ch.Name, ch.TestKind, TestKindTest)
		}
		wantTests[ch.Name] = true
	}
	for n, found := range wantTests {
		if !found {
			t.Errorf("expected chunk %q not found", n)
		}
	}
}

// TestClassifyTestBlockSwift covers Bucket B measure { ... } detection.
func TestClassifyTestBlockSwift(t *testing.T) {
	t.Run("measure_inside_xctestcase", func(t *testing.T) {
		const src = `import XCTest

class FooTests: XCTestCase {
    func testPerf() {
        measure {
            doSomething()
        }
    }
}`
		chunker := NewChunker()
		defer chunker.Close()
		res, err := chunker.ChunkFile(context.Background(), "Tests/FooTests.swift", []byte(src))
		if err != nil {
			t.Fatalf("ChunkFile: %v", err)
		}
		var found bool
		for _, c := range res.Chunks {
			if c.ChunkType != "test_block" {
				continue
			}
			found = true
			if !c.IsTest {
				t.Errorf("measure test_block IsTest=false; want true")
			}
			if c.TestKind != TestKindBenchmark {
				t.Errorf("measure test_block TestKind=%q; want %q", c.TestKind, TestKindBenchmark)
			}
		}
		if !found {
			t.Errorf("expected measure test_block chunk; got none")
		}
	})

	t.Run("measure_outside_xctestcase_drops_chunk", func(t *testing.T) {
		const src = `class Helper {
    func work() {
        measure { }
    }
}`
		chunker := NewChunker()
		defer chunker.Close()
		res, err := chunker.ChunkFile(context.Background(), "Tests/FooTests.swift", []byte(src))
		if err != nil {
			t.Fatalf("ChunkFile: %v", err)
		}
		for _, c := range res.Chunks {
			if c.ChunkType == "test_block" {
				t.Errorf("expected zero test_block chunks outside XCTestCase; got %+v", c)
			}
		}
	})

	t.Run("measure_in_non_test_file_drops_chunk", func(t *testing.T) {
		const src = `class FooTests: XCTestCase {
    func testPerf() { measure { } }
}`
		chunker := NewChunker()
		defer chunker.Close()
		res, err := chunker.ChunkFile(context.Background(), "Sources/Foo.swift", []byte(src))
		if err != nil {
			t.Fatalf("ChunkFile: %v", err)
		}
		for _, c := range res.Chunks {
			if c.ChunkType == "test_block" {
				t.Errorf("expected zero test_block chunks in non-test file; got %+v", c)
			}
		}
	})
}

// TestClassifyTestKindSwift_NonTestFile verifies the non-test-file gate.
func TestClassifyTestKindSwift_NonTestFile(t *testing.T) {
	src := `import XCTest

class FooTests: XCTestCase {
    func testWorks() {}
}
`
	chunker := NewChunker()
	defer chunker.Close()
	res, err := chunker.ChunkFile(context.Background(), "Sources/Foo.swift", []byte(src))
	if err != nil {
		t.Fatalf("ChunkFile: %v", err)
	}
	for _, ch := range res.Chunks {
		if ch.IsTest {
			t.Errorf("chunk %q (%s) IsTest=true in non-test file; expected false", ch.Name, ch.ChunkType)
		}
	}
}
