package llm

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
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
// internal/llm clients, the summarizer/embedder pipeline, and the Voyage
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

	// RetryAfter carries the server's stated retry delay, parsed from the
	// HTTP Retry-After header on a 429 / 503, when present. Zero means no
	// hint — callers fall back to their own exponential backoff. Only the
	// HTTP providers (anthropic / openai / gemini / voyage) populate it; the
	// CLI providers are subprocesses with no response headers and always
	// leave it zero.
	RetryAfter time.Duration

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
	if le, ok := errors.AsType[*LLMError](err); ok {
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

// ParseRetryAfter extracts the Retry-After delay from a response header, in
// the two RFC 7231 forms: delay-seconds (e.g. "30") and an HTTP-date (e.g.
// "Wed, 21 Oct 2026 07:28:00 GMT"). Returns 0 when the header is absent,
// empty, unparseable, or already in the past — callers treat 0 as "no hint"
// and fall back to their own backoff. Shared by every HTTP provider so a 429
// is honored identically regardless of which model backs the summarizer.
func ParseRetryAfter(h http.Header) time.Duration {
	if h == nil {
		return 0
	}
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// RetryAfterOf returns the server-stated retry delay carried by err when it is
// (or wraps) an *LLMError with a populated RetryAfter, else 0. Callers pass the
// result to their backoff gate so a provider 429 waits the server's delay
// instead of guessing.
func RetryAfterOf(err error) time.Duration {
	if le, ok := errors.AsType[*LLMError](err); ok {
		return le.RetryAfter
	}
	return 0
}

// Sentinel errors for the registry / config validation paths. Callers
// pattern-match these via errors.Is before falling back to LLMError.

// ErrProviderNotRegistered is returned by NewClient when no factory has
// registered for the requested Provider.
var ErrProviderNotRegistered = errors.New("llm: provider not registered")

// ErrInvalidConfig is returned by NewClient when the provided Config is
// missing a required field for the picked Provider.
var ErrInvalidConfig = errors.New("llm: invalid config")
