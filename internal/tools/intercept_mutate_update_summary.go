// SPDX-License-Identifier: Apache-2.0

// intercept_mutate_update_summary.go — the SUMMARY SEAM for mutate(update) on a
// typed knowledge node.
//
// WHY ONE SEAM. Deciding what summary a typed update forwards is ONE logical
// operation, and it used to have three different meanings depending on which
// path a caller happened to be on: the criterion path REFUSED an over-cap
// derivation, mutate(create) CLAMPED at a word boundary with a warning, and this
// update path wrote an UNCAPPED derivation over whatever summary was already
// stored — measured at 2,302 characters against a 500-character cap, destroying
// a caller-authored 489-character summary that the call never named. A caller who
// learned one path's rule predicted the wrong behavior on the next. The rule now
// lives here, once.
//
// THE RULE, now two parts rather than four:
//  1. a caller-supplied summary wins, verbatim and unvalidated;
//  2. everything else forwards NOTHING, leaving the stored summary untouched.
//
// NOTHING DERIVES ANY MORE. Every summary on a tool-created knowledge node is
// written by the authoring LLM, so this seam has no derivation to gate, no
// stored value to protect from one, and no derived text to clamp. The per-type
// derive-source table, the re-derivation and its clamp are gone rather than
// disabled — the type-by-type divergence they encoded is gone with them.
//
// A criterion's NAME is still derived from the first line of its description and
// a caller-supplied name is still rejected outright
// (rejectUnroutableUpdateParams) — that is the name, not the summary.

package tools

// summaryResolution is what this seam decided for one typed update: the summary
// value to forward (empty means forward nothing, which leaves the stored one
// untouched) and the disposition to report on the receipt.
//
// The disposition is set on the BRANCH ACTUALLY TAKEN rather than inferred from
// whether summary came out non-empty.
type summaryResolution struct {
	summary     string
	disposition string
}

// resolveTypedUpdateSummary applies the two-part rule in this file's header to
// one typed update. It reads the CALL alone: with nothing deriving, neither the
// node's type nor its stored state can change what this call forwards.
//
// When no summary is supplied the forwarded summary is the empty string, and
// engine.updateSetFields omits an empty summary from set_fields, so the stored
// summary is neither read, re-derived, validated nor rewritten.
//
// The caller-supplied branch returns the value verbatim at any length — an
// explicit summary is never clamped on the update path.
func resolveTypedUpdateSummary(a mutateArgs) summaryResolution {
	if a.Summary != "" {
		return summaryResolution{summary: a.Summary, disposition: summaryDispositionCallerSupplied}
	}
	return summaryResolution{disposition: summaryDispositionUnchanged}
}
