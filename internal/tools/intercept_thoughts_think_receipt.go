// SPDX-License-Identifier: Apache-2.0

// intercept_thoughts_think_receipt.go — the thoughts(think) render tail: a
// RECEIPT of what the write actually landed, plus the non-fatal
// parameter-shaped-tail advisory.
//
// WHY A RECEIPT RATHER THAN AN ARGUMENT ECHO: when a caller's tool-call
// serialization loses a parameter — leaving it as literal text inside the
// content body rather than transmitting it as a key — the handler is reached
// with that parameter simply absent. The create succeeds, an ID comes back, and
// the only trace of the narrowing is a MISSING line in the tail. A missing line
// is the weakest available tell, and in practice it goes unread. Every receipt
// line therefore renders unconditionally, with an explicit "none" for each
// context parameter that landed nothing, so a narrowed write is STATED instead
// of merely unmentioned. No parameter that never reached the tool can be
// recovered here; only made visible.
//
// THE ADVISORY OWNS ONLY THE AMBIGUOUS HALF, and warns rather than refusing
// because content may legitimately contain literal tool-call markup (a thought
// documenting the grammar is a correct call), so refusing on a mere mention would
// reject correct writes. The UNAMBIGUOUS half — a value carrying its own closing
// tag with the remainder running to the end of the value, which a correct call
// cannot produce — is REFUSED before any write by rejectSwallowedParamValues
// (swallowed_param_gate.go), at the tool's accounting point upstream of this
// render. So a caller reaching this advisory has already cleared that gate: the
// advisory flags a shape, and the receipt above it is the authority on what was
// written.

package tools

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// thinkReceipt records what a thought create LANDED, as opposed to what the
// caller asked for. composeThoughtCreate fills it at the compose/persist seam,
// where every outcome is already in hand — the tail never re-queries.
type thinkReceipt struct {
	ID string
	// SessionName is the requested session; SessionID the node it resolved to
	// (get-or-create). A non-empty SessionID means the session--contains-->thought
	// edge rode the create_batch, which is atomic with the node create.
	SessionName string
	SessionID   string
	// TicketID is the requested ticket; TicketLinked reports whether its
	// contains edge rode the batch. An unresolvable ticket is dropped by
	// buildContextLinks and never blocks the create — the receipt is where that
	// drop becomes visible to the caller rather than only to the log.
	TicketID     string
	TicketLinked bool
	// LinksResolved / LinksUnresolved are the per-link outcomes of the 3-outcome
	// cross-graph resolve. UNRESOLVED, not "dropped": the think path keeps a
	// no-hit id as a raw relates-to target (server outcome c), so the edge is
	// attempted rather than discarded.
	LinksResolved   int
	LinksUnresolved int
	// BornLinks counts the code-referent relates-to edges minted from
	// summary+content.
	BornLinks int
	// BranchesFrom is the supersession parent. Reported as an outcome: the edge
	// rides the same atomic batch as the node, so a returned ID means it landed.
	BranchesFrom string
}

// renderThinkTail builds the "Thought recorded → ID: ..." render followed by the
// write receipt. EVERY line renders unconditionally — the none/0 cases are the
// point (see the file header): an absent line is what made a silently narrowed
// write easy to miss.
func renderThinkTail(r thinkReceipt) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Thought recorded → ID: %s", r.ID)

	sb.WriteString("\nSession: ")
	if r.SessionID != "" {
		fmt.Fprintf(&sb, "%s → %s", r.SessionName, r.SessionID)
	} else {
		sb.WriteString("none")
	}

	sb.WriteString("\nTicket: ")
	switch {
	case r.TicketID == "":
		sb.WriteString("none")
	case r.TicketLinked:
		fmt.Fprintf(&sb, "%s → contains edge written", r.TicketID)
	default:
		fmt.Fprintf(&sb, "%s → NOT linked (id resolved nowhere)", r.TicketID)
	}

	fmt.Fprintf(&sb, "\nLinks: %d resolved, %d unresolved", r.LinksResolved, r.LinksUnresolved)
	fmt.Fprintf(&sb, "\nCode born-links: %d", r.BornLinks)

	sb.WriteString("\nBranches from: ")
	if r.BranchesFrom != "" {
		sb.WriteString(r.BranchesFrom)
	} else {
		sb.WriteString("none")
	}
	return sb.String()
}

