// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"fmt"
	"path/filepath"
	"strings"
)

// The corpus report's RENDERING half, split out of corpus_verification_test.go
// when that file reached the 500-line lefthook block. The functions moved
// BYTE-IDENTICALLY apart from render's new root parameter and the provenance
// row it writes; the measurement, the walk and the artifact write all stay
// where they were.

// renderECMAScript writes the per-language rows.
func (r *corpusReport) renderECMAScript(b *strings.Builder) {
	for _, lang := range ecmaLanguages {
		row, ok := r.ecma[lang]
		if !ok {
			continue
		}
		fmt.Fprintf(b, "ecma_%s_references=%d\n", lang, row.references)
		fmt.Fprintf(b, "ecma_%s_bound=%d\n", lang, row.bound)
		fmt.Fprintf(b, "ecma_%s_collided_key_resolutions=%d\n", lang, row.collided)
		fmt.Fprintf(b, "ecma_%s_bound_rule_unqualified_import=%d\n", lang, row.boundUnqualifiedImport)
		fmt.Fprintf(b, "ecma_%s_bound_rule_qualified_import=%d\n", lang, row.boundQualifiedImport)
		fmt.Fprintf(b, "ecma_%s_bound_rule_qualified_member=%d\n", lang, row.boundQualifiedMember)
		fmt.Fprintf(b, "ecma_%s_bound_rule_qualified_path=%d\n", lang, row.boundQualifiedPath)
		fmt.Fprintf(b, "ecma_%s_sibling_member_bound=%d\n", lang, row.siblingMemberBound)
		fmt.Fprintf(b, "ecma_%s_ambiguous_groups=%d\n", lang, row.ambiguousGroups)
		fmt.Fprintf(b, "ecma_%s_dynamic_groups=%d\n", lang, row.dynamicGroups)
		fmt.Fprintf(b, "ecma_%s_dynamic_unbound=%d\n", lang, row.dynamicUnbound)
		fmt.Fprintf(b, "ecma_%s_dynamic_member_access=%d\n", lang, row.dynamicMemberAccess)
		fmt.Fprintf(b, "ecma_%s_dynamic_computed_access=%d\n", lang, row.dynamicComputedAccess)
		fmt.Fprintf(b, "ecma_%s_rule_external_qualifier=%d\n", lang, row.externalQualifier)
		fmt.Fprintf(b, "ecma_%s_external=%d\n", lang, row.external)
		fmt.Fprintf(b, "ecma_%s_external_out_of_repo=%d\n", lang, row.outOfRepo)
		fmt.Fprintf(b, "ecma_%s_external_out_of_index=%d\n", lang, row.outOfIndex)
		fmt.Fprintf(b, "ecma_%s_external_no_named_declarations=%d\n", lang, row.noNamedDeclarations)
		fmt.Fprintf(b, "ecma_%s_external_no_binding=%d\n", lang, row.noBinding)
	}
}

