// SPDX-License-Identifier: Apache-2.0

// replace_baseline.go — the PRE-EDIT parse baseline for ast
// operation:"replace".
//
// The re-parse gate in applyEditsToSource (replace.go) can only tell that the
// rewritten source is ungrammatical. On its own that is not evidence the EDIT
// broke anything: a file that was already ungrammatical fails the same gate,
// and reporting it as a rejection tells the caller their edit failed when the
// file was broken before the call arrived.
//
// So every candidate file's ORIGINAL bytes are parsed before any splice runs.
// A file that already carries a grammar error is reported in
// ReplaceResult.PreexistingParseFailures — with the site of the error, so the
// message is a diagnosis rather than an accusation — and is never spliced and
// never written. After that split RejectedFiles means exactly one thing: the
// file parsed clean before the edit and does not parse after it.
//
// parseErrorSite is deliberately the SINGLE parse used by both sides. The
// baseline and the gate must agree on parser settings or the split they
// produce is meaningless, and two call sites constructing their own parser is
// how they drift apart.

package ast

import (
	"context"
	"hash/fnv"
	"sort"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// fileParseHint is the per-matched-file parse hint the replace path collects at
// MATCH time (matchFile, gated to the replace path) so the pre-edit baseline can
// skip re-parsing a file the match already parsed. clean is true when the match
// parse carried no ERROR nodes; size and digest (fnv64a over the file bytes) let
// the baseline confirm the bytes it is about to splice are the SAME bytes that
// were certified clean — a file mutated between match and replace mismatches the
// digest and is re-parsed, so the skip is stale-safe.
type fileParseHint struct {
	clean  bool
	size   int
	digest uint64
}

// fnv64a is the content digest shared by the hint producer (matchFile) and the
// hint consumer (baselineParseFailures): both must hash the SAME way or a clean
// file's bytes would never match their own recorded digest.
func fnv64a(b []byte) uint64 {
	h := fnv.New64a()
	_, _ = h.Write(b)
	return h.Sum64()
}

// ParseFailure locates a grammar error in a file the replace path declined to
// splice. Line and Column are 1-based (tree-sitter points are 0-based; both
// are incremented for a human-facing location).
//
// Line 0 means NO LOCATED SITE: the parse call itself failed — a parser
// operation-limit timeout, say — so the file could not be certified clean
// before the edit and could not be pointed at either. It is still a
// pre-existing failure, because the one thing that cannot honestly be said
// about such a file is that the caller's edit broke it.
type ParseFailure struct {
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
}

// parseErrorSite parses src for lang and reports the first grammar error in
// the resulting tree.
//
// Return shape:
//   - hasError=false, err=nil — src parses clean.
//   - hasError=true          — src carries a grammar error; line/column locate
//     the first ERROR or MISSING node, or are 0 when the tree offers no
//     locatable site.
//   - err != nil             — the parse call itself failed; line, column and
//     hasError are meaningless.
//
// Both the pre-edit baseline (baselineParseFailures) and the post-edit gate
// (applyEditsToSource) go through here, which is what keeps the two parses
// identical.
func parseErrorSite(ctx context.Context, src []byte, lang treesitter.Language) (line, column int, hasError bool, err error) {
	parser := treesitter.NewParser()
	defer parser.Close()
	tree, perr := parser.Parse(ctx, src, lang)
	if perr != nil {
		return 0, 0, false, perr
	}
	defer tree.Close()

	root := tree.RootNode()
	if root == nil {
		// No tree at all is a grammar failure with nowhere to point.
		return 0, 0, true, nil
	}
	if !root.HasError() {
		return 0, 0, false, nil
	}
	if n := firstErrorNode(root); n != nil {
		p := n.StartPoint()
		return int(p.Row) + 1, int(p.Column) + 1, true, nil
	}
	return 0, 0, true, nil
}

// firstErrorNode returns the first ERROR or MISSING node in document order,
// pruning any subtree that reports no error. tree-sitter sets HasError on
// every ancestor of an error node, so the prune cannot skip past the error it
// is looking for — and on a clean subtree it costs one check instead of a
// full descent.
func firstErrorNode(n *sitter.Node) *sitter.Node {
	if n == nil {
		return nil
	}
	if n.IsError() || n.IsMissing() {
		return n
	}
	if !n.HasError() {
		return nil
	}
	for i := range int(n.ChildCount()) {
		if found := firstErrorNode(n.Child(i)); found != nil {
			return found
		}
	}
	return nil
}

// baselineParseFailures parses the ORIGINAL bytes of every candidate file and
// returns those that already carry a grammar error, sorted by path.
//
// It runs over the WHOLE candidate set before the write loop begins rather
// than lazily inside it, so a crash mid-loop cannot leave a partially
// classified report — and so a dry run's blast radius describes exactly what
// an apply would do.
//
// The bytes come from srcByFile, which readMatchedSources already populated;
// no file is read twice. A path with no entry there is left unclassified
// (there is nothing to parse), and the write loop handles it as before.
//
// cleanHint (match-time parse hints, keyed by path) lets a file be certified
// clean WITHOUT re-parsing: when the hint says the file parsed clean at match
// time AND its size and fnv64a digest still match the bytes about to be spliced,
// the file cannot be a pre-existing failure and the re-parse is skipped. Any
// other case — absent hint, degraded hint, or a size/digest mismatch from a file
// mutated between match and replace — falls through to the parse, so
// classification is EXACTLY what it was before the hint existed. filesParsed
// counts the files actually re-parsed, so a test can prove the skip fired.
func baselineParseFailures(ctx context.Context, paths []string, srcByFile map[string][]byte, lang treesitter.Language, cleanHint map[string]fileParseHint) ([]ParseFailure, int) {
	var out []ParseFailure
	filesParsed := 0
	for _, path := range paths {
		src, ok := srcByFile[path]
		if !ok {
			continue
		}
		if h, ok := cleanHint[path]; ok && h.clean && h.size == len(src) && h.digest == fnv64a(src) {
			// Certified clean at match time and the bytes are unchanged, so it
			// cannot be a pre-existing failure — skip the parse.
			continue
		}
		filesParsed++
		line, column, hasError, err := parseErrorSite(ctx, src, lang)
		switch {
		case err != nil:
			// Unparseable for a reason that is not a located grammar error.
			// See ParseFailure.Line for why this is still pre-existing.
			out = append(out, ParseFailure{Path: path})
		case hasError:
			out = append(out, ParseFailure{Path: path, Line: line, Column: column})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, filesParsed
}
