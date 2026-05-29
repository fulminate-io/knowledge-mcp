// SPDX-License-Identifier: Apache-2.0

package backends

import (
	"errors"
	"fmt"
)

// Error is the typed error every backend adapter returns so callers (the T3
// mutate dispatcher and the T4 sync runner) can distinguish transient
// (retry on next sync; flag <backend>_dirty:true) from terminal (stamp
// <backend>_sync_failure_reason; stop attempting) failures.
//
// Mirrors *llm.LLMError at cmd/knowledge/internal/llm/errors.go — keep field
// shape and Error/Unwrap/IsTransient semantics in sync. Substring sniffing
// on err.Error() is brittle to error-text drift across providers; the
// dispatcher and runPush both make a deterministic transient/terminal
// decision via the typed wrap.
//
// errors.As walks through fmt.Errorf("%w") wraps so the dispatcher and
// runPush both see Transient through arbitrary upstream wrap layers.
type Error struct {
	// Transient marks this error as worth retrying on the next sync tick.
	// True for network failures, timeouts, HTTP 5xx, and rate-limit
	// responses. False for auth failures, validation errors, not-found,
	// unknown workflow state, and adapter-side invalid argument errors —
	// the runner treats these as terminal so a single bad node never
	// burns infinite retry time.
	Transient bool

	// Reason is a short human-readable category (one of the Reason*
	// constants below). Stamped into <backend>_sync_failure_reason on
	// terminal outcomes so operators can triage; logged on every failure.
	Reason string

	// Cause wraps the underlying error so callers can drill into
	// provider-specific detail. errors.As / Unwrap both honor it.
	Cause error
}

// Error returns a Reason-prefixed string suitable for slog.
func (e *Error) Error() string {
	if e == nil {
		return "<nil backends.Error>"
	}
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", e.Reason, e.Cause)
	}
	return e.Reason
}

// Unwrap exposes the wrapped Cause so errors.As / errors.Is traverse
// through provider-specific error types (ErrUnauthorized, ErrUnknownState,
// graphQL response errors, etc.).
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// IsTransient reports whether err is a *Error marked Transient. Bare
// errors and non-*Error types default to false (terminal) — the runner
// treats unknown failure modes as terminal so a single bad node never
// burns infinite worker time. Mirrors llm.IsTransient.
func IsTransient(err error) bool {
	if be, ok := errors.AsType[*Error](err); ok {
		return be.Transient
	}
	return false
}

// Canonical Reason categories. Stamped into <backend>_sync_failure_reason
// on terminal outcomes; logged on transient outcomes for operator triage.
const (
	// Transient — retry on next sync.

	// ReasonNetwork covers connection refused, DNS failures, TLS handshake
	// errors, and any other failure where a request never reached the
	// backend in a meaningful way.
	ReasonNetwork = "network"
	// ReasonTimeout covers context-deadline-exceeded and server-side
	// request timeouts.
	ReasonTimeout = "timeout"
	// ReasonHTTP5xx covers 500-599 responses where the backend
	// acknowledged but failed to process the request.
	ReasonHTTP5xx = "http_5xx"
	// ReasonRateLimited covers 429 responses and any provider-specific
	// "slow down" signal.
	ReasonRateLimited = "rate_limited"

	// Terminal — stamp <backend>_sync_failure_reason.

	// ReasonAuth covers 401 / 403 and any provider-specific revoked-key
	// response — operator must rotate credentials before sync resumes.
	ReasonAuth = "auth"
	// ReasonValidation covers backend-rejected payloads (bad enum values,
	// schema mismatch).
	ReasonValidation = "validation"
	// ReasonNotFound covers 404 and any provider-specific "this resource
	// has been deleted" response.
	ReasonNotFound = "not_found"
	// ReasonUnknownState covers references to workflow/project states the
	// backend does not know about.
	ReasonUnknownState = "unknown_state"
	// ReasonInvalidArgument covers programmer / adapter-boundary errors
	// — e.g. nil ref, missing required identifier — surfaced as terminal
	// so they don't loop forever.
	ReasonInvalidArgument = "invalid_argument"
	// ReasonGraphQL covers GraphQL top-level errors that don't map to a
	// more specific Reason. Surfaced with the original message in Cause.
	ReasonGraphQL = "graphql"
)
