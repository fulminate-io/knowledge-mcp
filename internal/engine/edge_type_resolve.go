// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"context"
	"fmt"
	"sort"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// edge_type_resolve.go resolves a caller's edge-type spelling against the
// TARGET GRAPH'S OWN edge vocabulary, replacing the per-graph-family case fold
// the client used to apply.
//
// WHY THE FOLD WAS WRONG: it was a function of the graph NAME, so it answered
// "what casing does a pdf graph use?" — a question with no true answer. A graph
// can carry two casing families at once (a raw pdf harvest holding both
// CONTAINS and contains), and the store compares edge types EXACTLY
// (edgeTypeFilter.matches in the server's edge_iterator.go; applySelection
// applies them as-given). Folding therefore turned a correct filter into a
// silent zero on one family and minted a second family on a mis-cased write.
//
// THE FOUR RULES:
//  1. An EXACT match wins outright.
//  2. A UNIQUE case-insensitive match resolves to the STORED spelling. On a
//     read this keeps edge_types ["calls"] working against a stored CALLS. On a
//     write it is what stops a second casing family being minted: linking
//     CONTAINS into a graph carrying only contains stores contains.
//  3. An AMBIGUOUS case-insensitive match REFUSES on BOTH paths, naming the
//     caller's spelling and every stored spelling it folded onto. This is the
//     one case where the caller's intent genuinely cannot be recovered, and
//     guessing would pick a family by accident.
//  4. NO MATCH DIVERGES, AND THE ASYMMETRY IS BY DESIGN — do not "fix" it.
//     A READ refuses, naming the vocabulary, because resolving against what
//     EXISTS is the entire question a filter asks: there is nothing for an
//     unknown spelling to select, so serving an empty result would be a
//     confident wrong answer. A WRITE admits the caller's spelling as a new
//     edge type, because a write may introduce what SHOULD exist: the existing
//     vocabulary is advice about near-misses there, not an authority over it.
//     Refusing on the write side would make the FIRST edge of every graph
//     unrepresentable — a fresh linkage graph could never receive its first
//     BUILDS.
//
// An EMPTY vocabulary is deliberately NOT a special case in the code below. It
// simply produces no match, which a write admits and a read refuses — the same
// two answers the rules give everywhere else. A branch for it is what made the
// bootstrap case wrong before it was removed.

// StatsFn issues one Engine.Stats RPC. It is a function type rather than an
// interface for the same reason ExecuteFn is: every caller injects its own
// client, and this package imports none of them.
type StatsFn func(ctx context.Context, req *knowledgev1.StatsRequest) (*knowledgev1.StatsResponse, error)

// EdgeTypeResolution is what a resolution pass learned about the graph.
//
// Types is positional: Types[i] is the spelling to send for want[i]. Unmatched
// lists the input spellings that matched no stored family and were ADMITTED —
// non-empty only on the declaration path, since a filter refuses instead.
// Vocabulary is the graph's stored edge types, sorted, so a caller that needs
// to say what the graph holds does not re-read Stats to find out.
type EdgeTypeResolution struct {
	Types      []string
	Unmatched  []string
	Vocabulary []string
}

// ResolveEdgeTypeFilter resolves a READ filter's edge-type spellings — the
// traverse tool's edge_types. An unknown spelling is REFUSED naming the
// vocabulary (rule 4, read half).
func ResolveEdgeTypeFilter(ctx context.Context, stats StatsFn, target *knowledgev1.GraphSelector, want []string) ([]string, error) {
	res, err := resolveEdgeTypes(ctx, stats, target, want, "edge_types", false)
	if err != nil {
		return nil, err
	}
	return res.Types, nil
}

// ResolveEdgeTypeDeclaration resolves a WRITE's edge-type declaration — the
// mutate link/unlink relationship. An unknown spelling is ADMITTED as a new
// edge type (rule 4, write half) and reported in Unmatched so the caller can
// disclose it.
//
// TWO ENTRY POINTS, ONE CORE, deliberately: a single function taking a boolean
// would let a call site select the wrong half by passing the wrong literal, and
// a bare true/false at a call site reads as nothing at all. The names cannot be
// got wrong; the shared rules cannot drift apart.
func ResolveEdgeTypeDeclaration(ctx context.Context, stats StatsFn, target *knowledgev1.GraphSelector, want []string) (EdgeTypeResolution, error) {
	return resolveEdgeTypes(ctx, stats, target, want, "relationship", true)
}

