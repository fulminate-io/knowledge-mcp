// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"testing"
)

func TestIsGroovyTestFile(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"FooSpec.groovy", true},
		{"src/test/groovy/FooSpec.groovy", true},
		{"src/test/groovy/Foo.groovy", true},
		{"Foo.groovy", false},
		{"MainSpec.groovy", true},
		{"lib/utils.groovy", false},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if got := isGroovyTestFile(tc.path); got != tc.want {
				t.Errorf("isGroovyTestFile(%q) = %v; want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestClassifyTestKindGroovy(t *testing.T) {
	const specSrc = `class FooSpec extends Specification {
    def setup() { x = 1 }
    def setupSpec() { y = 2 }
    def cleanup() { z = 3 }
    def cleanupSpec() { w = 4 }
    def helperMethod() { return 42 }
}`
	const nonSpecSrc = `class NotASpec {
    def regularFunc() { return 1 }
}`

	cases := []struct {
		desc     string
		path     string
		src      string
		method   string
		wantTest bool
		wantKind TestKind
	}{
		{"setup_in_spec", "FooSpec.groovy", specSrc, "setup", true, TestKindSetup},
		{"setupSpec_in_spec", "FooSpec.groovy", specSrc, "setupSpec", true, TestKindSetup},
		{"cleanup_in_spec", "FooSpec.groovy", specSrc, "cleanup", true, TestKindTeardown},
		{"cleanupSpec_in_spec", "FooSpec.groovy", specSrc, "cleanupSpec", true, TestKindTeardown},
		// T3-C critical: helper methods inside Spec class must be Helper, not Test.
		{"helper_in_spec_is_helper_not_test", "FooSpec.groovy", specSrc, "helperMethod", true, TestKindHelper},
		// Non-Spec class in test file → helper.
		{"non_spec_class_method", "FooSpec.groovy", nonSpecSrc, "regularFunc", true, TestKindHelper},
	}

	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			chunker := NewChunker()
			defer chunker.Close()
			res, err := chunker.ChunkFile(context.Background(), tc.path, []byte(tc.src))
			if err != nil {
				t.Fatalf("ChunkFile: %v", err)
			}
			// The Groovy TopLevel query doesn't bind @name on function_definition,
			// so chunk.Name is empty. Find the chunk whose Content references the
			// expected method name.
			var found *Chunk
			for i := range res.Chunks {
				c := &res.Chunks[i]
				if c.ChunkType != "function_definition" {
					continue
				}
				if !methodNameInContent(c.Content, tc.method) {
					continue
				}
				found = c
				break
			}
			if found == nil {
				t.Fatalf("function_definition for method %q not found in chunks=%+v", tc.method, res.Chunks)
			}
			if found.IsTest != tc.wantTest {
				t.Errorf("IsTest=%v; want %v (content=%q)", found.IsTest, tc.wantTest, found.Content)
			}
			if found.TestKind != tc.wantKind {
				t.Errorf("TestKind=%q; want %q (content=%q)", found.TestKind, tc.wantKind, found.Content)
			}
		})
	}
}

// TestClassifyTestKindGroovy_NonTestFile verifies declarations in non-test
// files are not classified.
func TestClassifyTestKindGroovy_NonTestFile(t *testing.T) {
	src := `class Helper {
    def doStuff() { return 1 }
}`
	chunker := NewChunker()
	defer chunker.Close()
	res, err := chunker.ChunkFile(context.Background(), "lib/Helper.groovy", []byte(src))
	if err != nil {
		t.Fatalf("ChunkFile: %v", err)
	}
	for _, ch := range res.Chunks {
		if ch.IsTest {
			t.Errorf("chunk %q (%s) IsTest=true in non-test file; want false", ch.Name, ch.ChunkType)
		}
	}
}

func methodNameInContent(content, method string) bool {
	// Look for the literal "def <method>" — the Spock idiom for declarations.
	probe := "def " + method
	for i := 0; i <= len(content)-len(probe); i++ {
		if content[i:i+len(probe)] == probe {
			return true
		}
	}
	return false
}
