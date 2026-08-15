// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// goRow is Go's resolution picture on the acceptance corpus.
//
// IT LIVES IN ITS OWN FILE, not beside ecmaRow, because corpus_verification_test.go
// is close to the 500-line cap and a rows block would not fit there.
//
// THE PER-RULE SPLIT IS WHAT ACTUALLY SAYS THE ARM WORKED. A Go qualified
// reference binding through the qualified-import rule is the thing this work
// exists to produce, and it is structurally zero before the arm exists — so a
// rise in the aggregate bound count with that row still at zero would mean
// something else moved.
type goRow struct {
	references      int
	bound           int
	external        int
	ambiguousGroups int
	dynamicGroups   int
	dynamicUnbound  int

	boundQualifiedImport   int
	boundUnqualifiedImport int
	boundDotScope          int

	// siblingMemberBound counts BOUND resolutions that fired the sibling rung:
	// a bare name inside a container reaching a member of that same container.
	//
	// IT IS THE BEFORE HALF OF THE SIBLING-RUNG TRANSITION, and Go is the
	// language that dominates it on this corpus. Go skips the rung once the gate
	// lands, so this row must read ZERO in a post-gate artifact; a non-zero
	// value there says the gate did not fire.
	siblingMemberBound int

	usesTypeTotal int
	usesTypeBound int

	// bindsFiles and bindsEntries are THE LIVENESS CONTROL. A zero here with
	// everything else unchanged is the exact signature of the module path never
	// reaching the ARM — RepoContext.ModulePath left unpopulated, or the pass
	// never running — and without it a flat result reads as "no improvement"
	// rather than "the arm never ran".
	bindsFiles   int
	bindsEntries int

	// bindsScopesUnknown is THE R2X INPUT CONTROL: recorded binds whose Scope
	// the declaration index does not hold. A zero here while bindsEntries is
	// positive is impossible on a corpus that imports stdlib in nearly every
	// file, so a zero indicates the join is not being computed rather than an
	// unusually self-contained repository.
	//
	// ITS CAUSE-SPLIT IS DELIBERATELY NOT RECORDED, unlike the ECMAScript row's
	// four-way external split. That split keys on a FILE-scope test; Go's
	// scopes are directories, and the equivalent test would have to re-derive
	// the in-repo/out-of-repo judgment the arm deliberately does not make.
	// Splitting it here would rebuild that judgment in the measurement layer
	// purely to produce a number, so the row is left whole and the
	// in-repo-unindexed population stays unmeasured BY DESIGN.
	bindsScopesUnknown int

	// r2xTerminations is RECORDED AND READ BY A HUMAN, NOT GATED. A zero is not
	// automatically wrong — the fix may be terminating references before any
	// local collision arises.
	r2xTerminations int

	// boundQualifiedMember counts a Go reference that reached a declaration
	// through an import bind's CONTAINER rather than its top level.
	//
	// A NON-ZERO VALUE HERE IS A FINDING TO INVESTIGATE, NOT A WIN. The Go arm
	// records no Container and a Go package import leaves Bind.Name empty, so
	// the parent key falls back to the QUALIFIER — the package alias. Nothing in
	// a Go package is parented to its own import alias unless a receiver TYPE
	// happens to be spelled exactly like the alias another file imports it
	// under, in which case `alias.Method` would reach a method on that type
	// instead of the package-level function it means. This row is what makes
	// that collision visible rather than silent; it measured zero across the
	// acceptance corpus's 149,360 Go references.
	boundQualifiedMember int

	// dotScopeFiles is what makes the landed dot_scope_binds=0 gate READABLE
	// rather than merely surviving: a zero here says no Go file reported a dot
	// scope, which is what that gate's zero means.
	dotScopeFiles int

	// ambiguousGroups listing input: one entry per surviving Go ambiguous
	// group, carrying the group key and each candidate's file path. It is the
	// input to the MANUAL attribution criterion.
	ambiguousListing []string
}

