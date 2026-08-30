// SPDX-License-Identifier: Apache-2.0

// swallowed_param_gate.go — the PRE-WRITE REFUSAL for a tool call whose
// parameter markup was mis-serialized, leaving one field's value carrying that
// field's own closing tag followed by the swallowed remainder of the call.
//
// THE FAILURE, as reproduced. When a tool call emits a malformed closing tag,
// the harness parser never closes the field it was inside. Every parameter after
// that point is kept as LITERAL TEXT appended to the open field's value, and the
// tool is reached with those parameters simply ABSENT. Three shapes were
// observed in one day, all stored verbatim and all reported as success:
//
//	content ending  "…</content>\n<parameter name=\"session\">ledger-triage"
//	summary ending  "…</summary><metadata>{\"evaluate_at\":\"…\"}</metadata>"
//	content ending  "…</content>\n</invoke>"
//
// In each case the node kept tag soup in a field a human authored, and the
// swallowed parameters (session / links / evidence / the metadata keys) routed
// nowhere. Nothing in the response said so.
//
// WHY THIS REFUSES RATHER THAN WARNS. A warning was tried first (the
// parameter-shaped-tail advisory in intercept_thoughts_think_receipt.go) and it
// does not stop the damage: the write still lands, the tag soup is still stored,
// and the parameters are still dropped. Bad input errors. The refusal happens
// BEFORE any write, so a malformed call leaves the node byte-identical rather
// than half-applied, and the caller re-sends the call correctly instead of
// discovering the loss on a later read-back. The two mechanisms are kept apart on
// purpose and neither replaces the other:
//
//   - THIS GATE refuses the shape a correct call cannot produce (below).
//   - THE ADVISORY still warns about parameter-shaped markup that does NOT carry
//     the field's own closing tag, which is genuinely ambiguous text and must
//     stay writable.
//
// THE PREDICATE, and why it is END-ANCHORED. The naive rule — "the value contains
// its own closing tag" — is unshippable: it refuses the bug report about this
// bug. The defect ticket's own summary and description both quote
// `</summary><metadata>{…}</metadata>` as the specimen, mid-prose, and must stay
// writable. What separates a failure from a quotation is that the swallowed
// remainder RUNS TO THE END of the value — the parser held the field open through
// the rest of the call, so nothing follows it — whereas a quotation sits inside a
// sentence that continues. So the tail AFTER the last occurrence of the field's
// own closing tag decides, on three legs (any one refuses):
//
//	1. the tail is empty — the value ends exactly at its own closing tag
//	2. the tail ENDS in markup (its last non-space character is '>')
//	3. the tail BEGINS with a tag and leaves some element unclosed
//
// KNOWN FALSE-POSITIVE CLASS, stated rather than hidden: prose that quotes an
// UNTERMINATED fragment at the very end of a value — for example a sentence
// closing with `` `</content><parameter name="x">` `` and nothing after it — trips
// leg 3, because it is byte-indistinguishable from the failure. The refusal
// message names the escape (write the angle brackets as &lt; / &gt;), so the
// caller is never stuck; there is no silent degrade and no automatic rewrite.
//
// SCOPE: the SEVENTEEN caller text fields named in swallowScannedFields below,
// wherever they appear in the payload, including nested bodies (create_batch
// nodes[], create_plan phases/steps/criteria, update_batch items[]). Metadata
// VALUES are deliberately not scanned: a metadata value legitimately carries
// arbitrary text (grep patterns, gate-script bodies, shell fragments) and, having
// no wire field name of its own, offers no closing tag to anchor on. The observed
// leaks landed in content and summary, both covered here.

