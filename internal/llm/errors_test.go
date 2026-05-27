package llm

import (
	"errors"
	"fmt"
	"testing"
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
