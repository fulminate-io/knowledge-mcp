// SPDX-License-Identifier: Apache-2.0

// intercept_mutate_update_receipt.go — the mutate(update) render tail: a RECEIPT
// naming what the typed update forwarded, plus the non-fatal
// parameter-shaped-tail advisory over the caller's text fields.
//
// WHY A RECEIPT. When a caller's tool-call serialization loses a parameter, the
// handler is reached with that parameter simply ABSENT: the update succeeds and
// the response says only that some node was updated. Nothing in that response
// distinguishes "you sent description and it was written" from "your description
// never arrived". Enumerating the fields and metadata keys the call forwarded
// makes a narrowed write STATED rather than merely unmentioned — from any cause,
// not only the serialization one. A parameter that never reached the tool cannot
// be recovered here; it can only be made visible.
//
// THE RECEIPT IS THE SECOND LINE OF DEFENSE, not the first. The serialization
// failures this receipt was built to expose are now REFUSED before any write when
// their shape is provable — a text field carrying its own closing tag with the
// remainder running to the end of the value (rejectSwallowedParamValues,
// swallowed_param_gate.go, called from accountMutateParams). So no receipt is
// rendered for those calls at all. What still reaches here is every OTHER cause
// of a narrowed write, which no gate can prove and only enumeration can show.
//
// THE LABELS SAY "FORWARDED BY THIS CALL" ON PURPOSE. The receipt is built
// client-side from the forward this handler constructed. It is NOT a read-back
// of stored state, and the two can diverge — a server may overwrite a
// caller-supplied value on write (an `author` value was observed being replaced
// exactly that way). For the caller-side narrowing this receipt targets the
// proxy is exact, because a parameter that was never transmitted cannot appear
// in the forward; it is not evidence about a server-side drop, and only a
// read-back settles that half.
//
// SCOPE FENCE: the advisory scans the three TEXT fields only, never metadata
// VALUES. A metadata value legitimately carries arbitrary text — grep patterns,
// shell fragments, command and gate-script bodies routinely contain angle
// brackets and quoting — so scanning them would fire on correct data. The
// omission is deliberate, not an oversight.

package tools

import (
	"fmt"
	"sort"
	"strings"
)

// The two summary dispositions a typed update can report — assigned by
// resolveTypedUpdateSummary (intercept_mutate_update_summary.go), which is the
// only place the summary rule lives. Spelled here and nowhere else.
//
// `unchanged` is what makes the seam's SILENT decision observable to a caller
// rather than merely absent from the response: no summary was supplied, so the
// stored one was left alone. Saying so, and naming the way to replace it, is
// what stops a caller reading an untouched summary as one this call rewrote.
const (
	summaryDispositionCallerSupplied = "caller-supplied"
	summaryDispositionUnchanged      = "unchanged (no summary supplied; the stored one is untouched — pass an explicit summary to replace it)"
)

// renderTypedUpdateReceipt builds the success response for a typed update: the
// existing one-line confirmation, the receipt lines naming what this call
// forwarded, and the standard `## Warnings` section carrying any
// parameter-shaped-tail advisories.
//
// The parameter-shaped-tail advisory runs over the CALLER-SUPPLIED text fields,
// each passed under its own wire name. The name is load-bearing twice: it is
// what the message reports, and it is what builds the closing tag
// paramShapedTailWarning's detection predicate looks for — a field passed under
// any other spelling silently loses bare-dialect detection for that field.
func renderTypedUpdateReceipt(a mutateArgs, fwd forwardedTypedUpdatePayload, sr summaryResolution) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "mutate(update): updated %s [graph: knowledge/default]", a.ID)
	fmt.Fprintf(&sb, "\nFields forwarded by this call: %s", joinOrNone(forwardedFieldNames(fwd)))
	fmt.Fprintf(&sb, "\nMetadata keys forwarded by this call: %s", joinOrNone(sortedMetadataKeys(fwd.Metadata)))
	fmt.Fprintf(&sb, "\nSummary: %s", sr.disposition)

	var warnings []string
	for _, f := range []struct{ name, text string }{
		{"summary", a.Summary},
		{"description", a.Description},
		{"content", a.Content},
	} {
		if w := paramShapedTailWarning(f.name, f.text); w != "" {
			warnings = append(warnings, w)
		}
	}
	writeClientWarningsSection(&sb, warnings)
	return sb.String()
}

// forwardedFieldNames returns the sorted set_fields keys this forward will
// produce, mirroring engine.updateSetFields' allowlist and its gating rules
// exactly — including that STATUS keys on POINTER PRESENCE rather than
// non-emptiness, because an explicit status:"" is a clear-to-blank write while
// an absent status leaves the field untouched. A divergence here would make the
// receipt describe a write that did not happen.
func forwardedFieldNames(fwd forwardedTypedUpdatePayload) []string {
	var names []string
	if fwd.Status != nil {
		names = append(names, "status")
	}
	for _, f := range []struct{ name, value string }{
		{"name", fwd.Name},
		{"description", fwd.Description},
		{"summary", fwd.Summary},
		{"content", fwd.Content},
		{"keywords", fwd.Keywords},
		{"source", fwd.Source},
	} {
		if f.value != "" {
			names = append(names, f.name)
		}
	}
	sort.Strings(names)
	return names
}

// sortedMetadataKeys returns m's keys in sorted order. Sorted because Go map
// iteration order is randomized: an unsorted list makes the receipt untestable
// and makes the tell itself harder to read across two responses. Named for the
// metadata map specifically because the package's test tree already declares a
// bool-valued sortedKeys.
func sortedMetadataKeys(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// joinOrNone renders a comma-separated list, or the literal `none` when empty.
// The none case renders rather than omitting the line: an absent line is the
// weakest available tell and is exactly what let narrowed writes go unnoticed.
func joinOrNone(items []string) string {
	if len(items) == 0 {
		return "none"
	}
	return strings.Join(items, ", ")
}
