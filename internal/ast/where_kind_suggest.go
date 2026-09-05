// SPDX-License-Identifier: Apache-2.0

// where_kind_suggest.go — near-miss suggestions for a rejected where-tree node
// kind.
//
// This is written rather than reused, and the census that decided it ran on
// both axes. By concept, the tree's only edit-distance implementations live in
// pdf font test helpers and in a file under a testdata/ path — test-only and
// excluded from the walk respectively, so neither is importable production
// code. By structure, the closest analog is the undeclared-param rejection's
// "did you mean" message: its SHAPE is worth mirroring, but its matching is
// exact set membership, and exact membership is precisely what has already
// failed by the time a suggestion is wanted.
//
// Cost: the scan is O(vocabulary x name length) and runs only on the error
// path, which is never hot. The vocabulary is built once per call by the
// validator and passed in.

package ast

import "sort"

// maxSuggestionDistance bounds how far a candidate may sit from the rejected
// name before it stops being a plausible typo. Three edits covers a doubled
// letter plus a transposition; past that a "did you mean" is noise and
// operation=list_node_kinds serves the caller better.
const maxSuggestionDistance = 3

// maxKindSuggestions bounds how many near-misses a rejection names. The C# and
// Java grammars carry hundreds of kinds, and an unbounded list buries the
// answer it exists to give; the caller who wants the whole set has
// operation=list_node_kinds.
const maxKindSuggestions = 3

// ClosestVocabulary returns up to maxKindSuggestions vocabulary entries within
// maxSuggestionDistance edits of name, nearest first and ties broken
// alphabetically so an error message is deterministic across runs.
//
// IT IS A SHARED NEAR-MISS SUGGESTER OVER ANY STRING VOCABULARY, and it has two
// callers whose definitions of a VALID VALUE are OPPOSITE. This package's kind
// validator checks a name against a tree-sitter GRAMMAR's symbol table: the
// vocabulary is every kind the grammar can produce, whether or not any file in
// the tree uses it. The recipe validator checks a value against a CENSUS OF A
// LOADED SOURCE GRAPH: the vocabulary is every value the corpus actually
// carries, and a value the schema permits but the graph never stamped is
// correctly refused.
//
// THAT IS WHY THE TWO VALIDATORS CANNOT MERGE, and this sentence is the only
// place in either package that says so. Someone who later notices two
// near-identical "unknown X, did you mean Y" validators and unifies them
// destroys one of the two meanings: grammar membership answered from a corpus
// refuses legal-but-unused kinds, and corpus membership answered from a grammar
// re-opens exactly the silent-empty-result class the recipe validator exists to
// close. Only the SUGGESTER is shared, because ranking strings by edit distance
// means the same thing on both sides.
func ClosestVocabulary(name string, vocabulary []string) []string {
	type scored struct {
		kind string
		dist int
	}
	var hits []scored
	for _, candidate := range vocabulary {
		// Length alone rules a candidate out for far less than the cost of
		// scoring it: no sequence of k edits moves a length by more than k.
		if d := len(candidate) - len(name); d > maxSuggestionDistance || d < -maxSuggestionDistance {
			continue
		}
		dist := editDistance(name, candidate)
		if dist > maxSuggestionDistance {
			continue
		}
		hits = append(hits, scored{kind: candidate, dist: dist})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].dist != hits[j].dist {
			return hits[i].dist < hits[j].dist
		}
		return hits[i].kind < hits[j].kind
	})
	if len(hits) > maxKindSuggestions {
		hits = hits[:maxKindSuggestions]
	}
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.kind)
	}
	return out
}

// editDistance is the Levenshtein distance between a and b: the fewest
// single-character insertions, deletions or substitutions that turn one into
// the other. Node-kind names are ASCII identifiers, so comparing bytes is
// comparing characters here.
func editDistance(a, b string) int {
	switch {
	case a == b:
		return 0
	case len(a) == 0:
		return len(b)
	case len(b) == 0:
		return len(a)
	}
	// Two rolling rows rather than the full matrix — only the previous row is
	// ever read, so the rest of it is never worth allocating.
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			substitution := prev[j-1]
			if a[i-1] != b[j-1] {
				substitution++
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, substitution)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}
