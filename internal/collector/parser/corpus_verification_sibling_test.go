// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// THE SIBLING-RUNG TRANSITION'S PRODUCER, and the artifact row the release
// decision reads.
//
// WHY THE PER-LANGUAGE sibling_member_bound ROWS CANNOT BE THE PRODUCER. Those
// come from the two verifiers, which count resolutions whose rule was
// RuleSiblingMember — and every language they walk (go plus the three
// ECMAScript ones) SKIPS that rung, so after the gate they all read zero. A
// zero is the right answer for "did the gate fire" and the wrong one for "how
// much did it move": rendering the transition from them would put a zero in the
// artifact exactly when the fix works.
//
// WHAT THIS MEASURES INSTEAD is the population the gate SUPPRESSED — references
// that would have bound through the sibling rung and now do not. It is
// numerically the before run's sibling-bound count, and measuring it in the
// AFTER run is what keeps the generated artifact regeneration-stable rather
// than carrying a hand-copied number that the next regeneration would erase.
//
// AND IT IS GLOBAL, NOT A SUM OF THE PER-LANGUAGE ROWS. PYTHON is the fifth
// gated language and neither verifier walks it, while its references ARE inside
// the global bound and external_references totals the conservation gate reads.
// A sum over the verifier rows would understate the transition and would not
// reconcile with that arithmetic.

// siblingSkipCensus is the suppressed population, split by language and by what
// the rung would have concluded.
type siblingSkipCensus struct {
	// bound counts suppressed references the rung would have BOUND — the
	// population that moves between the bound and external totals, which is why
	// it and not the total is what the artifact's headline row states.
	bound int
	// ambiguous counts suppressed references the rung would have made an
	// AMBIGUOUS GROUP of. It is reported separately and never summed into
	// bound: an ambiguous group leaves the ambiguous rows rather than the bound
	// ones, so folding the two together would silently break the conservation
	// arithmetic it sits beside.
	ambiguous int

	byLanguage map[string]int
}

// censusSiblingSkips walks every reference of every language and asks the
// PRODUCTION rung what it would have answered before the gate existed.
//
// IT DOES NOT RE-EXPRESS THE RUNG'S LOOKUP. resolveLocalScopes takes the
// language profile as a parameter, so a zero-value langProfile — whose
// SkipSiblingRung is false — turns the real rung back on for one call. A
// hand-written copy of the lookup here would be a second precedence expression
// free to drift from the one it measures.
func censusSiblingSkips(results []*treesitter.Result, ix *declIndex) *siblingSkipCensus {
	c := &siblingSkipCensus{byLanguage: map[string]int{}}
	for _, result := range results {
		for i := range result.Edges {
			e := &result.Edges[i]
			if kgtypes.EdgeType(e.Type) == kgtypes.EdgeContains ||
				kgtypes.EdgeType(e.Type) == kgtypes.EdgeImports || e.Ref == nil {
				continue
			}
			got, ok := ungatedSiblingRung(ix, e.Ref, e.ToID)
			if !ok || got.Rule != RuleSiblingMember {
				continue
			}
			if got.Status == RefBound {
				c.bound++
				c.byLanguage[string(result.Language)]++
				continue
			}
			c.ambiguous++
		}
	}
	return c
}

// ungatedSiblingRung replays ONE reference's bare-name rungs with the sibling
// rung forced on, reproducing resolveRef's own preamble — split, base name,
// suffix narrowing — and resolveUnqualified's own rung ORDER.
//
// THE IMPORT-FIRST CHECK IS NOT OPTIONAL. In go and the ECMAScript languages R4
// runs ahead of the bare-name rungs, so a reference an import already bound
// never reached the sibling rung even before the gate; counting it would report
// a suppression that never happened.
func ungatedSiblingRung(ix *declIndex, ref *treesitter.RefSite, target string) (refResolution, bool) {
	if ref == nil || target == "" || ref.Parent == "" {
		return refResolution{}, false
	}
	prof := profileFor(ref.Lang)
	if !prof.SkipSiblingRung {
		// The rung still applies in this language, so nothing was suppressed.
		return refResolution{}, false
	}
	qualifier, rawName := splitQualifier(ref.Lang, target)
	if qualifier != "" {
		// A qualified reference never reaches the bare-name rungs at all.
		return refResolution{}, false
	}
	name := baseDeclName(rawName)
	if name == "" {
		return refResolution{}, false
	}
	narrow := func(c []*declRec) []*declRec { return filterBySuffixedName(c, rawName) }
	if prof.ImportsBeatLocals {
		if _, ok := resolveImportBound(ix, ref, name, narrow); ok {
			return refResolution{}, false
		}
	}
	// The zero value is the UNGATED profile: SkipSiblingRung false is the
	// behavior every language had before this column existed.
	return resolveLocalScopes(ix, ref, name, narrow, langProfile{})
}

// renderSiblingSkip writes the transition rows.
//
// THE HEADLINE KEY IS STATED VERBATIM because a release gate greps this exact
// string: sibling_rung_skipped_refs. A genuine zero is a FAILURE and not a
// quiet pass — it means either the gate is not firing or the corpus holds no
// bare-name-matches-sibling reference at all, which would be surprising for a
// Go corpus this size. The two are told apart by the per-language fixtures, and
// either is a finding to surface rather than a number to record and move past.
func (r *corpusReport) renderSiblingSkip(b *strings.Builder) {
	c := r.siblingSkips
	if c == nil {
		return
	}
	b.WriteString("#\n")
	b.WriteString("# sibling_rung_skipped_refs is the population the per-language sibling\n")
	b.WriteString("# gate SUPPRESSED: bare names that would have bound to a member of their\n")
	b.WriteString("# own container in a language whose bare call carries no implicit\n")
	b.WriteString("# receiver. It is the BEFORE side of the transition, measured in the\n")
	b.WriteString("# after run so this generated file stays regeneration-stable.\n")
	b.WriteString("# ITS COUNTERPART IS <lang>_sibling_member_bound, which must read ZERO\n")
	b.WriteString("# for every gated language: one row says how much moved, the other says\n")
	b.WriteString("# the gate fired. A non-zero counterpart means it did not.\n")
	b.WriteString("# The ambiguous row is NEVER summed into the headline — a suppressed\n")
	b.WriteString("# ambiguous group leaves the ambiguous rows, not the bound ones.\n")
	fmt.Fprintf(b, "sibling_rung_skipped_refs=%d\n", c.bound)
	fmt.Fprintf(b, "sibling_rung_skipped_ambiguous_refs=%d\n", c.ambiguous)
	for _, lang := range sortedKeys(c.byLanguage) {
		fmt.Fprintf(b, "sibling_rung_skipped_%s=%d\n", lang, c.byLanguage[lang])
	}
}
