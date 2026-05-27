package llm

import (
	"errors"
	"fmt"
)

// LLMError is the typed error every llm.Client implementation returns so
// callers can distinguish transient (retry on next tick) from terminal
// (write a failure marker, stop attempting) failures.
//
// Why typed: substring sniffing on err.Error() is brittle to error-text
// drift across providers. Callers that need a deterministic
// transient/terminal decision rely on errors.As / IsTransient instead.
//
// errors.As works through fmt.Errorf("%w") wrapping so callers can chain
// LLMError up multiple layers (a batch wrapper passes LLMError through
// "batch %d: %w" — the worker still sees Transient).
//
// This is the single canonical LLM-error type for the client: it backs the
// domains/llm clients, the summarizer/embedder pipeline, and the Voyage
// embedder error classification. (P2-T6 consolidated the former duplicate
// store-side error type onto this type and deleted the store-side copy.)
type LLMError struct {
	// Transient marks this error as worth retrying on the next collector
	// tick. True for HTTP 429 (rate limit) and 5xx (server error). False
	// for HTTP 4xx-other (4xx not 429), context-too-large, configuration
	// errors, and unknown failure modes.
	Transient bool

	// Reason is a short human-readable category ("http_429",
	// "http_400", "context_too_large", "config", "network"). Logged on
	// every failure for operator triage; not used for control flow.
	Reason string

	// Cause wraps the underlying error so callers can drill into
	// provider-specific detail. errors.As / Unwrap both honor it.
	Cause error
}

// Error returns a Reason-prefixed string suitable for slog.
func (e *LLMError) Error() string {
	if e == nil {
		return "<nil LLMError>"
	}
	if e.Cause != nil {
		return fmt.Sprintf("llm: %s: %v", e.Reason, e.Cause)
	}
	return "llm: " + e.Reason
}

// Unwrap exposes the wrapped Cause so errors.As / errors.Is traverse
// through provider-specific error types.
func (e *LLMError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// IsTransient reports whether err is a *LLMError marked Transient. Bare
// errors and non-LLMError types default to false (terminal) — callers
// treat unknown failure modes as terminal so a single bad request never
// burns infinite worker time.
func IsTransient(err error) bool {
	var le *LLMError
	if errors.As(err, &le) {
		return le.Transient
	}
	return false
}

// HTTPStatusToTransient maps HTTP status code to the transient/terminal
// classification. 429 (rate limit) and 5xx (server error) are transient;
// everything else (including network unreachable encoded as a non-status
// error upstream) is terminal here. Callers that have already classified
// a network error as transient should set LLMError.Transient directly.
func HTTPStatusToTransient(status int) bool {
	return status == 429 || (status >= 500 && status < 600)
}

// Sentinel errors for the registry / config validation paths. Callers
// pattern-match these via errors.Is before falling back to LLMError.

// ErrProviderNotRegistered is returned by NewClient when no factory has
// registered for the requested Provider.
var ErrProviderNotRegistered = errors.New("llm: provider not registered")

// ErrInvalidConfig is returned by NewClient when the provided Config is
// missing a required field for the picked Provider.
var ErrInvalidConfig = errors.New("llm: invalid config")
