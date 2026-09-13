// SPDX-License-Identifier: Apache-2.0

package codexcli

import (
	"encoding/json"
	"strings"
)

// oversizeRejection is the machine-readable payload codex appends to its
// input-length refusal as a `data:` suffix. It is the ONLY structured form the
// refusal takes: codex rejects the turn before any model call, writes one text
// line, and emits no typed event for the condition on its --json stdout stream,
// so the JSON-RPC error code is text as well. The suffix itself is valid JSON,
// which is what makes the parse below a structured read rather than a phrase
// match on a provider's prose.
//
// The field names are codex's. Only the three the transport needs are declared;
// an unknown sibling field is ignored by encoding/json, which is what keeps this
// working when codex adds one.
type oversizeRejection struct {
	InputErrorCode string `json:"input_error_code"`
	MaxChars       int    `json:"max_chars"`
	ActualChars    int    `json:"actual_chars"`
}

const (
	// oversizeDataPrefix introduces the JSON suffix on codex's rejection line.
	oversizeDataPrefix = "data: "

	// oversizeErrorCode is the input_error_code value that names THIS condition.
	// The check on it is load-bearing rather than belt-and-braces: the same
	// `data:` envelope carries every other input-level refusal codex has, so a
	// parse that accepted any well-formed suffix would report an unrelated
	// malformed-input rejection as an oversize one and send the summary worker
	// off splitting a batch that no split can fix.
	oversizeErrorCode = "input_too_large"
)

// parseOversizeRejection reads codex's `data:` suffix out of a failure detail
// and reports whether it names the input-too-large condition, returning the
// sizes it carried. ok is false for a detail with no suffix, a suffix that is
// not valid JSON, a suffix that names no input_error_code, and a suffix naming
// any OTHER condition — every one of which must keep the transport's existing
// classification rather than be read as this one.
//
// IT TAKES THE FOLDED DETAIL, not the two raw buffers, and that is deliberate:
// codexFailureDetail already folds stderr's verbatim line together with the
// message mined out of a stdout `error` event, and codex has been observed
// emitting the refusal on either stream. Parsing the folded string covers both
// with one reader instead of two that could disagree.
//
// The JSON is decoded with a Decoder rather than Unmarshal over a hand-sliced
// substring: a Decoder stops at the end of the first complete value and ignores
// whatever the fold appended after it, so a detail carrying the suffix in the
// MIDDLE parses exactly as one carrying it at the end.
//
// IT IS DELIBERATELY LENIENT ABOUT UNKNOWN KEYS, and that is the opposite of the
// rule for a payload WE author. This envelope is a THIRD PARTY'S wire format:
// codex may add a sibling field to it in any release, and DisallowUnknownFields
// would then make this parse FAIL, stamp the refusal subprocess_failed, skip the
// split, and re-strand the very batch this code exists to rescue — silently, with
// every test green. Forward tolerance is the requirement here, not a shortcut.
// The three fields this reader depends on are each checked before use: the
// condition code by equality below, and the two sizes by the consumer, which
// treats zero as unknown.
func parseOversizeRejection(detail string) (actual, limit int, ok bool) {
	i := strings.LastIndex(detail, oversizeDataPrefix)
	if i < 0 {
		return 0, 0, false
	}
	rest := detail[i+len(oversizeDataPrefix):]
	start := strings.Index(rest, "{")
	if start < 0 {
		return 0, 0, false
	}
	var payload oversizeRejection
	if err := json.NewDecoder(strings.NewReader(rest[start:])).Decode(&payload); err != nil {
		return 0, 0, false
	}
	if payload.InputErrorCode != oversizeErrorCode {
		return 0, 0, false
	}
	return payload.ActualChars, payload.MaxChars, true
}
