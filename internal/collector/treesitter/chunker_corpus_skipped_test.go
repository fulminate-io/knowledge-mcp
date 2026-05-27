// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"testing"
)

// Test_SkippedLanguagesReturnFalse asserts that languages without a test
// concept (HTML/CSS/SCSS/Markdown/Dockerfile/Protobuf/SQL/TOML/YAML/CUE/Svelte)
// produce ZERO chunks with IsTest=true on synthetic test-looking source.
//
// The set is hand-maintained per locked Q7. If a future predicate adds e.g.
// SQL test support, remove SQL from this list and add matrix entries instead.
//
// The test asserts a NEGATIVE only — empty chunk-result is a valid pass since
// some of these languages emit no TopLevel matches.
func Test_SkippedLanguagesReturnFalse(t *testing.T) {
	cases := []struct {
		path string
		src  string
	}{
		// HTML — looks test-shaped via custom element + attribute name.
		{"foo.html", `<!DOCTYPE html>
<html><body>
<test name="login" data-spec="should login"><div>foo</div></test>
</body></html>`},
		// CSS — selector and class names mention test/spec.
		{"foo.css", `.test { color: red; }
#spec-target { background: blue; }
.fixture-row { padding: 4px; }`},
		// SCSS — same as CSS but ext to verify SCSS path.
		{"foo.scss", `.test {
  &__bench { color: red; }
  .fixture { padding: 4px; }
}`},
		// Markdown — heading and code fences mention test.
		{"foo.md", `# Test Plan

This is a test.

` + "```" + `
function TestFoo() {}
` + "```"},
		// Dockerfile — RUN commands invoking test runners.
		{"Dockerfile", `FROM golang:1.21
RUN go test ./...
RUN pytest tests/
CMD ["./run-tests.sh"]`},
		// Protobuf — message named TestRequest, service named TestService.
		{"foo.proto", `syntax = "proto3";
package test;

message TestRequest { string name = 1; }
message TestResponse { string result = 1; }

service TestService {
  rpc RunTest(TestRequest) returns (TestResponse);
}`},
		// SQL — queries mention test tables.
		{"foo.sql", `CREATE TABLE tests (id INT, name TEXT);
SELECT * FROM tests WHERE kind = 'test';
INSERT INTO benchmarks VALUES (1, 'speed');`},
		// TOML — table named test.
		{"foo.toml", `[test]
runner = "pytest"

[benchmark]
iterations = 1000`},
		// YAML — keys mention test/benchmark.
		{"foo.yaml", `test:
  command: pytest
  setup: install
benchmark:
  command: go test -bench=.`},
		// CUE — fields mention test.
		{"foo.cue", `test: {
  name: "TestLogin"
  setup: "install"
}
benchmarks: [...string]`},
		// Svelte — script + markup with test-looking content.
		{"foo.svelte", `<script>
  let testName = 'TestLogin';
</script>

<test-component>{testName}</test-component>`},
	}

	chunker := NewChunker()
	defer chunker.Close()

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			lang := DetectLanguage(tc.path)
			if lang == LangUnknown {
				t.Fatalf("DetectLanguage(%q) = LangUnknown; expected a registered language", tc.path)
			}
			res, err := chunker.ChunkFile(context.Background(), tc.path, []byte(tc.src))
			if err != nil {
				t.Fatalf("ChunkFile(%s, %s): %v", lang, tc.path, err)
			}
			for _, ch := range res.Chunks {
				if ch.IsTest {
					t.Errorf("[%s] chunk %q (%s) IsTest=true (TestKind=%q); want IsTest=false (skipped language)",
						lang, ch.Name, ch.ChunkType, ch.TestKind)
				}
			}
		})
	}
}
