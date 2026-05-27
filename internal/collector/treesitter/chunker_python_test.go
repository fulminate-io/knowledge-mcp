// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"testing"
)

// contains is a thin local wrapper so we don't import strings here.
func contains(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestIsPythonTestFile covers the filename / path-segment discovery rules.
func TestIsPythonTestFile(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"tests/test_foo.py", true},
		{"foo/test_bar.py", true},
		{"foo/bar_test.py", true},
		{"src/conftest.py", true},
		{"my/test/foo.py", true},
		{"my/tests/helpers.py", true},
		{"src/foo.py", false},
		{"app/main.py", false},
	}
	for _, tc := range cases {
		got := isPythonTestFile(tc.path)
		if got != tc.want {
			t.Errorf("isPythonTestFile(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// TestPytestFixtureDecoratorKind exercises the autouse vs plain fixture branch.
func TestPytestFixtureDecoratorKind(t *testing.T) {
	cases := []struct {
		dec     string
		wantOK  bool
		wantKnd TestKind
	}{
		{"pytest.fixture", true, TestKindFixture},
		{"pytest.fixture()", true, TestKindFixture},
		{"pytest.fixture(autouse=True)", true, TestKindSetup},
		{"pytest.fixture(scope='session', autouse=True)", true, TestKindSetup},
		{"fixture", true, TestKindFixture},
		{"fixture(autouse=True)", true, TestKindSetup},
		{"pytest.mark.parametrize('x', [1, 2])", false, TestKindNone},
		{"unrelated_decorator", false, TestKindNone},
	}
	for _, tc := range cases {
		gotKnd, gotOK := pytestFixtureDecoratorKind(tc.dec)
		if gotOK != tc.wantOK || gotKnd != tc.wantKnd {
			t.Errorf("decorator %q: got (%q, %v), want (%q, %v)",
				tc.dec, gotKnd, gotOK, tc.wantKnd, tc.wantOK)
		}
	}
}

// TestClassifyTestKindPython_NameDispatch exercises the bare (non-decorated)
// declaration shape — function_definition / class_definition / method
// chunks come through with chunkType=function_definition and the @name
// binding populated.
func TestClassifyTestKindPython_NameDispatch(t *testing.T) {
	cases := []struct {
		desc      string
		filePath  string
		chunkType string
		name      string
		wantTest  bool
		wantKind  TestKind
	}{
		{"non-test file → none", "src/foo.py", "function_definition", "test_thing",
			false, TestKindNone},
		{"test_ prefix in test file → test", "tests/test_foo.py", "function_definition", "test_thing",
			true, TestKindTest},
		{"setUp → setup", "tests/test_foo.py", "function_definition", "setUp",
			true, TestKindSetup},
		{"setUpClass → setup", "tests/test_foo.py", "function_definition", "setUpClass",
			true, TestKindSetup},
		{"tearDown → teardown", "tests/test_foo.py", "function_definition", "tearDown",
			true, TestKindTeardown},
		{"tearDownClass → teardown", "tests/test_foo.py", "function_definition", "tearDownClass",
			true, TestKindTeardown},
		{"helper in test file", "tests/test_foo.py", "function_definition", "_make_widget",
			true, TestKindHelper},
		{"class in test file → helper", "tests/test_foo.py", "class_definition", "TestThings",
			true, TestKindHelper},
		{"conftest helper → helper", "tests/conftest.py", "function_definition", "_factory",
			true, TestKindHelper},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			gotTest, gotKind := classifyTestKindPython(nil, nil, tc.chunkType, tc.name, ChunkContext{}, tc.filePath)
			if gotTest != tc.wantTest {
				t.Errorf("IsTest = %v, want %v", gotTest, tc.wantTest)
			}
			if gotKind != tc.wantKind {
				t.Errorf("TestKind = %q, want %q", gotKind, tc.wantKind)
			}
		})
	}
}

// TestClassifyTestKindPython_DecoratedDefinition exercises the
// decorated_definition descent end-to-end via ChunkFile. The @decl capture
// for decorated functions arrives with chunkType="decorated_definition" and
// name="" — the predicate descends to the inner function_definition and
// reads its name field. Also exercises pytest fixture decorators.
func TestClassifyTestKindPython_DecoratedDefinition(t *testing.T) {
	src := `
import pytest

@pytest.fixture
def my_fixture():
    return 1

@pytest.fixture(autouse=True)
def auto_fixture():
    pass

@pytest.mark.parametrize("x", [1, 2])
def test_param(x):
    assert x

@pytest.fixture
def setUp_fixture():
    pass
`
	chunker := NewChunker()
	defer chunker.Close()
	res, err := chunker.ChunkFile(context.Background(), "tests/test_decor.py", []byte(src))
	if err != nil {
		t.Fatalf("ChunkFile: %v", err)
	}

	// Find chunks by inspecting their content.
	type expectation struct {
		hint     string
		isTest   bool
		testKind TestKind
	}
	expects := []expectation{
		{"def my_fixture()", true, TestKindFixture},
		{"def auto_fixture()", true, TestKindSetup},
		{"def test_param(x)", true, TestKindTest},
	}

	for _, e := range expects {
		found := false
		for _, ch := range res.Chunks {
			if !contains(ch.Content, e.hint) {
				continue
			}
			// Pick the OUTER decorated_definition chunk (it carries the
			// @decl-binding from queries_python.go).
			if ch.ChunkType != "decorated_definition" && ch.ChunkType != "function_definition" {
				continue
			}
			found = true
			if ch.IsTest != e.isTest || ch.TestKind != e.testKind {
				t.Errorf("chunk %q: IsTest=%v Kind=%q, want %v/%q",
					e.hint, ch.IsTest, ch.TestKind, e.isTest, e.testKind)
			}
			break
		}
		if !found {
			t.Errorf("no chunk found containing %q", e.hint)
		}
	}
}

// TestPythonInnerDef exercises the descent helper directly.
func TestPythonInnerDef(t *testing.T) {
	src := `
@some_decorator
def inner_func():
    pass
`
	chunker := NewChunker()
	defer chunker.Close()
	res, err := chunker.ChunkFile(context.Background(), "tests/test_inner.py", []byte(src))
	if err != nil {
		t.Fatalf("ChunkFile: %v", err)
	}
	// At least one chunk should be the decorated_definition. Verify the
	// outer @decl chunk has Name="" (no @name binding) and the predicate
	// classifies it as helper (name doesn't match test_*/setUp/tearDown).
	foundDecorated := false
	for _, ch := range res.Chunks {
		if ch.ChunkType == "decorated_definition" {
			foundDecorated = true
			if ch.IsTest != true || ch.TestKind != TestKindHelper {
				t.Errorf("decorated chunk: IsTest=%v Kind=%q, want true/%q",
					ch.IsTest, ch.TestKind, TestKindHelper)
			}
		}
	}
	if !foundDecorated {
		t.Error("expected a decorated_definition chunk; none found")
	}
}
