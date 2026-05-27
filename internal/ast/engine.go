// SPDX-License-Identifier: Apache-2.0

// engine.go — pattern compilation pipeline for the v2 manual-walker engine.
//
// The compiler turns a parsed Pattern (from dsl.go) into a PatternTree the
// runtime walker matches against target ASTs. Pipeline (per ticket
// badce432a4917ba4b5e8867e093d0b9e "Engine architecture (locked)"):
//
//  1. Substitute every placeholder in pat.Source with a reserved-prefix
//     identifier (e.g. `$X` → `__META_AST_X`) chosen from cfg.Reserved.
//     Sequence placeholders use a `SEQ_` infix; wildcards a `WILD_`/`SEQWILD_`
//     infix plus a per-occurrence index so multiple wildcards never collide.
//  2. Try each cfg.Wrappers entry in order. The substituted source is
//     spliced between Prefix and Suffix and parsed via
//     treesitter.Parser.Parse. The first wrapper that yields a tree without
//     ERROR nodes wins.
//  3. Walk the resulting tree to find the smallest node that fully covers
//     the substituted-source byte range — that's the EffectiveRoot, the
//     starting point the runtime walker uses against target nodes. Index
//     every reserved-prefix identifier descendant by (start_byte, end_byte)
//     so the walker can recognize placeholder positions during traversal.
//
// Tree-sitter Close discipline (practice/go finding 92df0f05): the parser
// is closed via defer; the parsed Tree is owned by the returned PatternTree
// and released via PatternTree.Close. Failed wrapper attempts close their
// trees inline.

package ast

