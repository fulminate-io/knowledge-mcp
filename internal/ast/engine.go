// SPDX-License-Identifier: Apache-2.0

// engine.go — pattern compilation pipeline for the v2 manual-walker engine.
//
// The compiler turns a parsed Pattern (from dsl.go) into a PatternTree the
// runtime walker matches against target ASTs. The pipeline below is the LOCKED
// engine architecture — its stage order is a contract, not an implementation
// detail, because each stage's output is the next one's precondition:
//
//  1. Substitute every placeholder in pat.Source with a reserved-prefix
//     identifier (e.g. `$X` → `__META_AST_X`) chosen from cfg.Reserved.
//     Sequence placeholders use a `SEQ_` infix; wildcards a `WILD_`/`SEQWILD_`
//     infix plus a per-occurrence index so multiple wildcards never collide.
//  2. Try EVERY cfg.Wrappers entry. The substituted source is spliced
//     between Prefix and Suffix and parsed via treesitter.Parser.Parse, and
//     each wrapper whose parse carries no ERROR node and which HOSTS the
//     fragment contributes a candidate variant. The union of the distinct
//     candidates is what the walk matches — see engine_variants.go for the
//     three hosting rules and the dedupe.
//  3. Walk each surviving tree to find the node that hosts the substituted-
//     source byte range — that's the EffectiveRoot, the starting point the
//     runtime walker uses against target nodes. Index every reserved-prefix
//     identifier descendant by (start_byte, end_byte) so the walker can
//     recognize placeholder positions during traversal.
//
// Tree-sitter Close discipline: the parser
// is closed via defer; each parsed Tree is owned by its PatternTree and
// released via PatternTree.Close. Rejected and deduped-away wrapper attempts
// close their trees inline.

package ast

import (
	"context"
	"errors"
	"fmt"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
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

	// hasSeq is true when at least one placeholder is a sequence. The walker
	// consults it before every chain probe, so a pattern with no $$$ pays
	// nothing for the sequence machinery on its hot path.
	hasSeq bool
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

// errCompileNoWrapper is returned when no wrapper yields a candidate. The
// error message lists every wrapper attempted and, for each, the specific
// reason it contributed none: a parse error carrying its ERROR-node summary, a
// parse that did not host the fragment, or exclusion by a context pin.
var errCompileNoWrapper = errors.New("ast/engine: pattern did not compile under any context wrapper")

// compilePattern compiles a pattern and returns its FIRST candidate — the
// hosting wrapper earliest in cfg.Wrappers order. It is the single-tree view of
// compilePatternVariants, kept for callers that reason about one tree; the
// match and where-tree paths take the whole variant set. The discarded
// candidates are closed here, so the returned PatternTree is the only tree the
// caller owns.
func compilePattern(ctx context.Context, pat Pattern, cfg LangConfig) (*PatternTree, error) {
	variants, narrowed, err := compilePatternVariants(ctx, pat, cfg, "")
	if err != nil {
		return nil, err
	}
	// The single-tree view keeps only the first candidate; both the other
	// candidates and any narrowed member variant are trees this caller must
	// release, or they leak.
	closeVariants(narrowed)
	closeVariants(variants[1:])
	return variants[0].Tree, nil
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
		case KindLiteralDollar:
			// The `$$` escape: emit one literal `$` for the placeholder's
			// byte range and bind NO capture. It appends no substitution, so
			// the literal never reaches indexPlaceholders / the walker (whose
			// Kind-switches assume only capture kinds). `continue` skips the
			// post-switch subs append.
			b.WriteByte('$')
			cursor = ph.OffsetEnd
			continue
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

// buildPatternTree indexes every reserved-prefix identifier in the parsed tree
// by (start_byte, end_byte) so the runtime walker can recognize placeholder
// positions, and assembles the PatternTree around the EffectiveRoot.
//
// The effective root and its depth are supplied by the caller rather than
// derived here: hosting rule 2 roots a pattern at an ABSORBED child, which is
// one level below the smallest node covering the fragment, so the covering node
// is not always the answer.
func buildPatternTree(
	tree *sitter.Tree,
	full string,
	w ContextWrapper,
	subs []substitution,
	cfg LangConfig,
	effective *sitter.Node,
	depth int,
) (*PatternTree, error) {
	root := tree.RootNode()
	if root == nil {
		return nil, fmt.Errorf("ast/engine: parsed tree has nil root")
	}
	if effective == nil {
		return nil, fmt.Errorf("ast/engine: buildPatternTree called with no effective root")
	}

	prefixLen := uint32(len(w.Prefix))

	placeholders := indexPlaceholders(root, []byte(full), subs, cfg)

	hasSeq := false
	for _, ph := range placeholders {
		if ph.Kind == KindSeq || ph.Kind == KindSeqWild {
			hasSeq = true
			break
		}
	}

	return &PatternTree{
		Tree:              tree,
		Root:              effective,
		WrapperSkip:       depth,
		Placeholders:      placeholders,
		SubstitutedSource: full,
		PrefixLen:         int(prefixLen),
		LangCfg:           cfg,
		hasSeq:            hasSeq,
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