// paramTailWindow is how many trailing BYTES of content the advisory inspects.
// Bounded on purpose: parameter markup lost by a serialization failure lands at
// the very END of the content body (the parser held content open through it),
// while a thought that merely discusses the grammar mid-prose is not the shape
// this is looking for.
const paramTailWindow = 200

// paramTailNameRe extracts the parameter name from a parameter-open tag,
// tolerating both quoted and unquoted values and arbitrary intervening space.
var paramTailNameRe = regexp.MustCompile(`<parameter\s+name\s*=\s*"?([A-Za-z_][A-Za-z0-9_]*)`)

// paramShapedTailWarning returns a non-fatal advisory when the named field's
// text ends with parameter-shaped markup, naming every parameter the markup
// mentions. Returns "" for the overwhelmingly common clean case.
//
// `field` is the caller's WIRE NAME for the text being inspected (content,
// summary, description). It is interpolated into the message so the advisory
// names the field the caller actually sent, AND it builds the closing tag the
// detection predicate looks for — so passing anything other than the real wire
// name silently disables detection of the bare dialect for that field.
//
// DETECTION IS AN OR OF TWO LEGS, and they see different dialects. The regex leg
// catches the namespaced parameter-open-tag form; the closing-tag substring leg
// catches the BARE form, where a closing field tag is followed by a bare
// parameter tag with no `parameter name=` wrapper anywhere in it. Hardcoding the
// closing tag to one field would leave the bare dialect undetected on every
// other field.
//
// The closing-tag leg still fires here on a MID-PROSE mention, which is the part
// rejectSwallowedParamValues deliberately does not refuse (its predicate is
// end-anchored, so a value that quotes the shape and then continues into
// sentences stays writable). A warning on that shape is a tell worth having; a
// refusal on it would block the incident report about the defect.
//
// BEST-EFFORT, NOT A GUARANTEE: only the last paramTailWindow bytes of the text
// are inspected, so a leak whose fragment runs longer than that window is not
// detected. The advisory is a tell, and its silence is not evidence of a clean
// call.
//
// The wording is precise about scope on purpose: it says the TEXT applied
// nothing, never that the named parameters are missing from the write. A call
// can correctly emit `session` as a real parameter AND quote the markup inside
// its text — the receipt rendered directly above would then contradict a
// blanket "session was not applied", which is why the advisory defers to it.
func paramShapedTailWarning(field, text string) string {
	tail := contentTail(text, paramTailWindow)
	names := paramTailNames(tail)
	if len(names) == 0 && !strings.Contains(tail, "</"+field+">") {
		return ""
	}
	mention := ""
	if len(names) > 0 {
		mention = " mentioning: " + strings.Join(names, ", ")
	}
	return field + " ends with parameter-like markup" + mention +
		" — text inside " + field + " is never interpreted as tool parameters, so nothing in that text was applied." +
		" If any of it was meant as a parameter, re-send it as a real tool parameter; the write receipt above" +
		" states what actually landed."
}

// contentTail returns the last n bytes of s, advanced to the nearest rune
// boundary so the window is always valid UTF-8.
func contentTail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	start := len(s) - n
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}

// paramTailNames returns the deduplicated, sorted parameter names mentioned by
// parameter-open tags in the tail. Sorted so the advisory text is deterministic.
func paramTailNames(tail string) []string {
	matches := paramTailNameRe.FindAllStringSubmatch(tail, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(matches))
	names := make([]string, 0, len(matches))
	for _, m := range matches {
		if _, dup := seen[m[1]]; dup {
			continue
		}
		seen[m[1]] = struct{}{}
		names = append(names, m[1])
	}
	sort.Strings(names)
	return names
}
