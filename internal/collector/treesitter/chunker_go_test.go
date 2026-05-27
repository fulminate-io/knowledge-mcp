// SPDX-License-Identifier: Apache-2.0

package treesitter

import "testing"

// TestClassifyTestKindGo exercises the filename + symbol-name dispatch for
// the Go predicate. fileCtx and declNode are unused — Go's signals are
// path + name only — so we pass nil/zero values.
func TestClassifyTestKindGo(t *testing.T) {
	cases := []struct {
		desc      string
		filePath  string
		chunkType string
		name      string
		wantTest  bool
		wantKind  TestKind
	}{
		// _test.go file with the canonical prefixes.
		{"Test prefix in _test.go", "foo_test.go", "function_declaration", "TestFoo",
			true, TestKindTest},
		{"Test_ prefix (underscore boundary)", "foo_test.go", "function_declaration", "Test_Foo",
			true, TestKindTest},
		{"Benchmark prefix", "foo_test.go", "function_declaration", "BenchmarkBar",
			true, TestKindBenchmark},
		{"Example prefix bare", "foo_test.go", "function_declaration", "Example",
			true, TestKindExample},
		{"Example prefix with name", "foo_test.go", "function_declaration", "ExampleQux",
			true, TestKindExample},
		{"Fuzz prefix", "foo_test.go", "function_declaration", "FuzzZot",
			true, TestKindFuzz},
		{"TestMain → setup", "foo_test.go", "function_declaration", "TestMain",
			true, TestKindSetup},
		// _test.go file with a non-prefix name -> helper.
		{"helper in _test.go", "foo_test.go", "function_declaration", "newFixture",
			true, TestKindHelper},
		// Lowercase letter after Test/Benchmark/Fuzz disqualifies the prefix
		// (matches stdlib `testing` lookup rules: TestifyMe is NOT a test).
		{"Testify (lowercase after Test) → helper", "foo_test.go", "function_declaration", "Testify",
			true, TestKindHelper},
		// Non-test file: no classification.
		{"non-test file → none", "foo.go", "function_declaration", "TestFoo",
			false, TestKindNone},
		// Non-function decl in _test.go → helper (e.g. helper type).
		{"type in _test.go → helper", "foo_test.go", "type_declaration", "fakeStore",
			true, TestKindHelper},
	}

	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			gotTest, gotKind := classifyTestKindGo(nil, nil, tc.chunkType, tc.name, ChunkContext{}, tc.filePath)
			if gotTest != tc.wantTest {
				t.Errorf("IsTest = %v, want %v", gotTest, tc.wantTest)
			}
			if gotKind != tc.wantKind {
				t.Errorf("TestKind = %q, want %q", gotKind, tc.wantKind)
			}
		})
	}
}

// TestClassifyTestKindGo_MockFiles verifies the mock-filename branch fires
// regardless of name; mock files classify as TestKindMock.
func TestClassifyTestKindGo_MockFiles(t *testing.T) {
	cases := []struct {
		desc     string
		filePath string
	}{
		{"mock_ prefix", "mock_store.go"},
		{"_mock.go suffix", "store_mock.go"},
		{"mocks/ directory segment", "internal/mocks/store.go"},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			gotTest, gotKind := classifyTestKindGo(nil, nil, "function_declaration", "Foo", ChunkContext{}, tc.filePath)
			if !gotTest || gotKind != TestKindMock {
				t.Errorf("IsTest=%v Kind=%q, want true/%q", gotTest, gotKind, TestKindMock)
			}
		})
	}
}

// TestExtendFrameworksGo verifies the framework-extender appends
// FrameworkGoTesting on _test.go files and is a no-op otherwise.
func TestExtendFrameworksGo(t *testing.T) {
	cases := []struct {
		desc     string
		filePath string
		input    []Framework
		want     []Framework
	}{
		{"empty input on test file → adds GoTesting",
			"foo_test.go", nil, []Framework{FrameworkGoTesting}},
		{"non-test file → unchanged nil",
			"foo.go", nil, nil},
		{"preserves existing input on test file",
			"foo_test.go",
			[]Framework{FrameworkJSJest},
			[]Framework{FrameworkJSJest, FrameworkGoTesting}},
		{"preserves existing input on non-test file",
			"foo.go",
			[]Framework{FrameworkJSJest},
			[]Framework{FrameworkJSJest}},
	}

	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			got := extendFrameworksGo(nil, nil, tc.filePath, tc.input)
			if !frameworksEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