// render writes one key=value per line so a gate can anchor on whole lines.
//
// IT TAKES THE CORPUS ROOT because the artifact carried no provenance at all: a
// regeneration pointed at the wrong repository once collapsed a whole language
// family's rows to zero with every gate still green, and it was caught only by
// per-language file-count forensics. The root plus the per-language file counts
// is what makes a drifted comparison visible rather than inferred — the root
// alone does not, since the same path at two different commits is still
// ambiguous.
//
// ONLY THE BASENAME IS STAMPED, never the operator's absolute path. This
// artifact is copied into the public mirror by scripts/sync-to-oss.sh, and a
// shipped artifact carries no personal identifiers or paths; an absolute root
// publishes a home directory layout to every reader of the OSS repo. The
// basename is also what makes the stamp STABLE: the same corpus checked out at
// a different place on a different machine now produces a byte-identical row,
// where the absolute form made the artifact machine-specific.
//
// WHAT THE NARROWER ROW STILL DISTINGUISHES, AND WHAT IT NO LONGER DOES. It is
// still derived from the actual input, so a pair generated from two DIFFERENT
// repositories disagrees here and the matched-pair gates still fire — that is
// the drift described above and it stays caught. What it can no longer tell
// apart is two checkouts of the SAME basename at two different paths; those now
// read identically. files_discovered and the per-language file counts are the
// finer fingerprint for that case, and ful1336_check.awk already compares
// files_discovered across the pair for exactly this reason.
func (r *corpusReport) render(root string, fileCount, chunkNodeCount int) string {
	var b strings.Builder
	b.WriteString("# collector corpus verification — resolution transition\n")
	b.WriteString("#\n")
	b.WriteString("# ambiguous_groups: CLOSED, exactly one of the N targets is correct.\n")
	b.WriteString("# dynamic_groups: OPEN, dispatches to one of these or beyond.\n")
	b.WriteString("# THE TWO ARE NEVER SUMMED. A reader who adds them has reported a\n")
	b.WriteString("# language property (runtime dispatch) as an index gap (ambiguity).\n")
	b.WriteString("# dynamic_unbound counts dynamic references whose scope declares nothing\n")
	b.WriteString("# by that name: no edge, no group — the largest population of all.\n")
	b.WriteString("# dot_scope_binds / dot_scope_groups are RESOLUTION residue, not a census\n")
	b.WriteString("# of the construct: a corpus can carry dot imports that bind nothing, and\n")
	b.WriteString("# those are counted by the binds pass under dot_scopes_filled instead.\n")
	b.WriteString("# test_calls_edges is what the chunker EMITTED as TEST_CALLS;\n")
	b.WriteString("# test_calls_test_to_production is the resolved subset whose target is not\n")
	b.WriteString("# test code. They are their own counters, added to no row above — but they\n")
	b.WriteString("# do NOT make the rows above production-only. Every reference row counts\n")
	b.WriteString("# each edge that is neither CONTAINS nor IMPORTS, and TEST_CALLS rides the\n")
	b.WriteString("# same resolution walk CALLS does, so bound / external_references and the\n")
	b.WriteString("# per-language rows of any language with a TestBlocks query count\n")
	b.WriteString("# test-origin references beside production ones. Read them as ALL\n")
	b.WriteString("# references. The 18 languages with no TestBlocks query emit no TEST_CALLS\n")
	b.WriteString("# at all, which is why the go_ rows did not move when this row pair landed.\n")
	b.WriteString("#\n")
	fmt.Fprintf(&b, "corpus_root=%s\n", filepath.Base(root))
	fmt.Fprintf(&b, "files_discovered=%d\n", fileCount)
	fmt.Fprintf(&b, "chunk_nodes=%d\n", chunkNodeCount)
	fmt.Fprintf(&b, "cross_file_contains=%d\n", r.crossFileContains)
	fmt.Fprintf(&b, "same_file_contains=%d\n", r.sameFileContains)
	fmt.Fprintf(&b, "uncontained_node_chunks=%d\n", r.uncontained)
	fmt.Fprintf(&b, "collided_key_resolutions=%d\n", r.collided)
	fmt.Fprintf(&b, "bound=%d\n", r.stats.Bound)
	fmt.Fprintf(&b, "external_references=%d\n", r.stats.External)
	fmt.Fprintf(&b, "ambiguous_groups=%d\n", r.stats.AmbiguousGroups)
	fmt.Fprintf(&b, "ambiguous_edges=%d\n", r.stats.AmbiguousEdges)
	fmt.Fprintf(&b, "distinct_ambiguous_group_keys=%d\n", len(r.ambiguousKeys))
	fmt.Fprintf(&b, "dynamic_groups=%d\n", r.stats.DynamicGroups)
	fmt.Fprintf(&b, "dynamic_edges=%d\n", r.stats.DynamicEdges)
	fmt.Fprintf(&b, "distinct_dynamic_group_keys=%d\n", len(r.dynamicKeys))
	fmt.Fprintf(&b, "max_dynamic_group_size=%d\n", r.stats.MaxDynamicGroup)
	fmt.Fprintf(&b, "dynamic_unbound=%d\n", r.stats.DynamicUnbound)
	fmt.Fprintf(&b, "dot_scope_binds=%d\n", r.stats.DotScopeBinds)
	fmt.Fprintf(&b, "dot_scope_groups=%d\n", r.stats.DotScopeGroups)
	r.renderTestCalls(&b)

	r.renderSiblingSkip(&b)
	r.renderECMAScript(&b)
	r.renderGo(&b)

	for _, k := range sortedKeys(r.byEdgeType) {
		fmt.Fprintf(&b, "edges_%s=%d\n", k, r.byEdgeType[k])
	}
	for _, k := range sortedKeys(r.byLanguage) {
		fmt.Fprintf(&b, "files_%s=%d\n", k, r.byLanguage[k])
	}
	return b.String()
}
