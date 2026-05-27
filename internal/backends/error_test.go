// SPDX-License-Identifier: Apache-2.0

package backends

import (
	"errors"
	"fmt"
	"testing"
)

// TestError_UnwrapAndAsTraversesFmtErrorf ensures errors.As pierces
// fmt.Errorf("%w") wrapping — the runPush layer wraps adapter errors
// through "push %s: %w" and "batch %d: %w" patterns. Mirrors
// domains/store/llm_errors_test.go:59.
func TestError_UnwrapAndAsTraversesFmtErrorf(t *testing.T) {
	root := &Error{Transient: true, Reason: ReasonRateLimited, Cause: errors.New("linear: 429")}
	wrapped := fmt.Errorf("push abc-1: %w", root)

	var got *Error
	if !errors.As(wrapped, &got) {
		t.Fatalf("errors.As did not find *Error in wrapped: %v", wrapped)
	}
	if got.Transient != true {
		t.Errorf("got.Transient = false, want true")
	}
	if got.Reason != ReasonRateLimited {
		t.Errorf("got.Reason = %q, want %q", got.Reason, ReasonRateLimited)
	}

	// IsTransient should also pierce the wrap.
	if !IsTransient(wrapped) {
		t.Error("IsTransient(wrapped) = false, want true")
	}
}

// TestError_NilSafe asserts (*Error)(nil).Error() and (*Error)(nil).Unwrap()
// don't panic. Mirrors llm.LLMError nil-safety.
func TestError_NilSafe(t *testing.T) {
	var nilErr *Error
	if got := nilErr.Error(); got != "<nil backends.Error>" {
		t.Errorf("(*Error)(nil).Error() = %q, want %q", got, "<nil backends.Error>")
	}
	if got := nilErr.Unwrap(); got != nil {
		t.Errorf("(*Error)(nil).Unwrap() = %v, want nil", got)
	}
}

// TestIsTransient_BareErrIsFalse asserts that bare errors and non-*Error
// types default to false. Exercises the client-local backends.IsTransient
// predicate (cmd/knowledge/internal/backends/error.go).
func TestIsTransient_BareErrIsFalse(t *testing.T) {
	if IsTransient(nil) {
		t.Error("IsTransient(nil) = true, want false")
	}
	if IsTransient(errors.New("unknown")) {
		t.Error("IsTransient(bare error) = true, want false")
	}
	if !IsTransient(&Error{Transient: true, Reason: ReasonNetwork}) {
		t.Error("IsTransient(*Error{Transient:true}) = false, want true")
	}
	if IsTransient(&Error{Transient: false, Reason: ReasonAuth}) {
		t.Error("IsTransient(*Error{Transient:false}) = true, want false")
	}
}

// TestError_PrefixesReasonOnString verifies the Error() method's
// reason-prefix shape so operators can grep server logs by reason.
func TestError_PrefixesReasonOnString(t *testing.T) {
	cases := []struct {
		name string
		err  *Error
		want string
	}{
		{"with cause", &Error{Reason: ReasonAuth, Cause: errors.New("401")}, "auth: 401"},
		{"no cause", &Error{Reason: ReasonValidation}, "validation"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.want {
				t.Errorf("Error() = %q, want %q", got, tc.want)
			}
		})
	}
}
