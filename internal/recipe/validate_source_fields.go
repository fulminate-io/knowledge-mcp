// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"fmt"
	"sort"
	"strings"
)

// validate_source_fields.go — the FIELD-PATH half of the source census, split
// out of validate_source.go when that file crossed the repo's 500-line ceiling.
//
// THE SPLIT IS BY QUESTION ASKED, not by size. validate_source.go answers "is
// this RULE legal against the loaded graph" — node types on select, edge types
// on traverse and walk, the where-tree, and the switch's own completeness. This
// file answers "is this FIELD PATH legal", which is a different question against
// four different vocabularies: node metadata keys, the well-known Node struct
// fields, the edge attributes and Evidence keys, and the dotted names a rule
// stamps on a row.
//
// WHAT ACTUALLY MOVED, stated exactly because an earlier version of this header
// claimed all nine declarations arrived verbatim and that is not true:
//   - MOVED UNCHANGED (3): wellKnownNodeFields, checkEdgeFieldPath,
//     sortedVocabulary.
//   - MOVED AND EDITED (1): checkFieldPath, which gained the pseudo-variable
//     branch above the length guard and lost the head-keyed skip below it.
//   - REPLACED (2): pseudoVariableNamespaces and isPseudoVariableNamespace, which
//     held and read HEADS, became the name-keyed pair below.
//   - NEW (5): pseudoVariableNames, isPseudoVariableName, isPseudoVariableHead,
//     sortedPseudoVariableNames, checkPseudoVariable.
// The BEHAVIOUR change rides the edited and new declarations, not the move: a
// name under a declared namespace that no rule stamps is now refused instead of
// silently admitted, and a BARE `walk` or `group` is refused where 9b8a0609
// admitted `group` silently.

// wellKnownNodeFields are the field-path tails readNodeField answers from the
// Node struct itself. They are legal on every graph, whatever the corpus
// stamped, so they are never censused against metadata keys.
var wellKnownNodeFields = map[string]bool{
	"type": true, "symbol_name": true, "name": true, "summary": true,
	"description": true, "content": true, "source": true, "status": true,
	"id": true, "body": true,
}

// checkFieldPath censuses the TAIL of one field path against the metadata-key
// vocabulary.
//
// THE HEAD IS NOT CENSUSED HERE — parse-time head validation already refused a
// head that names no row. ONE class of head is skipped entirely: a `$var`
// reference, because a binding holds a scalar whose key the graph never carried.
// A PSEUDO-VARIABLE NAMESPACE is not skipped; it is censused against its own
// declared names by checkPseudoVariable, because the names under it are values a
// rule stamps rather than keys the corpus stamped, and neither vocabulary can
// judge the other.
func (v *sourceValidator) checkFieldPath(path string, pos Position, site string) {
	segments := splitFieldPath(path)
	if len(segments) == 0 {
		return
	}
	head := segments[0]
	// A BARE `edge` IS HANDLED BEFORE THE LENGTH GUARD, and the reason is the
	// CENSUS rather than the read. The guard below returns early for any
	// single-segment path, so a bare `edge` falling through it would never reach
	// checkEdgeFieldPath and no pre-walk refusal would be recorded at all. It
	// would then fail at ROW time, with no position and outside the collected
	// violation set — and on a rowset that is EMPTY by the time the read runs, it
	// would not fail at all: rows=0 with no message, which is precisely the
	// silence this file's header says the pre-walk layer exists to end.
	//
	// IT IS NOT ABOUT A SYMBOL-NAME ANSWER. An earlier version of this comment
	// said the fall-through would send `edge` to the node read and answer a row's
	// name; that is wrong, and measuring it is what disproved it. evalField
	// dispatches head == edgeHead before the node read unconditionally, so a bare
	// `edge` never reaches readNodeField under either order. The sibling comment
	// below says the same thing about a pseudo-variable head and IS correct
	// there, because evalField has no such early dispatch for `walk`.
	if head == edgeHead {
		v.checkEdgeFieldPath(segments, pos, site+" "+path)
		return
	}
	// A PSEUDO-VARIABLE HEAD IS ALSO HANDLED BEFORE THE LENGTH GUARD, and for the
	// same reason the edge head is: the guard exists for a bare NODE head, where
	// `section` alone legally reads the row's name. A bare `walk` has no such
	// default, so falling through would answer a row's symbol name for a question
	// about a pseudo-variable.
	if isPseudoVariableHead(head) {
		v.checkPseudoVariable(path, pos, site+" "+path)
		return
	}
	if len(segments) < 2 {
		return
	}
	if strings.HasPrefix(head, "$") {
		return
	}
	rest := segments[1:]
	key := rest[0]
	if key == "metadata" {
		if len(rest) < 2 {
			return
		}
		key = rest[1]
	} else if wellKnownNodeFields[key] {
		return
	}
	if !contains(v.census.metaKeys, key) {
		v.refuseVocabulary(pos, site+" "+path, censusMetaKey, key)
	}
}

