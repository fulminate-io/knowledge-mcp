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

	// Reason is a short category ("http_429", "http_400",
	// "context_too_large", "config", "network"). Logged on every failure for
	// operator triage, AND IT IS THE CONTROL-FLOW SEAM: the pipeline's
	// classifier switches on this string alone and imports no provider package,
	// so Reason is the only provider-agnostic way a transport can tell the
	// pipeline WHICH failure it saw. A consumer therefore never matches on the
	// rendered message text. Adding a condition means adding a Reason value and
	// mapping it there, not sniffing Cause.
	Reason string

	// RetryAfter carries the server's stated retry delay, parsed from the
	// HTTP Retry-After header on a 429 / 503, when present. Zero means no
	// hint — callers fall back to their own exponential backoff. Only the
	// HTTP providers (anthropic / openai / gemini / voyage) populate it; the
	// CLI providers are subprocesses with no response headers and always
	// leave it zero.
	RetryAfter time.Duration

	// InputChars and MaxInputChars carry the size the provider counted and the
	// size it allows, in characters, when it rejected the request for being too
	// large. ZERO MEANS UNKNOWN on either field, and a consumer must behave
	// correctly with zero: only a transport that reads a numeric limit out of the
	// rejection populates them, which today is codex-cli alone (its rejection
	// carries a machine-readable `data:` suffix). The documentation shape is
	// RetryAfter's above — which providers populate it, and that the rest leave
	// it at the zero value.
	//
	// They are NOT the caller's own measurement: a summary worker already knows
	// the size it sent. MaxInputChars is the value it cannot know any other way,
	// and it is what lets a retry pack sub-requests against the real limit
	// instead of blindly halving.
	InputChars    int
	MaxInputChars int

	// Cause wraps the underlying error so callers can drill into
	// provider-specific detail. errors.As / Unwrap both honor it.
	Cause error
}

// ReasonInputTooLarge is the Reason a transport stamps when the provider refused
// the request because its input exceeded a size limit — a deterministic fault of
// the request itself, so a retry of the SAME input on ANY provider fails
// identically and the only useful retry is a SMALLER one.
//
// IT IS A CONSTANT RATHER THAN A LITERAL SPELLED PER SIDE, unlike the older
// Reason vocabulary. A mis-spelling here cannot be caught by a compiler or by a
// test of either side alone: the transport would stamp a string the classifier
// does not know, the error would classify as unclassified-Other, the fallback
// chain would advance to a provider that must also refuse it, and the summary
// axis would go on re-attempting an input nothing can accept — the exact latch
// this reason exists to end, wearing a different hat. The declaring package is
// imported by every transport AND by the pipeline classifier, so one spelling
// costs nothing.
const ReasonInputTooLarge = "input_too_large"

// InputTooLargeOf reports whether err is (or wraps) an *LLMError stamped
// ReasonInputTooLarge, returning the sizes it carried. actual and limit are the
// provider's own counts and are ZERO WHEN UNKNOWN — ok tells you the condition
// was detected, never that the numbers are populated, and those are genuinely
// different facts: a transport can recognize the refusal without being told a
// number. Callers that need a size always have their own.
//
// This is the TYPED read of the condition. A caller must not reach the same
// conclusion by matching err.Error() for a provider's phrasing: the message text
// is not a contract, drifts per provider and per version, and a match on it
// silently stops working rather than failing loudly.
func InputTooLargeOf(err error) (actual, limit int, ok bool) {
	le, isLLM := errors.AsType[*LLMError](err)
	if !isLLM || le.Reason != ReasonInputTooLarge {
		return 0, 0, false
	}
	return le.InputChars, le.MaxInputChars, true
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
