// SPDX-License-Identifier: Apache-2.0

// negation_gate.go — the deterministic, LLM-free verified-negation gate.
//
// A negation op (asserting a node is contradicted / invalidated / superseded)
// must carry a verbatim quote of the contradicted node's CURRENT first-party
// source. The gate validates that quote by a pure whitespace-normalized substring
// match against the live source (resolved through the cited-code boundary, or the
// thought's own current content) — NO LLM, NO embed, NO semantic/relevance step
// anywhere in the path. It is a honesty primitive: it forces the negator to have
// actually pulled the live source before contradicting it.
//
// WHAT THE GATE GUARANTEES: existence + currency + (coarse) locality of the cited
// first-party source. WHAT IT DELIBERATELY DOES NOT: it does NOT judge whether the
// quote JUSTIFIES the contradiction — relevance/soundness would require an LLM,
// which is the explicitly-rejected bandaid (CEO-locked: no LLM in this path). The
// accepted residual: an agent could quote an unrelated-but-real CURRENT line that
// falls within the cited range; the optional cited_range locality check narrows
// this to the right file, but full line-range relevance is out of scope for v1.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// errFirstPartyEvidenceRequired is the rejection returned when a negation op
// carries no quote, or a quote that does not match the contradicted node's
// current source (a hallucinated or stale quote). The message tells the negator
// exactly what to supply.
const errFirstPartyEvidenceMsg = "first-party evidence required — quote the current source of %s you verified (supply verified_quote as a TOP-LEVEL param on this call — not inside metadata and not inside edge_evidence — holding a verbatim substring of the node's current source; a hallucinated or stale quote will not match)"

// errNonNegationProofMsg is the rejection returned when a call that is NOT a
// negation carries a negation-gate proof param. Naming the three shapes that DO
// take one is what makes the refusal actionable rather than a bare no.
const errNonNegationProofMsg = "%s is a negation-gate proof-of-work param and this call is not a negation — supply it only on mutate(link, relationship:\"contradicts\"), mutate(update, status:\"invalidated\"), or thoughts(think) with branches_from set; drop it, or issue the negation call that needs it"

// negationOp is the recognized shape of a single negation tool-call: which node
// is being contradicted, the supplied proof-of-work quote, and the optional
// file:line-range locality hint. It is the configurable unit the reusable gate
// validates — v1 enables it for the four negation shapes only.
type negationOp struct {
	ContradictedID string
	Quote          string
	CitedRange     string
}

// recognizeNegationOp returns the negationOp + true ONLY for the four negation
// shapes, false (and a zero op) for everything else. The op is recognized purely
// from the already-parsed arg structs — no graph read. The four shapes:
//
//   - mutate(link relationship:contradicts) → the To node is contradicted.
//     "contradicts" has no EdgeType constant; it is a raw mutate(link)
//     relationship string (thought/wire_adjacency.go:74-76 keys it as the
//     EdgeType("contradicts") literal), so the predicate matches the literal.
//   - mutate(update status:invalidated) → the ID node is invalidated.
//     "invalidated" is a raw status string (no StatusInvalidated Go constant).
//   - thoughts(think) supersession (branches_from set) → the branches_from node is
//     contradicted. This single predicate covers BOTH plain supersession AND the
//     branches_from + status:invalidated shape: branches_from != "" IS the
//     supersession-of-an-existing-node signal. thinkArgs has no Operation field
//     (branches_from is a think-only field — Operation lives only on the separate
//     thoughtsArgs), so the op IS the think op by construction; the predicate gates
//     on toolName=="thoughts" && t.BranchesFrom != "". A think with
//     status:invalidated but NO branches_from creates a fresh invalidated thought
//     with no prior target — that is not a negation of an existing node, so it is
//     NOT recognized.
func recognizeNegationOp(toolName string, a mutateArgs, t thinkArgs) (negationOp, bool) {
	switch toolName {
	case "mutate":
		switch {
		case a.Operation == "link" && a.Relationship == "contradicts":
			return negationOp{ContradictedID: a.To, Quote: a.VerifiedQuote, CitedRange: a.CitedRange}, true
		case a.Operation == "update" && a.Status == "invalidated":
			return negationOp{ContradictedID: a.ID, Quote: a.VerifiedQuote, CitedRange: a.CitedRange}, true
		}
	case "thoughts":
		if t.BranchesFrom != "" {
			return negationOp{ContradictedID: t.BranchesFrom, Quote: t.VerifiedQuote, CitedRange: t.CitedRange}, true
		}
	}
	return negationOp{}, false
}