import (
	"context"
	"errors"
	"fmt"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// byteRange is a half-open [Start, End) range used to key placeholder
// positions in PatternTree.Placeholders.
type byteRange struct {
	Start uint32
	End   uint32
}

// PatternTree is a compiled pattern: the parsed tree-sitter tree, the
// effective root within that tree (after wrapper-stripping), and the
// per-occurrence placeholder index.
//
// Tree is owned by PatternTree — callers MUST defer pt.Close() to release
// the underlying CGO tree-sitter resources.
type PatternTree struct {
	// Tree is the parsed tree-sitter tree. Owned by PatternTree.
	Tree *sitter.Tree

	// Root is the smallest tree node that fully covers the user's
	// substituted source range. matchTree starts comparison here.
	Root *sitter.Node

	// WrapperSkip is the depth from Tree.RootNode() to Root, inclusive of
	// every wrapper level. Informational — the walker uses Root directly.
	WrapperSkip int

	// Placeholders maps the byte range of each substituted reserved-prefix
	// identifier in the parsed source to the original Placeholder. The
	// walker uses this map to recognize placeholder positions in the
	// PatternTree during traversal.
	Placeholders map[byteRange]Placeholder

	// SubstitutedSource is the literal source string handed to the
	// parser (Prefix + substituted DSL + Suffix). Useful for error
	// messages and explain output.
	SubstitutedSource string

	// PrefixLen is the byte length of the chosen wrapper prefix in
	// SubstitutedSource. The walker uses it to translate a placeholder
	// byteRange (which is an offset into the parser's input, i.e.
	// SubstitutedSource) back to the user-supplied DSL when reporting
	// errors.
	PrefixLen int

	// LangCfg is the resolved LangConfig used to build this tree. Held so
	// downstream code (sub-pattern recursion in B.4) can reach the
	// reserved prefix without re-resolving.
	LangCfg LangConfig
}

// Close releases the underlying tree-sitter tree. Nil-safe.
func (pt *PatternTree) Close() {
	if pt == nil {
		return
	}
	if pt.Tree != nil {
		pt.Tree.Close()
		pt.Tree = nil
	}
	pt.Root = nil
}

// errCompileNoWrapper is returned when no wrapper produces an ERROR-free
// parse. The error message lists every wrapper attempted plus the ERROR-
// node summary from the most-promising wrapper (the one that produced the
// fewest ERROR descendants).
var errCompileNoWrapper = errors.New("ast/engine: pattern did not parse under any context wrapper")

// compilePattern is the v2 pattern compiler. Substitutes placeholders, then
// tries each wrapper until one parses without ERROR nodes; on success
// returns a PatternTree the walker can match against target nodes. On
// failure returns an error that names every wrapper attempted.
func compilePattern(ctx context.Context, pat Pattern, cfg LangConfig) (*PatternTree, error) {
	if pat.Source == "" {
		return nil, errParseEmpty
	}
	if len(cfg.Wrappers) == 0 {
		return nil, fmt.Errorf("ast/engine: LangConfig for %q has no wrappers", cfg.Lang)
	}

	subst, placeholderRanges := substitutePlaceholders(pat, cfg)

	parser := treesitter.NewParser()
	defer parser.Close()

	var (
		bestErr      error
		attempted    []string
		errorSummary string
	)
	for _, w := range cfg.Wrappers {
		attempted = append(attempted, w.Name)
		full := w.Prefix + subst + w.Suffix
		tree, err := parser.Parse(ctx, []byte(full), cfg.Lang)
		if err != nil {
			if bestErr == nil {
				bestErr = err
			}
			continue
		}
		root := tree.RootNode()
		if root == nil || root.HasError() {
			if errorSummary == "" && root != nil {
				errorSummary = root.String()
			}
			tree.Close()
			continue
		}
		// Successful parse. Build the PatternTree.
		pt, perr := buildPatternTree(tree, full, w, subst, placeholderRanges, cfg)
		if perr != nil {
			tree.Close()
			return nil, perr
		}
		return pt, nil
	}
	if bestErr != nil {
		return nil, fmt.Errorf("%w (tried %s; first parse error: %v)", errCompileNoWrapper, strings.Join(attempted, ","), bestErr)
	}
	return nil, fmt.Errorf("%w (tried %s; ERROR-node summary: %s)", errCompileNoWrapper, strings.Join(attempted, ","), errorSummary)
}

// substitution describes one placeholder replacement applied to pat.Source.
type substitution struct {
	OldStart    int
	OldEnd      int
	Replacement string
	Placeholder Placeholder
}

// substitutePlaceholders applies cfg.Reserved-prefixed identifier
// replacements to every placeholder in pat. Returns the substituted source
// and a slice of substitution descriptors so buildPatternTree can later
// match parsed identifiers back to the original Placeholder.
//
// Naming scheme:
//
//	$X       → <Reserved>X
//	$_       → <Reserved>WILD_<i>
//	$$$X     → <Reserved>SEQ_X
//	$$$_     → <Reserved>SEQWILD_<i>
//
// Multiple `$X` with the same name produce separate captures (the v2 design
// rejects implicit binding equality), so the substituted identifier carries
// no de-duplication — each occurrence gets the same replacement and the
// walker emits a fresh capture per match. To keep the substituted text
// distinct in the parser's view (so we can walk-and-match on byteRange),
// each occurrence is INDEXED with a per-pattern counter:
//
//	$X (1st occurrence) → <Reserved>X__0
//	$X (2nd occurrence) → <Reserved>X__1
//
// This guarantees byteRange uniqueness while letting the walker recover the
// canonical capture name (everything before the trailing `__N`).
func substitutePlaceholders(pat Pattern, cfg LangConfig) (string, []substitution) {
	if len(pat.Placeholders) == 0 {
		return pat.Source, nil
	}
	var (
		b          strings.Builder
		subs       = make([]substitution, 0, len(pat.Placeholders))
		cursor     = 0
		wildIdx    = 0
		seqWildIdx = 0
		nameOccur  = map[string]int{}
	)

	for _, ph := range pat.Placeholders {
		// Copy literal text up to the placeholder.
		b.WriteString(pat.Source[cursor:ph.OffsetStart])

		var replacement string
		switch ph.Kind {
		case KindNode:
			occ := nameOccur[ph.Name]
			nameOccur[ph.Name] = occ + 1
			replacement = fmt.Sprintf("%s%s__%d", cfg.Reserved, ph.Name, occ)
		case KindNodeWild:
			replacement = fmt.Sprintf("%sWILD__%d", cfg.Reserved, wildIdx)
			wildIdx++
		case KindSeq:
			occ := nameOccur[ph.Name]
			nameOccur[ph.Name] = occ + 1
			replacement = fmt.Sprintf("%sSEQ_%s__%d", cfg.Reserved, ph.Name, occ)
		case KindSeqWild:
			replacement = fmt.Sprintf("%sSEQWILD__%d", cfg.Reserved, seqWildIdx)
			seqWildIdx++
		}

		subs = append(subs, substitution{
			OldStart:    ph.OffsetStart,
			OldEnd:      ph.OffsetEnd,
			Replacement: replacement,
			Placeholder: ph,
		})
		b.WriteString(replacement)
		cursor = ph.OffsetEnd
	}
	b.WriteString(pat.Source[cursor:])
	return b.String(), subs
}

// buildPatternTree walks the parsed tree, locates the EffectiveRoot (the
// smallest node fully covering the substituted user-source range within
// SubstitutedSource), and indexes every reserved-prefix identifier by
// (start_byte, end_byte) so the runtime walker can recognize placeholder
// positions in the PatternTree.
func buildPatternTree(
	tree *sitter.Tree,
	full string,
	w ContextWrapper,
	subst string,
	subs []substitution,
	cfg LangConfig,
) (*PatternTree, error) {
	root := tree.RootNode()
	if root == nil {
		return nil, fmt.Errorf("ast/engine: parsed tree has nil root")
	}

	prefixLen := uint32(len(w.Prefix))
	userStart := prefixLen
	userEnd := prefixLen + uint32(len(subst))

	effective, depth := smallestNodeCovering(root, userStart, userEnd, 0)
	if effective == nil {
		// Should not happen — the wrapper would not have parsed cleanly.
		effective = root
		depth = 0
	}

	placeholders := indexPlaceholders(root, []byte(full), subs, cfg)

	return &PatternTree{
		Tree:              tree,
		Root:              effective,
		WrapperSkip:       depth,
		Placeholders:      placeholders,
		SubstitutedSource: full,
		PrefixLen:         int(prefixLen),
		LangCfg:           cfg,
	}, nil
}

// smallestNodeCovering returns the smallest descendant of n whose byte
// range fully covers [start, end). depth tracks the number of edges
// traversed from the original caller's n. Returns (n, depth) when no
// strictly-smaller descendant covers the range — the input is the answer.
func smallestNodeCovering(n *sitter.Node, start, end uint32, depth int) (*sitter.Node, int) {
	if n == nil || n.StartByte() > start || n.EndByte() < end {
		return nil, 0
	}
	for i := uint32(0); i < n.NamedChildCount(); i++ {
		child := n.NamedChild(int(i))
		if child == nil {
			continue
		}
		if child.StartByte() <= start && child.EndByte() >= end {
			return smallestNodeCovering(child, start, end, depth+1)
		}
	}
	return n, depth
}

// indexPlaceholders walks the parsed tree and records every node whose
// content text matches a substituted-placeholder identifier. Keys the
// result by (start_byte, end_byte) so the runtime walker can do
// constant-time lookup during traversal.
//
// We accept any node kind because tree-sitter parses substitutions as
// identifier in expression position, type_identifier in type position,
// field_identifier in selector position, etc. The reserved-prefix
// substitution naming is unique enough that text equality alone suffices.
func indexPlaceholders(root *sitter.Node, src []byte, subs []substitution, cfg LangConfig) map[byteRange]Placeholder {
	out := make(map[byteRange]Placeholder, len(subs))
	if len(subs) == 0 {
		return out
	}
	byText := make(map[string]Placeholder, len(subs))
	for _, s := range subs {
		byText[s.Replacement] = s.Placeholder
	}
	walkAll(root, func(n *sitter.Node) {
		if n == nil {
			return
		}
		// Placeholder substitutions are always leaf-shaped identifiers.
		// Skip non-leaf nodes for the cheap path.
		if n.NamedChildCount() > 0 {
			return
		}
		text := n.Content(src)
		if !strings.HasPrefix(text, cfg.Reserved) {
			return
		}
		if ph, ok := byText[text]; ok {
			out[byteRange{Start: n.StartByte(), End: n.EndByte()}] = ph
		}
	})
	return out
}

// walkAll calls fn on every node in the subtree rooted at n in pre-order.
func walkAll(n *sitter.Node, fn func(*sitter.Node)) {
	if n == nil {
		return
	}
	fn(n)
	count := int(n.NamedChildCount())
	for i := range count {
		walkAll(n.NamedChild(i), fn)
	}
}
