// SPDX-License-Identifier: Apache-2.0

// ast_handlers.go — explain / list_node_kinds handlers for the
// client-side ast intercept. Split from ast.go to keep both files under
// the 300-line lefthook warn threshold.

package tools

import (
	"context"
	"sort"
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
	if strings.TrimSpace(a.Replacement) == "" {
		return errorResult("operation=replace requires replacement")
	}

	patterns, perr := buildAstPatterns(a)
	if perr != nil {
		return errorResult("parse pattern: " + perr.Error())
	}

	where, werr := ast.ParseWhere(a.Where)
	if werr != nil {
		return errorResult("parse where: " + werr.Error())
	}

	repoDir, derr := resolveRepoDir(deps)
	if derr != nil {
		return errorResult(derr.Error())
	}

	scope := scopeFromArgs(a)
	raws, _, merr := matchAll(ctx, lang, patterns, where, repoDir, scope)
	if merr != nil {
		return errorResult("match: " + merr.Error())
	}

	dryRun := a.DryRun == nil || bool(*a.DryRun)
	res, rerr := ast.ApplyReplace(ctx, repoDir, lang, raws, a.Replacement, dryRun)
	if rerr != nil {
		return errorResult("replace: " + rerr.Error())
	}

	return jsonResult(map[string]any{
		"applied":          res.Applied,
		"dry_run":          dryRun,
		"files_touched":    res.FilesTouched,
		"matches_replaced": res.MatchesReplaced,
		"refused_files":    res.RefusedFiles,
		"rejected_files":   res.RejectedFiles,
		"diffs":            res.Diffs,
	})
}

// handleAstExplain parses a snippet and emits an indented node-kind tree.
// Per-call failure modes (criterion 8146d875) are surfaced as errorResult —
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
	walkNodeKinds(tree.RootNode(), 0, &b)
	return textResult(b.String())
}

// walkNodeKinds depth-first prints each tree-sitter node's Type() indented
// by depth. Includes every named-and-anonymous child so the LLM sees the
// full structure (callers explicitly asked for "explain" — punctuation
// included is more useful than a filtered tree for this debug op).
func walkNodeKinds(n *sitter.Node, depth int, b *strings.Builder) {
	if n == nil {
		return
	}
	for range depth {
		b.WriteString("  ")
	}
	b.WriteString(n.Type())
	b.WriteByte('\n')
	for i := range int(n.ChildCount()) {
		walkNodeKinds(n.Child(i), depth+1, b)
	}
}

// handleAstListNodeKinds enumerates the tree-sitter node-kind vocabulary
// for a language by walking grammar.SymbolCount() / SymbolName() and
// filtering to SymbolTypeRegular (drops anonymous tokens like '+', '{').
//
// API verification recorded in plan-456dc5bb-impl think note (criterion
// 605a68f8): smacker exposes both SymbolCount() uint32 and
// SymbolName(s Symbol) string on *sitter.Language; bindings.go:362,372
// at $GOMODCACHE/github.com/smacker/go-tree-sitter@<sha>/bindings.go.
func handleAstListNodeKinds(a astArgs) kgtools.ToolResult {
	lang := treesitter.Language(a.Language)
	grammar, ok := treesitter.LanguageGrammar(lang)
	if !ok || grammar == nil {
		return errorResult("unsupported language: " + a.Language)
	}

	count := int(grammar.SymbolCount())
	seen := make(map[string]struct{}, count)
	kinds := make([]string, 0, count)
	for i := range count {
		s := sitter.Symbol(uint16(i))
		if grammar.SymbolType(s) != sitter.SymbolTypeRegular {
			continue
		}
		name := grammar.SymbolName(s)
		if name == "" {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		kinds = append(kinds, name)
	}
	sort.Strings(kinds)

	return jsonResult(map[string]any{
		"language":    a.Language,
		"node_kinds":  kinds,
		"source":      "dynamic",
		"count":       len(kinds),
		"resolved_at": time.Now().UTC().Format(time.RFC3339),
	})
}
