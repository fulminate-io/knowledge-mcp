package llm

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// TestHTTPStatusToTransient asserts the classifier: 429 + 5xx transient,
// everything else terminal.
func TestHTTPStatusToTransient(t *testing.T) {
	cases := []struct {
		status int
		want   bool
	}{
		{200, false},
		{400, false},
		{401, false},
		{404, false},
		{429, true},
		{500, true},
		{502, true},
		{503, true},
		{599, true},
		{600, false},
	}
	for _, tc := range cases {
		if got := HTTPStatusToTransient(tc.status); got != tc.want {
			t.Errorf("HTTPStatusToTransient(%d) = %v, want %v", tc.status, got, tc.want)
		}
	}
}

// TestIsTransient_DefaultsTerminalForBareErrors asserts that bare errors
// and nil default to false (terminal); only *LLMError with Transient=true
// returns true.
func TestIsTransient_DefaultsTerminalForBareErrors(t *testing.T) {
	if IsTransient(nil) {
		t.Error("IsTransient(nil) = true, want false")
	}
	if IsTransient(errors.New("unknown")) {
		t.Error("IsTransient(bare error) = true, want false")
	}
	if !IsTransient(&LLMError{Transient: true, Reason: "http_429"}) {
		t.Error("IsTransient(*LLMError{Transient:true}) = false, want true")
	}
	if IsTransient(&LLMError{Transient: false, Reason: "http_400"}) {
		t.Error("IsTransient(*LLMError{Transient:false}) = true, want false")
	}
}

// TestLLMError_AsTraversesFmtErrorf is the criterion for step 6: errors.As
// pierces fmt.Errorf("%w") wrapping.
func TestLLMError_AsTraversesFmtErrorf(t *testing.T) {
	root := &LLMError{Transient: true, Reason: "http_429", Cause: errors.New("upstream 429")}
	wrapped := fmt.Errorf("provider call: %w", root)

	var got *LLMError
	if !errors.As(wrapped, &got) {
		t.Fatalf("errors.As did not find *LLMError in wrapped error: %v", wrapped)
	}
	if !got.Transient {
		t.Error("got.Transient = false, want true")
	}
	if got.Reason != "http_429" {
		t.Errorf("got.Reason = %q, want http_429", got.Reason)
	}
	if !IsTransient(wrapped) {
		t.Error("IsTransient(wrapped) = false, want true")
	}
}

// TestLLMError_ErrorString formats Reason and Cause cleanly.
func TestLLMError_ErrorString(t *testing.T) {
	withCause := (&LLMError{Reason: "http_429", Cause: errors.New("rate limit")}).Error()
	if withCause != "llm: http_429: rate limit" {
		t.Errorf("with cause = %q", withCause)
	}
	noCause := (&LLMError{Reason: "config"}).Error()
	if noCause != "llm: config" {
		t.Errorf("no cause = %q", noCause)
	}
	var nilErr *LLMError
	if nilErr.Error() != "<nil LLMError>" {
		t.Errorf("nil = %q", nilErr.Error())
	}
}

// TestParseRetryAfter covers both RFC 7231 forms (delay-seconds + HTTP-date),
// plus the absent / unparseable / past-date cases that must yield 0 ("no hint").
func TestParseRetryAfter(t *testing.T) {
	mk := func(v string) http.Header {
		h := http.Header{}
		if v != "" {
			h.Set("Retry-After", v)
		}
		return h
	}
	if got := ParseRetryAfter(mk("30")); got != 30*time.Second {
		t.Errorf("delay-seconds: got %v, want 30s", got)
	}
	if got := ParseRetryAfter(mk("  5 ")); got != 5*time.Second {
		t.Errorf("whitespace-trimmed seconds: got %v, want 5s", got)
	}
	if got := ParseRetryAfter(mk("")); got != 0 {
		t.Errorf("absent: got %v, want 0", got)
	}
	if got := ParseRetryAfter(nil); got != 0 {
		t.Errorf("nil header: got %v, want 0", got)
	}
	if got := ParseRetryAfter(mk("0")); got != 0 {
		t.Errorf("zero seconds: got %v, want 0", got)
	}
	if got := ParseRetryAfter(mk("garbage")); got != 0 {
		t.Errorf("unparseable: got %v, want 0", got)
	}
	// HTTP-date in the past → 0; in the future → positive.
	if got := ParseRetryAfter(mk("Wed, 21 Oct 2015 07:28:00 GMT")); got != 0 {
		t.Errorf("past date: got %v, want 0", got)
	}
	future := time.Now().Add(2 * time.Hour).UTC().Format(http.TimeFormat)
	if got := ParseRetryAfter(mk(future)); got <= 0 {
		t.Errorf("future date: got %v, want > 0", got)
	}
}

// TestRetryAfterOf extracts the delay from an *LLMError (through wrapping) and
// returns 0 for non-LLMError / hint-less errors.
func TestRetryAfterOf(t *testing.T) {
	root := &LLMError{Transient: true, Reason: "http_429", RetryAfter: 9 * time.Second}
	if got := RetryAfterOf(root); got != 9*time.Second {
		t.Errorf("direct: got %v, want 9s", got)
	}
	if got := RetryAfterOf(fmt.Errorf("call: %w", root)); got != 9*time.Second {
		t.Errorf("wrapped: got %v, want 9s", got)
	}
	if got := RetryAfterOf(&LLMError{Transient: true, Reason: "http_429"}); got != 0 {
		t.Errorf("no hint: got %v, want 0", got)
	}
	if got := RetryAfterOf(errors.New("bare")); got != 0 {
		t.Errorf("bare error: got %v, want 0", got)
	}
}

// TestSentinels_AreErrorsIsMatchable confirms ErrProviderNotRegistered and
// ErrInvalidConfig satisfy errors.Is when wrapped.
func TestSentinels_AreErrorsIsMatchable(t *testing.T) {
	wrapped := fmt.Errorf("NewClient: %w", ErrProviderNotRegistered)
	if !errors.Is(wrapped, ErrProviderNotRegistered) {
		t.Error("errors.Is(wrapped, ErrProviderNotRegistered) = false")
	}
	if errors.Is(wrapped, ErrInvalidConfig) {
		t.Error("errors.Is(wrapped, ErrInvalidConfig) = true, want false")
	}
}
