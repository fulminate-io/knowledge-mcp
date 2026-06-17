// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"errors"
	"strconv"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// ErrClass is the small, stable error-classification enum the circuit breaker
// tracks per window so the auto-pause reason can name the DOMINANT failure
// class instead of a fixed generic string. It is EXPORTED because this package
// is the seam OWNER: downstream per-axis escalation work consumes the dominant
// class and downstream fail-fast work consumes the deterministic-terminal
// predicate + per-class streaks. Keep this surface minimal and stable so
// neither downstream consumer has to re-touch the classifier.
type ErrClass int

// The six error classes. ClassOther is the zero value / default so a
// zero-value ErrClass is the safe "unclassified" bucket. classify maps every
// KNOWN live LLMError.Reason to a non-Other class; only genuinely-unknown
// reasons and bare/non-LLMError errors fall to ClassOther.
const (
	// ClassOther is the zero value: an unclassified error (config / request
	// construction faults, an unclassified Generate error, or a bare error).
	ClassOther ErrClass = iota
	// ClassParse: the model produced output that did not match the requested
	// schema and could not be decoded.
	ClassParse
	// ClassTruncation: the model output hit its max_tokens cap and was cut off.
	ClassTruncation
	// ClassAuthQuota: an authentication failure or a quota / rate-limit / usage
	// cap rejection from the provider.
	ClassAuthQuota
	// ClassTimeoutTransport: a timeout, network, or transport-level failure
	// (including a CLI subprocess that exited or timed out at the transport
	// level).
	ClassTimeoutTransport
	// ClassInvalidRequest: a malformed / invalid request — a deterministic
	// client-side fault, distinct from a quota/auth condition.
	ClassInvalidRequest
)

// classify maps an errored summary/embed LLM call to its ErrClass. It is the
// SINGLE classification point in the pipeline and is deliberately unexported:
// nothing outside this package classifies. It extracts the typed *llm.LLMError
// and switches on its Reason string — the provider-agnostic seam (the pipeline
// imports llm but NONE of the provider packages). classify deliberately
// DISCARDS LLMError.Transient and operates on Reason alone, so consumers
// reason about the class axis independently of the transient/terminal axis.
//
// The mapping is built from the EXHAUSTIVE produced Reason vocabulary (every
// Reason literal emitted under internal/llm + internal/llmproviders +
// internal/embed): there is NO silent ClassOther fallthrough for a KNOWN live
// reason — every known reason is named in a case, with a one-line rationale
// where the mapping is non-obvious. Only genuinely-unknown reasons and
// bare/non-LLMError errors reach the default ClassOther.
func classify(err error) ErrClass {
	le, ok := errors.AsType[*llm.LLMError](err)
	if !ok {
		// A bare / non-LLMError error carries no Reason vocabulary to classify.
		return ClassOther
	}
	switch le.Reason {
	// Parse: model output did not match the requested schema.
	case "parse_summaries_json", "parse_response", "parse_cli_response",
		"decode_response", "empty_structured_output", "no_choices",
		"no_candidates", "prompt_blocked":
		return ClassParse

	// Truncation: anthropic stamps this Reason on the *llm.LLMError wrapping a
	// TruncatedOutputError; reading the Reason string keeps the pipeline
	// provider-agnostic (it does NOT import the anthropic package).
	case "response_truncated":
		return ClassTruncation

	// Auth/quota: the CLI non-zero-exit and structured-error-envelope reasons
	// bundle rate limit / usage cap / auth failure / schema rejection; auth and
	// quota dominate that family, so both map here.
	case "cli_exec", // claudecli non-zero exit: rate limit / usage cap / auth / schema rejection bundled; auth+quota dominate.
		"cli_response_error": // claudecli structured IsError envelope: same provider auth/quota/schema family as cli_exec.
		return ClassAuthQuota

	// Timeout/transport: network + read-side failures and CLI-subprocess exit /
	// timeout siblings are all transport-level.
	case "network", "read_response", "cli_deadline", "subprocess_timeout",
		"subprocess_failed", // codexcli non-zero exit (codex exited N): transport-class CLI-exit sibling of subprocess_timeout.
		"subprocess_error":  // codexcli run failure (codex run failed): transport-class CLI-exit sibling of subprocess_timeout.
		return ClassTimeoutTransport

	// Other: config / request-construction faults and an unclassified Generate
	// error. Each KNOWN reason is named here so the mapping is a deliberate
	// ClassOther, not an accidental fallthrough.
	case "config", "marshal_request", "create_request", "translate_request",
		"build_request", "subprocess_setup", "cli_not_found", "turn_failed",
		"openai_api_error",
		"summarize_generate": // reasonPrefix stamped only for a bare/empty-Reason Generate error -> unclassified -> Other.
		return ClassOther

	default:
		// http_<status> reasons are formatted via Sprintf("http_%d", status), so
		// they are not matchable as literal cases — parse the numeric suffix.
		if cls, matched := classifyHTTPReason(le.Reason); matched {
			return cls
		}
		// A genuinely-unknown reason: classify conservatively as Other.
		return ClassOther
	}
}