package tools

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// swallowScannedFields are the caller text fields the gate inspects, in the
// order a payload is checked so the same payload always names the same field
// first. They are the fields whose wire name is also the tag name a harness
// closes, which is what makes an anchored closing tag readable at all.
//
// THE LIST IS CHECKED IN BY HAND, and that is a deliberate choice over deriving
// it from the schemas. A schema property carries Type "string" for ids, enums,
// paths, comma-joined id lists and prose alike, so "is this prose" is NOT
// decidable from the schema; a derivation that cannot be written reads as
// automation and delivers nothing. Each entry names the schema it was read from.
//
// SCANNING IS KEY-NAME-GLOBAL. findSwallowedParamValue walks the whole decoded
// payload depth-first and matches on KEY NAME wherever it appears — it has no
// notion of which tool sent the payload. So every name here is scanned on EVERY
// gated tool, not only the one whose schema it came from. Do not add a name on
// the assumption it is scoped to one tool.
//
// `evidence` COLLIDES ACROSS TOOLS and is admitted anyway: on mutate it is the
// finding's prose field, while on thoughts(charge) the wire key of the same name
// is an ARRAY OF NODE IDS — which is exactly why mutate renamed its own charge
// form to charge_evidence. The collision is safe ONLY because the scanner
// type-asserts t[field].(string) and skips a non-string value; it is safe by a
// TYPE GUARD, not by design, and a future field colliding between two PROSE uses
// would get no such protection.
//
// `findings` IS DELIBERATELY EXCLUDED. Its schema says "Comma-separated finding
// node IDs for answer operation" — IDENTIFIERS, not prose. Adding id-valued
// fields to a prose scanner buys nothing and widens the false-positive surface.
var swallowScannedFields = []string{
	// Originally covered: the three generic body fields.
	"content", "description", "summary",
	// thoughts(charge).
	"reasoning",
	// create_plan.
	"goal", "overview", "sketch", "no_patterns_reason",
	// record_decision.
	"rationale", "alternatives", "context", "choice",
	// create_research.
	"question",
	// mutate.
	"conclusion", "evidence", "enforcement", "edge_evidence",
}

// swallowOpenTagRe matches an OPENING tag's name. `</name>` cannot match: the
// character after `<` must begin an identifier, and `/` does not.
var swallowOpenTagRe = regexp.MustCompile(`<([A-Za-z][A-Za-z0-9_:.-]*)`)

// swallowFragmentQuoteLimit bounds how much of the offending tail the refusal
// message quotes. The message has to show the caller the actual bytes to be
// actionable, but a swallowed remainder can be the whole rest of a tool call.
const swallowFragmentQuoteLimit = 200

// swallowedParamFragment returns the offending fragment — the field's own
// closing tag plus everything after it — when text carries a swallowed-parameter
// tail for a field sent under the wire name `field`. Returns "" for every value
// that is merely ABOUT such markup, and for the overwhelmingly common clean case.
//
// See the file header for the three legs and for the known false-positive class.
// The LAST occurrence of the closing tag is the anchor, because a value may
// legitimately discuss the markup earlier and still have been damaged at the end.
func swallowedParamFragment(field, text string) string {
	closing := "</" + field + ">"
	idx := strings.LastIndex(text, closing)
	if idx < 0 {
		return ""
	}
	rest := text[idx+len(closing):]
	trimmedRight := strings.TrimRight(rest, " \t\r\n")
	trimmedLeft := strings.TrimLeft(rest, " \t\r\n")

	swallowed := trimmedRight == "" ||
		strings.HasSuffix(trimmedRight, ">") ||
		(strings.HasPrefix(trimmedLeft, "<") && hasUnclosedTag(trimmedLeft))
	if !swallowed {
		return ""
	}
	return strings.TrimSpace(text[idx:])
}

// hasUnclosedTag reports whether s opens an element it never closes. Used as leg
// 3 of the predicate: a swallowed remainder typically ends inside the parameter
// tag the harness was mid-way through when the call ended.
func hasUnclosedTag(s string) bool {
	for _, m := range swallowOpenTagRe.FindAllStringSubmatchIndex(s, -1) {
		name := s[m[2]:m[3]]
		if !strings.Contains(s[m[1]:], "</"+name+">") {
			return true
		}
	}
	return false
}

// rejectSwallowedParamValues walks raw and returns a refusal naming the first
// field carrying a swallowed-parameter tail, or nil when every scanned value is
// clean. An empty payload is clean by construction.
//
// A payload this gate cannot decode yields no diagnosis here — see
// decodeForSwallowScan for why that is not a hole.
func rejectSwallowedParamValues(tool string, raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	payload, ok := decodeForSwallowScan(raw)
	if !ok {
		return nil
	}
	field, fragment := findSwallowedParamValue(payload)
	if fragment == "" {
		return nil
	}
	return swallowedParamError(tool, field, fragment)
}