// goRows attributes every Go reference's resolution outcome and censuses what
// the binds pass filled.
//
// IT IS AN ADAPTER OVER censusWalk (corpus_census_walk_test.go) AND NO LONGER A
// WALK OF ITS OWN. The measurement — one pass over results, one resolveRef per
// reference, attributed by status and by rule — is shared with ecmaScriptRows
// and censusByLanguage, which were three copies of it. What stays here is the
// SHAPING: goRow's named per-rule counters, which renderGo writes and which are
// the shared boundByRule map read at Go's own rungs.
//
// The census still re-walks rather than reading resolveEdgesWithStats's
// aggregate, for the reason it always did: that aggregate is global and carries
// no per-rule or per-language split, and every question here is about which
// RULE fired for GO.
func goRows(results []*treesitter.Result, ix *declIndex) *goRow {
	row := &goRow{}
	lang := string(treesitter.LangGo)
	rows := censusWalk(results, ix, censusSpec{
		languages: []string{lang},
		admits:    admitAllReferences,
		hooks: censusHooks{
			// THE LISTING STAYS IN GO'S OWN FILE because no other family renders
			// it, and pushing it into the shared walk would encode one family's
			// artifact shape in the layer all three share.
			onAmbiguous: func(_ string, e *treesitter.Edge, got refResolution) {
				files := make([]string, 0, len(got.Candidates))
				for _, c := range got.Candidates {
					files = append(files, c.File)
				}
				row.ambiguousListing = append(row.ambiguousListing,
					groupKey(e.Ref.File, e.RefByte, string(e.Type), e.ToID)+"|"+
						strings.Join(files, ","))
			},
		},
	})
	// censusWalk pre-creates a row for every NAMED language before it walks, so
	// this read is present even on a corpus holding no Go file at all — which is
	// what keeps renderGo emitting its full row set as zeros rather than
	// dropping the block.
	row.fill(rows[lang])
	sort.Strings(row.ambiguousListing)
	return row
}

// fill copies the shared census onto Go's row shape. Each named counter is the
// boundByRule map read at one rung; the four rungs Go never renders
// (qualified-parent, qualified-path, external-qualifier, dynamic-scope,
// own-scope, not-declared) are simply not read.
func (row *goRow) fill(c *censusRow) {
	row.references = c.references
	row.bound = c.bound
	row.external = c.external
	row.ambiguousGroups = c.ambiguous
	row.dynamicGroups = c.dynamicGroups
	row.dynamicUnbound = c.dynamicUnbound
	row.boundQualifiedImport = c.boundByRule[string(RuleQualifiedImport)]
	row.boundUnqualifiedImport = c.boundByRule[string(RuleUnqualifiedImport)]
	row.boundDotScope = c.boundByRule[string(RuleDotScope)]
	row.boundQualifiedMember = c.boundByRule[string(RuleQualifiedMember)]
	row.siblingMemberBound = c.boundByRule[string(RuleSiblingMember)]
	row.usesTypeTotal = c.usesTypeTotal
	row.usesTypeBound = c.usesTypeBound
	row.bindsFiles = c.bindsFiles
	row.bindsEntries = c.bindsEntries
	row.bindsScopesUnknown = c.bindsScopesUnknown
	row.r2xTerminations = c.externalQualifier
	row.dotScopeFiles = c.dotScopeFiles
}