// classifyHTTPReason maps an "http_<status>" Reason (formatted via
// Sprintf("http_%d", status) by the HTTP providers) to its ErrClass by parsing
// the numeric suffix. classify runs once per errored batch (not per node), so
// a serial TrimPrefix + Atoi is correct and cheap — no regex needed. The
// boolean reports whether the reason was an http_ reason at all; a non-http_
// reason returns (ClassOther, false) so the caller can keep treating it as a
// genuinely-unknown reason.
func classifyHTTPReason(reason string) (ErrClass, bool) {
	suffix, ok := strings.CutPrefix(reason, "http_")
	if !ok {
		return ClassOther, false
	}
	status, err := strconv.Atoi(suffix)
	if err != nil {
		// http_ prefix but an unparseable suffix: treat as a matched-but-unknown
		// http reason and bucket to Other.
		return ClassOther, true
	}
	switch {
	case status == 401 || status == 403 || status == 429:
		return ClassAuthQuota, true
	case status == 400 || status == 404 || status == 422:
		return ClassInvalidRequest, true
	case status >= 500 && status <= 599:
		return ClassTimeoutTransport, true
	case status >= 400 && status <= 499:
		// A 4xx that is not auth/quota is a deterministic client-request fault.
		return ClassInvalidRequest, true
	default:
		return ClassOther, true
	}
}

// Label returns a short human phrase naming the error class for the auto-pause
// reason rendered in pipeline_status and the search staleness footer.
func (c ErrClass) Label() string {
	switch c {
	case ClassParse:
		return "response-parse failures (model output did not match the requested schema)"
	case ClassTruncation:
		return "response-truncation failures (model output hit max_tokens)"
	case ClassAuthQuota:
		return "auth/quota failures"
	case ClassTimeoutTransport:
		return "timeout/transport failures"
	case ClassInvalidRequest:
		return "invalid-request failures (malformed request)"
	default:
		return "errors"
	}
}

// shortLabel returns a compact one-token shorthand for the class, used in the
// breakdown suffix of the auto-trip reason (e.g. "parse=18, timeout/transport=2").
func (c ErrClass) shortLabel() string {
	switch c {
	case ClassParse:
		return "parse"
	case ClassTruncation:
		return "truncation"
	case ClassAuthQuota:
		return "auth/quota"
	case ClassTimeoutTransport:
		return "timeout/transport"
	case ClassInvalidRequest:
		return "invalid-request"
	default:
		return "other"
	}
}

// ShouldAdvanceFallback reports whether a fallback summarizer chain should
// ADVANCE to the next entry on this error, versus failing the node directly. It
// is the single exported seam the (client-side) summarizer-chain selection
// wrapper consults — defined HERE, over the same classify + IsDeterministicTerminal
// the breaker uses, so the classifier stays the single source of truth and the
// wrapper duplicates no classification.
//
// Advance == !IsDeterministicTerminal(classify(err)): a quota / rate-limit /
// overload / timeout / unclassified failure is NOT deterministic-terminal, so it
// advances (a different provider may serve the batch); a parse / invalid-request
// / truncation failure IS deterministic-terminal, so it fails directly (a retry
// on another entry would fail identically). The classifier discards the
// transient/terminal axis, so this decision is on the error CLASS alone — exactly
// the design's "advance on any non-deterministic-terminal failure" rule.
func ShouldAdvanceFallback(err error) bool {
	return !IsDeterministicTerminal(classify(err))
}

// IsDeterministicTerminal reports whether an error class reproduces identically
// for the same batch + config — i.e. a retry of the same input is futile. True
// for ClassParse, ClassInvalidRequest, and ClassTruncation (truncation
// reproduces identically for the same batch + config). It is EXPORTED and owned
// HERE so the fail-fast consumer can read the predicate without re-touching the
// classifier.
func IsDeterministicTerminal(c ErrClass) bool {
	switch c {
	case ClassParse, ClassInvalidRequest, ClassTruncation:
		return true
	default:
		return false
	}
}
