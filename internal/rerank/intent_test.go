// SPDX-License-Identifier: Apache-2.0

package rerank

import "testing"

func TestClassifyQueryIntent(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  Intent
	}{
		// IntentImpl: substantive query, no test vocabulary, >2 whitespace tokens.
		{"how does retry work", "how does retry work", IntentImpl},
		{"render code for rerank", "render code for rerank", IntentImpl},
		{"implementation of authenticate user", "implementation of authenticate user", IntentImpl},

		// IntentTest: bare-vocab token hits.
		{"how is retry tested", "how is retry tested", IntentTest},
		{"mocking interface", "mocking interface", IntentTest},
		{"fixtures for rankeval", "fixtures for rankeval", IntentTest},

		// T3-A vocabulary additions: describe, context, should.
		{"describe block parser", "describe block parser", IntentTest},
		{"it should reject expired", "it should reject expired", IntentTest},
		{"rspec context nesting", "rspec context nesting", IntentTest},

		// IntentTest: multi-word phrases.
		{"unit test for parser", "unit test for parser", IntentTest},
		{"integration test for store", "integration test for store", IntentTest},

		// IntentTest: framework call-syntax substring.
		{"jest it call syntax", `it("rejects expired", () => {})`, IntentTest},
		{"jest describe block", `describe("auth", () => {})`, IntentTest},
		{"go test call", `test(t, "case")`, IntentTest},

		// IntentTest: conservative-bias trade-off — "process mock setup"
		// contains bare "mock"; classifier accepts the false-positive risk
		// because the worst case (test-intent query routed through impl
		// pipeline, demoting test results) is avoided. Documents the locked
		// Q2 directionality.
		{"process mock setup", "process mock setup", IntentTest},

		// Hyphenated boundary regression guard (locked Q 942bbf13): bare
		// "test" inside "latency-test-server" must NOT trigger. Query has
		// >2 whitespace tokens so the IntentImpl fallback (not IntentUnknown)
		// is the correct expectation when the regression holds.
		{"latency-test-server config setup", "latency-test-server config setup", IntentImpl},

		// IntentUnknown: ≤2 whitespace tokens, no vocab/phrase/syntax hit.
		{"hello world", "Hello World", IntentUnknown},
		{"single token", "process", IntentUnknown},
		{"two-token impl", "auth flow", IntentUnknown},

		// IntentUnknown vs IntentTest tie-break for ≤2 tokens: when ≤2
		// tokens contains a vocab hit, IntentTest still wins (the vocab
		// check runs before the length fallback). "mock embedder" → 2
		// tokens, "mock" matches.
		{"two-token test vocab", "mock embedder", IntentTest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyQueryIntent(tc.query)
			if got != tc.want {
				t.Errorf("ClassifyQueryIntent(%q) = %q; want %q", tc.query, got, tc.want)
			}
		})
	}
}