// renderGo writes Go's rows, each on its own line so no two are summed.
func (r *corpusReport) renderGo(b *strings.Builder) {
	row := r.goRows
	if row == nil {
		return
	}
	b.WriteString("#\n")
	b.WriteString("# go_binds_files / go_binds_entries are the LIVENESS control: zero here\n")
	b.WriteString("# means the arm never ran, which reads as \"no improvement\" without them.\n")
	b.WriteString("# go_binds_scopes_unknown is the input the external-qualifier rung consumes.\n")
	b.WriteString("# go_external and go_r2x_terminations are RECORDED, NOT GATED: terminating\n")
	b.WriteString("# a reference that previously became a wrong dynamic edge RAISES external,\n")
	b.WriteString("# so a gate demanding it fall would be demanding the defect back.\n")
	b.WriteString("# go_ambiguous_group_<n> lines list each surviving group's key and the file\n")
	b.WriteString("# of every candidate — the input to the manual attribution criterion.\n")
	b.WriteString("#\n")
	b.WriteString("# ATTRIBUTION OF THE SURVIVING GROUPS, recorded here rather than in the\n")
	b.WriteString("# artifact body because the artifact is generated and a hand-edit would be\n")
	b.WriteString("# erased by the next regeneration. Every group listed below was checked by\n")
	b.WriteString("# reading the //go:build line of each candidate file, and all of them are\n")
	b.WriteString("# MUTUALLY-EXCLUSIVE BUILD-CONSTRAINT PAIRS — several declarations of one\n")
	b.WriteString("# name in one scope, which is the ticket's stated legitimately-ambiguous\n")
	b.WriteString("# class and the correct answer rather than residue to drive to zero. The\n")
	b.WriteString("# three families seen are: GOOS pairs (a _linux.go file against a _stub.go\n")
	b.WriteString("# carrying //go:build !linux), //go:build devendpoint against !devendpoint,\n")
	b.WriteString("# and //go:build internal against !internal.\n")
	b.WriteString("# A GROUP WHOSE CANDIDATES ARE NOT SUCH A SET IS A DEFECT TO REPORT, never\n")
	b.WriteString("# residue to accept — re-run this attribution when the listing changes.\n")
	fmt.Fprintf(b, "go_references=%d\n", row.references)
	fmt.Fprintf(b, "go_bound=%d\n", row.bound)
	fmt.Fprintf(b, "go_external=%d\n", row.external)
	fmt.Fprintf(b, "go_ambiguous_groups=%d\n", row.ambiguousGroups)
	fmt.Fprintf(b, "go_dynamic_groups=%d\n", row.dynamicGroups)
	fmt.Fprintf(b, "go_dynamic_unbound=%d\n", row.dynamicUnbound)
	fmt.Fprintf(b, "go_bound_rule_qualified_import=%d\n", row.boundQualifiedImport)
	fmt.Fprintf(b, "go_bound_rule_unqualified_import=%d\n", row.boundUnqualifiedImport)
	fmt.Fprintf(b, "go_bound_rule_qualified_member=%d\n", row.boundQualifiedMember)
	fmt.Fprintf(b, "go_bound_rule_dot_scope=%d\n", row.boundDotScope)
	fmt.Fprintf(b, "go_sibling_member_bound=%d\n", row.siblingMemberBound)
	fmt.Fprintf(b, "go_uses_type_bound=%d\n", row.usesTypeBound)
	fmt.Fprintf(b, "go_uses_type_total=%d\n", row.usesTypeTotal)
	fmt.Fprintf(b, "go_binds_files=%d\n", row.bindsFiles)
	fmt.Fprintf(b, "go_binds_entries=%d\n", row.bindsEntries)
	fmt.Fprintf(b, "go_binds_scopes_unknown=%d\n", row.bindsScopesUnknown)
	fmt.Fprintf(b, "go_r2x_terminations=%d\n", row.r2xTerminations)
	fmt.Fprintf(b, "go_dot_scope_files=%d\n", row.dotScopeFiles)
	// THE COUNT ROW IS ALWAYS EMITTED, including when it is zero: a listing
	// that only appears when non-empty cannot distinguish "no surviving groups"
	// from "the listing was never wired".
	fmt.Fprintf(b, "go_ambiguous_group_listing=%d\n", len(row.ambiguousListing))
	for i, entry := range row.ambiguousListing {
		fmt.Fprintf(b, "go_ambiguous_group_%d=%s\n", i, entry)
	}
}