// checkEdgeFieldPath censuses an `edge.…` read against the FOURTH source
// vocabulary: the Evidence keys the loaded graph's edges actually carry.
//
// THIS IS THE BAD-INPUT HALF OF THE ABSENT-VALUE RULE, and its counterpart lives
// in the evaluator. A key the source graph's vocabulary does not carry — no edge
// in the loaded graph ever stamped it — is BAD INPUT and is refused here, before
// the walk. A censused key merely MISSING ON ONE EDGE is a FALSE PREDICATE and
// reads empty in readEdgeField.
//
// THE REFUSAL CARRIES A SECOND LINE NAMING THE EDGE'S OWN FIELDS. The read an
// author is likeliest to get wrong is an Edge STRUCT field this leaf does not
// expose — `edge.weight` is the measured example — and a message listing only
// observed Evidence keys would never mention `edge.type`, leaving the author to
// guess that the field they want has a different home entirely.
func (v *sourceValidator) checkEdgeFieldPath(segments []string, pos Position, site string) {
	spellings := fmt.Sprintf("%s.type, %s.%s.<key>, or %s.<key> for an evidence key",
		edgeHead, edgeHead, edgeEvidenceKey, edgeHead)
	if len(segments) < 2 {
		v.add(pos, "%s: `%s` names no edge attribute — read %s. "+
			"The run was refused before the walk rather than answered with zero rows",
			site, edgeHead, spellings)
		return
	}
	key := segments[1]
	if wellKnownEdgeFields[key] {
		return
	}
	if key == edgeEvidenceKey {
		if len(segments) < 3 {
			v.add(pos, "%s: `%s.%s` names no key — read %s.%s.<key>. "+
				"The run was refused before the walk rather than answered with zero rows",
				site, edgeHead, edgeEvidenceKey, edgeHead, edgeEvidenceKey)
			return
		}
		key = segments[2]
	}
	if contains(v.census.edgeEvidenceKeys, key) {
		return
	}
	v.refuseVocabulary(pos, site, censusEdgeEvidenceKey, key)
	v.add(pos, "%s: the edge's own fields are read as %s, and they are not evidence keys",
		site, joinPlain(sortedVocabulary(wellKnownEdgeFields)))
}

// sortedVocabulary renders a well-known field set deterministically. Go
// randomizes map iteration, which is why sourceValidator.result sorts its
// violations at all; a message assembled from an unsorted walk would vary run to
// run in exactly the same way.
func sortedVocabulary(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, edgeHead+"."+k)
	}
	sort.Strings(out)
	return out
}

// pseudoVariableNames are the FULL DOTTED NAMES a rule stamps on a row rather
// than keys any corpus stamped on a node: `group.keys` by the group_by rule, and
// `walk.depth` / `walk.position` by the walk rule. Censusing them against the
// source graph's metadata keys would refuse correct recipes, which is why they
// are declared here.
//
// IT IS A SET OF NAMES RATHER THAN A SET OF HEADS, and that is the second
// narrowing this declaration has needed. `group` was first a hardcoded string
// comparison, which is exactly how `walk` came to be missed. Widening it to a
// set of HEADS fixed that and opened a hole of its own: every name under `walk.`
// was admitted, so a typo — `walk.levl` — parsed, validated, and read back empty
// on every row, and a write run emitted that empty value as metadata with no
// error and no counter. Names are the narrowest true statement of what a rule
// stamps, so a typo is refused by the same path an unknown metadata key is.
//
// A RULE THAT STAMPS A ROW-SCOPED VALUE ADDS EVERY DOTTED NAME IT STAMPS to this
// one declaration, and its HEAD to the head scope in parser_heads.go. Nowhere
// else: the head set below is derived from these names rather than declared
// beside them, so the two cannot drift apart.
var pseudoVariableNames = map[string]bool{
	"group.keys":    true,
	"walk.depth":    true,
	"walk.position": true,
}

// isPseudoVariableName reports whether a full dotted path is one of the declared
// names.
func isPseudoVariableName(path string) bool {
	return pseudoVariableNames[path]
}

// isPseudoVariableHead reports whether a bare head introduces a declared
// namespace. It is DERIVED from pseudoVariableNames rather than declared, so
// adding a name cannot leave its head unregistered.
func isPseudoVariableHead(head string) bool {
	for name := range pseudoVariableNames {
		if h, _, ok := strings.Cut(name, "."); ok && h == head {
			return true
		}
	}
	return false
}

// sortedPseudoVariableNames renders the declared names deterministically, for a
// refusal message. Go randomizes map iteration, and a message assembled from an
// unsorted walk would vary run to run in exactly the way result() sorts its
// violations to prevent.
func sortedPseudoVariableNames() []string {
	out := make([]string, 0, len(pseudoVariableNames))
	for name := range pseudoVariableNames {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// checkPseudoVariable censuses a read under a declared pseudo-variable namespace
// against the DECLARED NAMES, which is that namespace's whole vocabulary.
//
// IT DOES NOT GO THROUGH refuseVocabulary, deliberately: that helper reports what
// the LOADED SOURCE GRAPH carries, and a pseudo-variable is stamped by a rule
// rather than read from the corpus, so naming the graph's metadata keys here
// would send an author looking for a key that was never the point. This mirrors
// checkEdgeFieldPath, which refuses against the edge vocabulary for the same
// reason.
func (v *sourceValidator) checkPseudoVariable(path string, pos Position, site string) {
	if isPseudoVariableName(path) {
		return
	}
	v.add(pos, "%s: `%s` is not a value any rule stamps on a row — the stamped names are %s. "+
		"The run was refused before the walk rather than answered with zero rows",
		site, path, joinPlain(sortedPseudoVariableNames()))
}