// decodeForSwallowScan decodes raw into a generic tree, reporting ok=false when
// it does not parse.
//
// SAYING NOTHING ABOUT AN UNPARSEABLE PAYLOAD IS CORRECT HERE, and it is the
// opposite of the param-accounting gates' fail-closed rule, for a reason worth
// stating. Those gates answer "did the caller supply a param this arm drops?",
// where an unreadable payload hides the very thing being checked, so passing it
// through would be the hole. This gate answers a narrower question — "does a
// value carry markup that proves the call was mis-serialized?" — and an
// unparseable payload carries no such value to read. It is a DIFFERENT
// malformation, and every caller of this gate already refuses it: each one runs
// rejectUndeclaredParams beside this call, which reads the key set through
// suppliedMutateParams and FAILS CLOSED when that read returns nil — which is
// exactly the unparseable case. Several arms (create_research, create_test_plan)
// have decoded into their own args struct before reaching here and error on the
// parse first. Answering here would replace those precise messages with a
// swallowed-parameter diagnosis that misnames the fault.
func decodeForSwallowScan(raw json.RawMessage) (any, bool) {
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, false
	}
	return payload, true
}

// findSwallowedParamValue walks a decoded JSON payload depth-first and returns
// the first (field, fragment) pair whose value carries a swallowed-parameter
// tail. Descends into nested objects and arrays so the structured create bodies
// (create_batch nodes[], create_plan phases/steps/criteria, update_batch items[])
// are covered, not only top-level params — a top-level-only sweep would leave
// every structured create unguarded.
//
// Object keys are visited in sorted order so a payload with more than one damaged
// value always names the same one first.
func findSwallowedParamValue(v any) (string, string) {
	switch t := v.(type) {
	case map[string]any:
		for _, field := range swallowScannedFields {
			s, ok := t[field].(string)
			if !ok {
				continue
			}
			if frag := swallowedParamFragment(field, s); frag != "" {
				return field, frag
			}
		}
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if field, frag := findSwallowedParamValue(t[k]); frag != "" {
				return field, frag
			}
		}
	case []any:
		for _, item := range t {
			if field, frag := findSwallowedParamValue(item); frag != "" {
				return field, frag
			}
		}
	}
	return "", ""
}

// swallowedParamError is the refusal. It names the tool, the field, and quotes
// the offending bytes, because a caller cannot repair a call it cannot see; and
// it states the escape, because the predicate's known false-positive class
// (prose ending in an unterminated quoted fragment) has to be reachable by an
// author who genuinely means to write about the markup.
func swallowedParamError(tool, field, fragment string) error {
	return fmt.Errorf(
		"%s: the %s parameter's value carries its own closing tag </%s> followed by "+
			"parameter markup that runs to the end of the value — the tool call was "+
			"mis-serialized, so every parameter after that point was swallowed into %s as "+
			"literal text and reached this tool as ABSENT. Refusing the whole call: nothing "+
			"was written, and the target is unchanged. Offending tail of %s: %s. Re-send the "+
			"call with each parameter in its own tag. If the text is genuinely ABOUT tool-call "+
			"markup, escape the angle brackets (&lt; and &gt;) so it cannot be read as a "+
			"serialization failure",
		tool, field, field, field, field, quoteSwallowedFragment(fragment))
}

// quoteSwallowedFragment renders the offending tail bounded to
// swallowFragmentQuoteLimit runes, marking a truncation explicitly so a caller
// never mistakes the quoted prefix for the whole fragment.
func quoteSwallowedFragment(fragment string) string {
	if utf8.RuneCountInString(fragment) <= swallowFragmentQuoteLimit {
		return fmt.Sprintf("%q", fragment)
	}
	runes := []rune(fragment)
	return fmt.Sprintf("%q (truncated; %d runes total)", string(runes[:swallowFragmentQuoteLimit]), len(runes))
}