// resolveEdgeTypes is the shared core both entry points delegate to.
//
// COST IS A CONTRACT, NOT AN ACCIDENT: exactly ONE Stats read per CALL, never
// one per edge type — the vocabulary is fetched once and every spelling is
// resolved against that one map. An EMPTY want returns immediately and issues
// NO Stats read at all, so a traverse naming no edge_types costs exactly what
// it cost before this existed.
//
// A nil StatsFn with a non-empty want is an ERROR rather than a silent skip: a
// caller that quietly loses resolution is the failure mode this whole change
// removes. A Stats failure is likewise returned, never swallowed — a graph the
// caller cannot stat must surface that rather than degrade into an unfiltered
// or empty walk.
func resolveEdgeTypes(ctx context.Context, stats StatsFn, target *knowledgev1.GraphSelector, want []string, kind string, admitUnknown bool) (EdgeTypeResolution, error) {
	if len(want) == 0 {
		return EdgeTypeResolution{Types: want}, nil
	}
	if stats == nil {
		return EdgeTypeResolution{}, fmt.Errorf(
			"%s cannot be resolved: no graph-stats seam is wired on this dispatch path", kind)
	}
	resp, err := stats(ctx, &knowledgev1.StatsRequest{Target: target})
	if err != nil {
		return EdgeTypeResolution{}, fmt.Errorf(
			"cannot read the edge vocabulary of %s: %w", describeGraphTarget(target), err)
	}
	vocab := resp.GetGraphStats().GetEdgesByType()
	res := EdgeTypeResolution{
		Types:      make([]string, len(want)),
		Vocabulary: sortedVocabKeys(vocab),
	}
	for i, w := range want {
		if _, exact := vocab[w]; exact {
			res.Types[i] = w // rule 1.
			continue
		}
		folded := foldedMatches(vocab, w)
		switch len(folded) {
		case 1:
			res.Types[i] = folded[0] // rule 2: adopt the STORED spelling.
		case 0:
			if !admitUnknown {
				return EdgeTypeResolution{}, fmt.Errorf(
					"%s %q does not match any edge type stored in %s. The graph's edge vocabulary is: %s",
					kind, w, describeGraphTarget(target), renderVocabulary(res.Vocabulary)) // rule 4, read.
			}
			res.Types[i] = w // rule 4, write: the caller's spelling becomes a new family.
			res.Unmatched = append(res.Unmatched, w)
		default:
			return EdgeTypeResolution{}, fmt.Errorf(
				"%s %q is ambiguous in %s: it case-insensitively matches the stored edge types %s — name one of them exactly",
				kind, w, describeGraphTarget(target), strings.Join(folded, ", ")) // rule 3.
		}
	}
	return res, nil
}

// foldedMatches returns every stored spelling that case-insensitively equals
// want, SORTED so an ambiguity refusal reads the same way on every run (Go map
// iteration order is randomized, and a refusal message that reorders itself
// between runs is a message nobody can diff).
func foldedMatches(vocab map[string]int64, want string) []string {
	var out []string
	for stored := range vocab {
		if strings.EqualFold(stored, want) {
			out = append(out, stored)
		}
	}
	sort.Strings(out)
	return out
}

// sortedVocabKeys returns the graph's stored edge types in a stable order.
//
// NAMED sortedVocabKeys rather than sortedKeys because this package already
// declares a sortedKeys over map[string]string (render_node.go) — a second one
// would not compile.
func sortedVocabKeys(vocab map[string]int64) []string {
	out := make([]string, 0, len(vocab))
	for k := range vocab {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// renderVocabulary is the caller-facing rendering of the stored edge types. An
// empty graph says so in words rather than trailing off after a colon — the
// bootstrap case is a legitimate state a reader will meet, not a malfunction.
func renderVocabulary(vocab []string) string {
	if len(vocab) == 0 {
		return "(none — this graph stores no edges yet)"
	}
	return strings.Join(vocab, ", ")
}

// describeGraphTarget renders the selector a resolution read against: the graph
// type plus whichever instance selector it carries, so a refusal names the
// graph the caller actually addressed rather than just its family.
func describeGraphTarget(target *knowledgev1.GraphSelector) string {
	graph := target.GetGraph()
	if graph == "" {
		graph = "knowledge" // the engine's empty-graph=knowledge default.
	}
	out := "graph=" + graph
	switch {
	case target.GetName() != "":
		out += " name=" + target.GetName()
	case target.GetRepo() != "":
		out += " repo=" + target.GetRepo()
	case target.GetAccount() != "":
		out += " account=" + target.GetAccount()
	case target.GetLanguage() != "":
		out += " language=" + target.GetLanguage()
	}
	return out
}
