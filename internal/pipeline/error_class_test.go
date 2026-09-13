// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"errors"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// TestShouldAdvanceFallback covers the exported advance predicate the fallback
// summarizer chain consults: it advances (true) on a non-deterministic-terminal
// class (quota/rate-limit via http_429→AuthQuota, transport via
// subprocess_failed→TimeoutTransport) and does NOT advance (false) on a
// deterministic-terminal class (parse via parse_summaries_json→Parse, invalid
// request via http_400→InvalidRequest). It is the single seam over
// classify+IsDeterministicTerminal — no classification is duplicated.
func TestShouldAdvanceFallback(t *testing.T) {
	cases := []struct {
		reason string
		want   bool
	}{
		{"http_429", true},              // AuthQuota — advance
		{"subprocess_failed", true},     // TimeoutTransport — advance
		{"parse_summaries_json", false}, // Parse — deterministic-terminal, no advance
		{"http_400", false},             // InvalidRequest — deterministic-terminal, no advance
	}
	for _, tc := range cases {
		err := &llm.LLMError{Reason: tc.reason}
		if got := ShouldAdvanceFallback(err); got != tc.want {
			t.Errorf("ShouldAdvanceFallback(%q) = %v; want %v", tc.reason, got, tc.want)
		}
	}
}

// TestClassify_OversizeInputIsDeterministicTerminal is R3's cell: the oversize
// reason classifies ClassInvalidRequest, that class IS deterministic-terminal,
// and the fallback chain therefore does NOT advance on it. Advancing would send
// an input NO provider can accept to a second provider, billing a round trip to
// learn what the first one already reported.
//
// The three assertions are separate on purpose: the mapping, the predicate over
// the class, and the predicate over the ERROR are three different functions, and
// a change to any one of them alone would break requirement 3 while the other
// two still read correctly.
func TestClassify_OversizeInputIsDeterministicTerminal(t *testing.T) {
	err := &llm.LLMError{Reason: llm.ReasonInputTooLarge, InputChars: 1608836, MaxInputChars: 1048576}

	if got := classify(err); got != ClassInvalidRequest {
		t.Errorf("classify(Reason=%q) = %v; want ClassInvalidRequest", llm.ReasonInputTooLarge, got)
	}
	if !IsDeterministicTerminal(classify(err)) {
		t.Errorf("IsDeterministicTerminal(classify(%q)) = false; want true — the same input is refused identically on a retry",
			llm.ReasonInputTooLarge)
	}
	if ShouldAdvanceFallback(err) {
		t.Errorf("ShouldAdvanceFallback(%q) = true; want false — no provider can accept an input this size, so advancing bills a round trip to re-learn it",
			llm.ReasonInputTooLarge)
	}

	// CONTROL, in the same run: a class that is NOT deterministic-terminal still
	// advances. Without it an IsDeterministicTerminal that returned true for
	// everything would satisfy every assertion above.
	quota := &llm.LLMError{Reason: "http_429"}
	if !ShouldAdvanceFallback(quota) {
		t.Errorf("control: ShouldAdvanceFallback(http_429) = false; want true — a quota wall may be served by another provider")
	}
}

// TestClassify_HTTP400AlreadyMeetsRequirement3 is R3's HTTP arm (T12), and it is
// a must-NOT-change row rather than a change: no oversize body signature is
// observed for anthropic, openai or gemini, so those transports keep stamping
// http_<status> and an oversize prompt there arrives as http_400 — which already
// classifies deterministic-terminal and already refuses to advance the fallback
// chain. Requirement 3 is therefore met on those transports with no edit at all,
// and this row is what says so out loud instead of leaving a reader to infer it
// from the absence of a change.
//
// A detecting half for those providers would need a documented body signature
// from the pinned version's error vocabulary. Inventing a fixture body would
// assert the matcher against its own answer key, so the arm stays negative until
// a signature is observed and cited.
func TestClassify_HTTP400AlreadyMeetsRequirement3(t *testing.T) {
	err := &llm.LLMError{Transient: false, Reason: "http_400"}
	if got := classify(err); got != ClassInvalidRequest {
		t.Errorf("classify(http_400) = %v; want ClassInvalidRequest (unchanged)", got)
	}
	if !IsDeterministicTerminal(classify(err)) {
		t.Error("IsDeterministicTerminal(classify(http_400)) = false; want true (unchanged)")
	}
	if ShouldAdvanceFallback(err) {
		t.Error("ShouldAdvanceFallback(http_400) = true; want false (unchanged) — an HTTP oversize rejection arrives here and must not advance")
	}
	// The two are DISTINGUISHABLE classes of the same ErrClass: http_400 is not
	// the oversize condition and must not be read as one, because the worker's
	// split reads the condition, not the class.
	if _, _, ok := llm.InputTooLargeOf(err); ok {
		t.Error("InputTooLargeOf(http_400) ok = true; want false — a 400 is not a size refusal and must not trigger a split")
	}
}

// TestClassify_CLIExecKeepsAuthQuotaMapping is R3's must-NOT-change cell (T13).
// claude-cli stamps cli_exec for every non-zero exit and NO oversize signature is
// observed for it — nothing in this tree records what a claude-cli oversize
// rejection looks like — so its mapping stays ClassAuthQuota and its fallback
// advances. The row exists so a later sweep cannot "fix" the mapping to
// deterministic-terminal on the strength of this ticket rather than on evidence:
// that would stop a real quota wall from reaching a second provider.
func TestClassify_CLIExecKeepsAuthQuotaMapping(t *testing.T) {
	err := &llm.LLMError{Reason: "cli_exec"}
	if got := classify(err); got != ClassAuthQuota {
		t.Errorf("classify(cli_exec) = %v; want ClassAuthQuota (unchanged: no oversize signature is observed for claude-cli)", got)
	}
	if !ShouldAdvanceFallback(err) {
		t.Errorf("ShouldAdvanceFallback(cli_exec) = false; want true (unchanged) — a claude-cli exit is a quota/auth family failure another provider may serve")
	}
}

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
		{llm.ReasonInputTooLarge, ClassInvalidRequest},
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
