// SPDX-License-Identifier: Apache-2.0

package rerank

import "strings"

// Intent classifies the inferred shape of a user search query so the
// default rerank pipeline can bias towards production code (impl-intent)
// or towards test code (test-intent). Unknown is the conservative fallback
// for very short queries where token heuristics are noisy.
type Intent string

const (
	IntentImpl    Intent = "impl"
	IntentTest    Intent = "test"
	IntentUnknown Intent = "unknown"
)

// testIntentTokens lists query tokens that, when present at WHITESPACE
// boundaries in the query, indicate the caller is asking about tests.
// Lookup is via map (order irrelevant). Hyphenated compounds like
// "latency-test-server" remain a single whitespace-bounded token and
// therefore do NOT match bare "test" — matching the locked Q 942bbf13
// resolution.
var testIntentTokens = map[string]struct{}{
	"test": {}, "tests": {}, "tested": {}, "testing": {},
	"spec": {}, "specs": {},
	"mock": {}, "mocks": {}, "mocked": {}, "mocking": {},
	"fixture": {}, "fixtures": {},
	"assertion": {}, "assertions": {}, "assert": {}, "asserts": {},
	"expect": {}, "expects": {}, "expected": {},
	"coverage": {},
	"describe": {}, "context": {}, "should": {},
}

// testFrameworkSubstrings catches test-framework call syntax that does
// not survive whitespace tokenization (parens/camelCase). Matched as
// substrings on the lowercase whole query.
var testFrameworkSubstrings = []string{
	"it(", "describe(", "test(",
	"beforeeach", "aftereach", "beforeall", "afterall",
}

// multiWordTestPhrases catches phrasing patterns the single-token list
// can't. Matched as substrings on the lowercase whole query.
var multiWordTestPhrases = []string{
	"unit test", "integration test", "e2e test", "end-to-end test",
}

// ClassifyQueryIntent returns the inferred intent for a user search
// query using a three-stage check: (1) substring scan for
// testFrameworkSubstrings on the lowercase whole query, (2) substring scan
// for multiWordTestPhrases on the lowercase whole query, (3) whitespace-
// bounded token match against testIntentTokens. Any match returns
// IntentTest. Otherwise IntentUnknown (≤2 whitespace tokens) or IntentImpl.
//
// CONSERVATIVE BIAS (locked Q2): false-negatives prefer IntentImpl. The
// classifier returns IntentTest only on explicit test vocabulary, framework
// call syntax, or unambiguous multi-word phrasing. Hyphenated compound
// forms (e.g. "latency-test-server") are preserved as single tokens by
// strings.Fields and therefore do not match bare "test".
func ClassifyQueryIntent(query string) Intent {
	lower := strings.ToLower(query)
	for _, sub := range testFrameworkSubstrings {
		if strings.Contains(lower, sub) {
			return IntentTest
		}
	}
	for _, phrase := range multiWordTestPhrases {
		if strings.Contains(lower, phrase) {
			return IntentTest
		}
	}
	tokens := strings.Fields(query)
	for _, tok := range tokens {
		if _, ok := testIntentTokens[strings.ToLower(tok)]; ok {
			return IntentTest
		}
	}
	if len(tokens) <= 2 {
		return IntentUnknown
	}
	return IntentImpl
}