// validateNegationQuote is the deterministic match: it resolves the contradicted
// node's CURRENT source and returns nil ONLY when the supplied quote is a
// whitespace-normalized substring of that source (and, when a cited_range is
// supplied, the source is local to the cited path). It is FAIL-CLOSED at every
// branch: an empty quote, an unresolvable node, or a non-matching quote all
// reject. No LLM, no regex — only resolveThoughtCurrentSource (graph reads) and
// strings.Fields/Contains.
func validateNegationQuote(ctx context.Context, gc GraphCaller, op negationOp) error {
	if strings.TrimSpace(op.Quote) == "" {
		// No quote = immediate miss. Fail-closed.
		return firstPartyEvidenceError(op.ContradictedID)
	}
	sources, err := resolveThoughtCurrentSource(ctx, gc, op.ContradictedID)
	if err != nil || len(sources) == 0 {
		// Cannot fetch ground truth → cannot validate → reject: a negation of an
		// unresolvable node has no first-party basis. Fail-closed.
		return fmt.Errorf("cannot resolve current source of %s — negation rejected (no first-party basis to validate the quote against)", op.ContradictedID)
	}
	wantQuote := normalize(op.Quote)
	for _, src := range sources {
		if !strings.Contains(normalize(src.Text), wantQuote) {
			continue
		}
		if op.CitedRange == "" || quoteLocalToRange(src, op.CitedRange) {
			return nil // PASS: verbatim current-source substring, local to the cited range.
		}
	}
	return firstPartyEvidenceError(op.ContradictedID)
}

// firstPartyEvidenceError builds the locked first-party-evidence rejection for a
// contradicted node ID.
func firstPartyEvidenceError(contradictedID string) error {
	return fmt.Errorf(errFirstPartyEvidenceMsg, contradictedID)
}

// rejectNonNegationProofParams returns a non-nil error when a call the gate has
// already determined is NOT a negation nevertheless carries a proof param. It
// names the FIRST non-empty field in a fixed order — verified_quote before
// cited_range — so the same payload always produces the same message, mirroring
// the deterministic first-hit contract accountMutateParams gets from
// rejectedSorted (mutate_param_accounting.go).
//
// WHY THIS LIVES HERE rather than in the arm registry: the registry classifies
// per ARM, but negation-ness is per CALL — it is decided by the relationship /
// status VALUE. A link arm that consumes verified_quote (as it must, since a
// contradicts-link legitimately carries one) would therefore accept-and-ignore
// the same param on relationship:"relates-to", which is exactly the silent-drop
// shape the accounting gate exists to close. The in-tree idiom for a
// value-conditional refinement the table cannot express is a sibling gate — the
// same shape as rejectUnroutableUpdateParams (intercept_mutate_update.go) — and
// this is the one function that already parses both arg structs and already
// knows whether the call is a negation, so it costs no second parse.
func rejectNonNegationProofParams(a mutateArgs, t thinkArgs) error {
	for _, param := range []struct{ name, value string }{
		{"verified_quote", a.VerifiedQuote},
		{"verified_quote", t.VerifiedQuote},
		{"cited_range", a.CitedRange},
		{"cited_range", t.CitedRange},
	} {
		if strings.TrimSpace(param.value) != "" {
			return fmt.Errorf(errNonNegationProofMsg, param.name)
		}
	}
	return nil
}

// normalize collapses every run of whitespace (spaces, tabs, AND newlines) to a
// single space and trims the ends — the canonical strings.Fields/Join idiom. This
// is INTENTIONAL and load-bearing: a quote pulled from a multi-line code body or a
// thought's Summary+Content will differ from the live source only in indentation /
// line breaks, and the gate must still match it. Collapsing newlines is the point,
// NOT a bug to "fix" — a future reader must not narrow this to spaces/tabs only.
func normalize(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// quoteLocalToRange is the COARSE v1 locality check: when a cited_range is
// supplied as "path:start-end", confirm the cited path is consistent with the
// resolved source's path. currentSource carries only {Text, Origin} — it does NOT
// carry the resolved *Node — so the path is parsed out of Origin:
//
//   - Origin "code:<nodeID>" where nodeID = "<filepath>:<Symbol>": strip the
//     "code:" prefix, then take the file path = the substring before the LAST
//     colon of the node ID (a code node ID is path-then-symbol, symbol-last).
//   - Origin "thought:<id>" (the own-content fallback): there is no file path, so
//     a path-scoped cited_range cannot constrain it — treat as local (the gate
//     already enforced existence+currency via the substring match; a code
//     line-range over a thought's own content is not meaningful).
//
// v1 locality is cited-PATH consistency ONLY — full line-range narrowing is the
// documented accepted residual. Pure string check; NO LLM.
func quoteLocalToRange(src currentSource, citedRange string) bool {
	citedPath := pathBeforeLastColon(citedRange)
	if citedPath == "" {
		return true // malformed/empty range constrains nothing.
	}
	origin := src.Origin
	switch {
	case strings.HasPrefix(origin, "code:"):
		nodeID := strings.TrimPrefix(origin, "code:")
		return pathBeforeLastColon(nodeID) == citedPath
	default:
		// thought:<id> own-content source — no file path to constrain.
		return true
	}
}

// pathBeforeLastColon returns the substring before the LAST colon of s, or "" if
// there is no colon. For "path/file.go:Symbol" → "path/file.go"; for
// "path/file.go:120-140" → "path/file.go".
func pathBeforeLastColon(s string) string {
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return ""
	}
	return s[:i]
}

