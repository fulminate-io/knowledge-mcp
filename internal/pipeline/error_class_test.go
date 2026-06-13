// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"errors"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// TestClassify_MapsRealReasonVocabulary is the EXHAUSTIVE antidote to silent
// ClassOther fallthrough: it enumerates every live LLMError.Reason literal found
// by the grep census over internal/llm + internal/llmproviders + internal/embed.
// If a new provider reason appears later, classify must gain a case AND this
// table must gain a row — a new reason silently bucketing to ClassOther is the
// exact failure this test exists to catch.
func TestClassify_MapsRealReasonVocabulary(t *testing.T) {
	cases := []struct {
		reason string
		want   ErrClass
	}{
		// Parse family.
		{"parse_summaries_json", ClassParse},
		{"parse_response", ClassParse},
		{"parse_cli_response", ClassParse},
		{"decode_response", ClassParse},
		{"empty_structured_output", ClassParse},
		{"no_choices", ClassParse},
		{"no_candidates", ClassParse},
		{"prompt_blocked", ClassParse},
		// Truncation.
		{"response_truncated", ClassTruncation},
		// Auth/quota.
		{"http_429", ClassAuthQuota},
		{"http_401", ClassAuthQuota},
		{"http_403", ClassAuthQuota},
		{"cli_exec", ClassAuthQuota},
		{"cli_response_error", ClassAuthQuota},
		// Timeout/transport.
		{"http_500", ClassTimeoutTransport},
		{"network", ClassTimeoutTransport},
		{"read_response", ClassTimeoutTransport},
		{"cli_deadline", ClassTimeoutTransport},
		{"subprocess_timeout", ClassTimeoutTransport},
		{"subprocess_failed", ClassTimeoutTransport},
		{"subprocess_error", ClassTimeoutTransport},
		// Invalid request.
		{"http_400", ClassInvalidRequest},
		{"http_404", ClassInvalidRequest},
		{"http_422", ClassInvalidRequest},
		// Other (each KNOWN reason is a deliberate ClassOther, not a fallthrough).
		{"config", ClassOther},
		{"marshal_request", ClassOther},
		{"create_request", ClassOther},
		{"translate_request", ClassOther},
		{"build_request", ClassOther},
		{"subprocess_setup", ClassOther},
		{"cli_not_found", ClassOther},
		{"turn_failed", ClassOther},
		{"openai_api_error", ClassOther},
		{"summarize_generate", ClassOther},
	}
	for _, tc := range cases {
		t.Run(tc.reason, func(t *testing.T) {
			got := classify(&llm.LLMError{Reason: tc.reason})
			if got != tc.want {
				t.Fatalf("classify(Reason=%q) = %v, want %v", tc.reason, got, tc.want)
			}
		})
	}

	// Non-LLMError shapes both bucket to ClassOther.
	t.Run("bare_error", func(t *testing.T) {
		if got := classify(errors.New("boom")); got != ClassOther {
			t.Fatalf("classify(bare error) = %v, want ClassOther", got)
		}
	})
	t.Run("nil_error", func(t *testing.T) {
		if got := classify(nil); got != ClassOther {
			t.Fatalf("classify(nil) = %v, want ClassOther", got)
		}
	})
	// An http_ reason with an unparseable / unknown status still buckets safely.
	t.Run("http_unparseable", func(t *testing.T) {
		if got := classify(&llm.LLMError{Reason: "http_teapot"}); got != ClassOther {
			t.Fatalf("classify(http_teapot) = %v, want ClassOther", got)
		}
	})
	t.Run("http_418_other_4xx", func(t *testing.T) {
		// A 4xx that is not auth/quota is a client-request fault -> InvalidRequest.
		if got := classify(&llm.LLMError{Reason: "http_418"}); got != ClassInvalidRequest {
			t.Fatalf("classify(http_418) = %v, want ClassInvalidRequest", got)
		}
	})
}

// TestIsDeterministicTerminal pins the predicate this package owns for the
// downstream fail-fast consumer: parse / invalid-request / truncation reproduce
// identically for the same batch + config; the rest may clear on retry.
func TestIsDeterministicTerminal(t *testing.T) {
	deterministic := []ErrClass{ClassParse, ClassInvalidRequest, ClassTruncation}
	for _, c := range deterministic {
		if !IsDeterministicTerminal(c) {
			t.Fatalf("IsDeterministicTerminal(%v) = false, want true", c)
		}
	}
	nonDeterministic := []ErrClass{ClassAuthQuota, ClassTimeoutTransport, ClassOther}
	for _, c := range nonDeterministic {
		if IsDeterministicTerminal(c) {
			t.Fatalf("IsDeterministicTerminal(%v) = true, want false", c)
		}
	}
}
