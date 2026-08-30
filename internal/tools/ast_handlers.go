// SPDX-License-Identifier: Apache-2.0

// ast_handlers.go — explain / list_node_kinds handlers for the
// client-side ast intercept. Split from ast.go to keep both files under
// the 300-line lefthook warn threshold.

package tools

import (
	"context"
	"strings"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/fulminate-io/knowledge-mcp/internal/ast"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// handleAstReplace is the WRITE counterpart to handleAstMatch: it mirrors the
// same setup (buildAstPatterns / ParseWhere / resolveRepoDir / scopeFromArgs /
// matchAll), then hands the raw matches to ast.ApplyReplace. dry_run defaults
// TRUE (a.DryRun==nil) so a missing arg previews without writing; an explicit
// dry_run:false applies. Overlapping/nested refusals and re-parse rejections
// are reported in the result, not treated as fatal.
func handleAstReplace(ctx context.Context, deps ClientDeps, a astArgs) kgtools.ToolResult {
	lang := treesitter.Language(a.Language)
	if _, ok := treesitter.LanguageGrammar(lang); !ok {
		return errorResult("unsupported language: " + a.Language)
	}
	// A nil replacement means the arg was omitted → error. An EXPLICIT empty
	// string is allowed and DELETES the matched ranges (the template interpolates
	// to "", splicing nothing). dry_run defaults TRUE, so a deletion previews its
	// unified diff before any write.
	if a.Replacement == nil {
		return errorResult(`operation=replace requires replacement (pass replacement:"" to DELETE matched ranges)`)
	}

	patterns, patternErrs, perr := buildAstPatterns(a)
	if perr != nil {
		return errorResult("parse pattern: " + perr.Error())
	}

	where, werr := ast.ParseWhere(a.Where)
	if werr != nil {
		return errorResult("parse where: " + werr.Error())
	}

	// The same refusal as on the read paths, and this is where it pays: a
	// where-tree that can never match makes a replace report zero rewrites with
	// no error, which reads as a migration that had nothing left to do.
	if kerr := ast.ValidateWhereKinds(where, lang); kerr != nil {
		return errorResult(kerr.Error())
	}

	// Same reasoning one leaf over, and it pays hardest here for the reason
	// above: an unanswerable flows_to leaf would make a replace report zero
	// rewrites with no error.
	if ferr := ast.ValidateWhereFlowArms(where, lang); ferr != nil {
		return errorResult(ferr.Error())
	}

	if perr := validateContextPin(a.Context, lang); perr != nil {
		return errorResult(perr.Error())
	}

	// The refusal matters most on this path: include_tests is a blast-radius
	// control, and a control that is accepted but inert widens a WRITE.
	if terr := validateIncludeTests(a, lang); terr != nil {
		return errorResult(terr.Error())
	}

	repoDir, derr := resolveRepoDir(ctx, deps, "ast", a.Repo)
	if derr != nil {
		return errorResult(derr.Error())
	}

	scope := scopeFromArgs(a)
	// Ask the match walk to record a per-matched-file parse hint {clean, size,
	// digest} so ApplyReplace's pre-edit baseline can skip re-parsing files the
	// match already parsed clean. Only the replace path requests it — the read
	// paths leave EmitParseHint false and pay no digest.
	scope.EmitParseHint = true
	raws, compiled, narrowed, walk, patternErrs, merr := matchAll(ctx, a, lang, patterns, patternErrs, where, repoDir, scope)
	if merr != nil {
		return errorResult("match: " + merr.Error())
	}

	dryRun := a.DryRun == nil || bool(*a.DryRun)
	res, rerr := ast.ApplyReplace(ctx, repoDir, lang, raws, *a.Replacement, dryRun, walk.CleanHint)
	if rerr != nil {
		return errorResult("replace: " + rerr.Error())
	}

	// compiled travels with the write path too: a rewrite driven by a pattern
	// that compiled to the wrong construct is the failure this disclosure
	// exists to catch, and it is the one where seeing it late costs the most.
	//
	// The four counters are reported apart because they answer different
	// questions: files_matched / matches_replaced are what the pattern reached,
	// files_changed / matches_changed are what actually moved. An identity
	// template makes the second pair zero while the first stays non-zero.
	// preexisting_parse_failures names files that were already ungrammatical —
	// declined rather than rejected, so rejected_files means only what the edit
	// broke.
	//
	// pattern_errors matters most on this path for the same reason the kind
	// refusal does: a rewrite driven by three of four sibling forms, reported as
	// though all four ran, is a migration certified complete over a quarter of
	// its intended blast radius.
	out := map[string]any{
		"applied":                    res.Applied,
		"compiled":                   compiled,
		"narrowed":                   narrowed,
		"dry_run":                    dryRun,
		"files_matched":              res.FilesMatched,
		"files_changed":              res.FilesChanged,
		"matches_replaced":           res.MatchesReplaced,
		"matches_changed":            res.MatchesChanged,
		"refused_files":              res.RefusedFiles,
		"rejected_files":             res.RejectedFiles,
		"preexisting_parse_failures": res.PreexistingParseFailures,
		"diffs":                      res.Diffs,
	}
	if len(patternErrs) > 0 {
		out["pattern_errors"] = patternErrs
	}
	return jsonResult(out)
}

// handleAstExplain parses a snippet and emits an indented node-kind tree.
// Per-call failure modes are surfaced as errorResult —
// explain is a debug op invoked with a single snippet, NOT a corpus walk,
// so the silent-skip discipline from match.go does not apply here.
func handleAstExplain(ctx context.Context, a astArgs) kgtools.ToolResult {
	if strings.TrimSpace(a.Snippet) == "" {
		return errorResult("operation=explain requires snippet")
	}
	lang := treesitter.Language(a.Language)
	grammar, ok := treesitter.LanguageGrammar(lang)
	if !ok || grammar == nil {
		return errorResult("unsupported language: " + a.Language)
	}

	parser := treesitter.NewParser()
	defer parser.Close()
	tree, perr := parser.Parse(ctx, []byte(a.Snippet), lang)
	if perr != nil {
		return errorResult("parse failed: " + perr.Error())
	}
	defer tree.Close()

	var b strings.Builder
	// A denied language still parses — explain is informational — but match and
	// replace refuse it, so prepend a note making clear this parse view cannot
	// drive a rewrite. Supported languages get no note (the tree speaks for
	// itself).
	if ast.IsDeniedLanguage(lang) {
		b.WriteString("// NOTE: " + a.Language + " is deny-listed for ast match/replace; this parse view is informational only.\n")
	}
	walkNodeKinds(tree.RootNode(), 0, &b)
	return textResult(b.String())
}

// walkNodeKinds depth-first prints each tree-sitter node's Type() indented
// by depth. Includes every named-and-anonymous child so the LLM sees the
// full structure (callers explicitly asked for "explain" — punctuation
// included is more useful than a filtered tree for this debug op).
//
// Each line is tagged ` (named)` or ` (anonymous)` from n.IsNamed(). The
// distinction is load-bearing for authoring a replace: an anonymous token
// (punctuation, a keyword like `func`, a brace) is exactly the kind of node a
// byte-range splice can silently drop or inject, so surfacing which nodes are
// anonymous tells an author where a rewrite is most likely to lose a token.
// The tag is additive — it never changes WHICH nodes are printed (anonymous
// tokens still appear), only labels them, so existing substring assertions
// against a bare kind name (e.g. "function_declaration") stay green.
func walkNodeKinds(n *sitter.Node, depth int, b *strings.Builder) {
	if n == nil {
		return
	}
	for range depth {
		b.WriteString("  ")
	}
	b.WriteString(n.Type())
	if n.IsNamed() {
		b.WriteString(" (named)")
	} else {
		b.WriteString(" (anonymous)")
	}
	b.WriteByte('\n')
	for i := range int(n.ChildCount()) {
		walkNodeKinds(n.Child(i), depth+1, b)
	}
}

// handleAstListNodeKinds prints the tree-sitter node-kind vocabulary for a
// language. The enumeration itself lives in the ast package as NodeKinds, which
// the where-tree kind validator reads too: one enumeration means a name this op
// prints can never be a name that validator rejects.
func handleAstListNodeKinds(a astArgs) kgtools.ToolResult {
	lang := treesitter.Language(a.Language)
	kinds, ok := ast.NodeKinds(lang)
	if !ok {
		return errorResult("unsupported language: " + a.Language)
	}

	out := map[string]any{
		"language":    a.Language,
		"node_kinds":  kinds,
		"source":      "dynamic",
		"count":       len(kinds),
		"resolved_at": time.Now().UTC().Format(time.RFC3339),
	}
	// The enumeration is grammar-driven, so a denied language still lists its
	// node kinds honestly — but match/replace refuse the language, so those
	// kinds are informational only: none can appear in a pattern or a
	// where-kind leaf. match_replace_supported carries that fact on every
	// response; the note explains it only where it is false.
	if ast.IsDeniedLanguage(lang) {
		out["match_replace_supported"] = false
		out["note"] = a.Language + " is deny-listed for ast match/replace; these node kinds are informational only and cannot be used in a pattern or where-kind leaf"
	} else {
		out["match_replace_supported"] = true
	}
	return jsonResult(out)
}