// InterceptNegationGate is the client-side intercept that enforces the gate on
// negation ops BEFORE they reach their write handler. It claims only mutate +
// thoughts tool names, recognizes the negation shape, and:
//
//   - returns (true, errorResult) for a NON-negation call that nevertheless
//     carries a proof param — REJECT, naming the field (rejectNonNegationProofParams);
//   - returns (false, _) for any other NON-negation call (fall through untouched —
//     v1 enables the gate for negation ops only);
//   - returns (false, _) when there is no graph access (nil gc, headless/test
//     fixtures) — FAIL-OPEN to the existing handler, which itself handles nil gc;
//     the gate cannot validate without reads and must not block the daemon's
//     degraded mode;
//   - returns (true, errorResult) when the quote is missing / hallucinated / stale
//     / unresolvable — REJECT, the negation never reaches its write handler;
//   - returns (false, _) when the quote validates — fall through so the real
//     InterceptThoughts / InterceptMutate handler runs the actual negation write.
//
// THE ORDER OF THOSE FIRST TWO AGAINST THE NIL-GC FAIL-OPEN IS LOAD-BEARING.
// The non-negation proof-param reject runs BEFORE the GraphCaller lookup, not
// after. The fail-open below exists for an EPISTEMIC limitation — with no graph
// access the gate cannot read ground truth, so it cannot judge a quote and must
// not block the daemon's degraded mode. That reasoning does not extend to this
// check, which reads NOTHING: it is pure argument shape, decidable from the
// parsed structs alone. So it stays LOUD in degraded mode, where a fail-open
// would silently accept-and-drop exactly the param this gate exists to make
// visible.
//
// It must be wired into runInterceptChainInner BEFORE both InterceptThoughts
// (claims thoughts(think)) and InterceptMutate (claims mutate(update/link)).
func InterceptNegationGate(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "mutate" && params.Name != "thoughts" {
		return false, kgtools.ToolResult{}
	}

	// Parse the relevant arg struct for the tool. On a parse error, fall through
	// (false, {}) — let the real handler surface the parse error (established
	// idiom, mirrors InterceptMutate / interceptThoughtsOp).
	var a mutateArgs
	var t thinkArgs
	switch params.Name {
	case "mutate":
		// mutate passes its flex-open carrier so the rejection keeps the
		// did-you-mean pointer the live mutate gate already emits.
		if err := rejectUndeclaredParams("mutate", "metadata", mutateProperties(), params.Arguments); err != nil {
			return true, errorResult(err.Error())
		}
		if err := json.Unmarshal(params.Arguments, &a); err != nil {
			return false, kgtools.ToolResult{}
		}
	case "thoughts":
		if err := rejectUndeclaredParams("thoughts", "", ThoughtsToolDef().InputSchema.Properties, params.Arguments); err != nil {
			return true, errorResult(err.Error())
		}
		if err := json.Unmarshal(params.Arguments, &t); err != nil {
			return false, kgtools.ToolResult{}
		}
	}

	op, isNeg := recognizeNegationOp(params.Name, a, t)
	if !isNeg {
		// Pure arg-shape check, deliberately ahead of the GraphCaller lookup — see
		// the doc comment above on why this one does not ride the fail-open.
		if err := rejectNonNegationProofParams(a, t); err != nil {
			return true, errorResult(err.Error()) // REJECT — a proof param here routes nothing.
		}
		return false, kgtools.ToolResult{} // not a negation — fall through untouched.
	}

	gc := deps.GraphCaller()
	if gc == nil {
		// No graph access (headless / router-less test fixture) — fail-open to the
		// existing handler. The gate cannot read ground truth here, and blocking
		// would break the daemon's degraded mode.
		return false, kgtools.ToolResult{}
	}

	if err := validateNegationQuote(ctx, gc, op); err != nil {
		return true, errorResult(err.Error()) // REJECT — never reaches the write handler.
	}
	return false, kgtools.ToolResult{} // PASS — fall through to the real negation write.
}
