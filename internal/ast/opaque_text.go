// SPDX-License-Identifier: Apache-2.0

// opaque_text.go — the span-gap literal mechanism: the matcher's whole-text
// comparison for grammar kinds whose own content bytes no child covers, and the
// compile-time refusal that keeps a placeholder from landing inside one.
//
// THE PROBLEM. matchNode decides token equality by text ONLY for a node with no
// children of any kind, because that is what "leaf" means: a node holding
// children is one whose children the matcher compares one by one. That reading
// is right until a grammar produces a node whose children do NOT cover its own
// byte span. tree-sitter-go parses `"hello"` into an interpreted_string_literal
// with exactly two anonymous children — the opening and closing quote — and the
// content between them produces NO node at all. The matcher recursed into the
// child list, compared quote to quote twice, and never read `hello`: those bytes
// sat in a span gap nothing was ever compared against.
//
// WHAT THAT COST. Every byte in such a gap was a wildcard. An inlined literal
// written into a pattern constrained NOTHING, so `fmt.Errorf("specific text",
// $$$ARGS)` matched every fmt.Errorf call in the corpus. On the match path that
// is a wrong answer; on the REPLACE path it is silent corruption, because the
// replacement template then substitutes ITS literal over a target whose literal
// differed. The rewrite parses cleanly, so the re-parse gate cannot catch it and
// the dry-run diff renders it as though intended.
//
// THE FIX, AND WHY IT IS PER-LANGUAGE RATHER THAN GENERAL. A grammar declares
// its span-gap kinds in LangConfig.OpaqueTextKinds and the matcher compares
// those nodes' WHOLE TEXT, byte for byte, instead of descending into them. The
// alternative — comparing every node's uncovered gaps everywhere — would have to
// tell a gap carrying content from a gap carrying only the layout whitespace
// between two real tokens, and that discrimination is exactly the
// whitespace-insensitivity the matcher exists to provide. A declared list keeps
// the comparison where the measurement says content lives and nowhere else. The
// declarations are MEASURED by TestOpaqueTextCensus, never hand-guessed, in the
// same discipline LayoutTokens and CommentKinds already follow.
//
// WHY WHOLE-TEXT COMPARE IS SAFE HERE. Placeholder substitution runs BEFORE the
// parse, so a placeholder cannot appear inside a literal's content by accident —
// it can only appear there because the caller wrote it there. That case is
// refused at compile time by checkNoPlaceholderInsideOpaqueText below rather
// than silently comparing a reserved identifier as literal text, which would
// turn the old wildcard into an equally silent never-match.
//
// ALIGNMENT. An opaque node records ONE alignment entry spanning the whole node
// and its children record none, because the walk returns before descending. That
// keeps the alignment record's leaf-only invariant intact — the entries stay
// non-overlapping and in ascending pattern-byte order — while covering bytes
// that previously earned no entry at all.

package ast

import (
	"fmt"
	"slices"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// isOpaqueTextKind reports whether cfg declares kind as a span-gap kind whose
// text the matcher compares whole. The length test comes first because it is the
// cheapest and because most grammars declare none, so the common case pays one
// comparison on a path that runs for every candidate node.
func isOpaqueTextKind(cfg LangConfig, kind string) bool {
	return len(cfg.OpaqueTextKinds) > 0 && slices.Contains(cfg.OpaqueTextKinds, kind)
}

// matchOpaqueText compares one span-gap node's WHOLE text against the target's
// and records the single alignment entry that covers it. Called from matchNode
// once the kinds have already been compared equal, so a mismatch here is a
// content difference and nothing else.
//
// THE COMPARISON IS BYTE-EXACT, deliberately, and does not take the
// TrimsAnonTokenWhitespace path a childless token can: that trim exists for
// ANONYMOUS tokens a JSX grammar absorbed layout whitespace into, and every kind
// reaching here is a named literal or comment whose whitespace is content. A
// space inside a string is a different string.
//
// ONE ALIGNMENT ENTRY, spanning the whole node, because the walk returns without
// descending — so the node's children record none and the alignment record stays
// non-overlapping. Before this path existed those content bytes earned no entry
// at all, since nothing was ever compared against them.
func matchOpaqueText(p, t *sitter.Node, patSrc, src []byte, caps *Captures) bool {
	if !tokenTextMatches(patSrc[p.StartByte():p.EndByte()], src[t.StartByte():t.EndByte()], false) {
		return false
	}
	caps.recordAlignRange(p, t.StartByte(), t.EndByte())
	return true
}

// checkNoPlaceholderInsideOpaqueText refuses a compiled pattern in which a
// substituted placeholder identifier lands INSIDE an opaque-text node without
// being that node's entire text.
//
// WHY THIS IS AN ERROR AND NOT A BEST EFFORT. An opaque node is compared whole,
// so a reserved identifier sitting inside one would be compared as literal
// source text and could never match anything — a pattern that silently counts
// zero, which reads exactly like a pattern that correctly found nothing. The old
// behavior for the same input was a silent wildcard; replacing one silent wrong
// answer with another is not a fix. The refusal names the placeholder as the
// caller spelled it and names both ways forward: capture the WHOLE literal and
// constrain it with a where-tree leaf, or write `$$` for a literal dollar.
//
// A placeholder whose text IS the node's entire text is fine and is NOT refused:
// that node is a placeholder position, matchNode binds it before the opaque
// comparison is ever reached, and grammars that surface a string's content as
// its own node (Python's string_content) support exactly that spelling.
func checkNoPlaceholderInsideOpaqueText(pt *PatternTree, subs []substitution, patternSource string) error {
	if pt == nil || pt.Tree == nil || len(subs) == 0 || len(pt.LangCfg.OpaqueTextKinds) == 0 {
		return nil
	}
	root := pt.Tree.RootNode()
	if root == nil {
		return nil
	}
	src := pt.SubstitutedSource
	var found error
	walkAllIncludingAnonymous(root, func(n *sitter.Node) {
		if found != nil || !isOpaqueTextKind(pt.LangCfg, n.Type()) {
			return
		}
		start, end := int(n.StartByte()), int(n.EndByte())
		if start < 0 || end > len(src) || start >= end {
			return
		}
		text := src[start:end]
		for _, s := range subs {
			if text == s.Replacement || !strings.Contains(text, s.Replacement) {
				continue
			}
			found = fmt.Errorf(
				"ast/engine: placeholder %s sits inside a %s literal, whose text is compared whole so a placeholder cannot bind to part of it; "+
					"capture the whole literal with a single placeholder and constrain it with a where-tree `matches` or `equals` leaf, or write `$$` to mean a literal dollar sign",
				placeholderSpelling(patternSource, s.Placeholder), n.Type())
			return
		}
	})
	return found
}

// placeholderSpelling renders a placeholder as the caller wrote it, read back
// from the raw DSL source rather than reconstructed from its Kind and Name — a
// reconstruction would quietly disagree with the source for any spelling the
// parser accepts and the reconstruction does not reproduce.
func placeholderSpelling(patternSource string, ph Placeholder) string {
	if ph.OffsetStart >= 0 && ph.OffsetEnd <= len(patternSource) && ph.OffsetStart < ph.OffsetEnd {
		return patternSource[ph.OffsetStart:ph.OffsetEnd]
	}
	return "$" + ph.Name
}

// walkAllIncludingAnonymous calls fn on every node in the subtree rooted at n in
// pre-order, ANONYMOUS CHILDREN INCLUDED. walkAll beside it descends only named
// children, which is right for placeholder indexing (a substitution always parses
// to a named identifier) and wrong here: a grammar is free to surface an
// opaque-text kind under an anonymous parent, and a walk that could not reach it
// would report a clean pattern for one that cannot match.
func walkAllIncludingAnonymous(n *sitter.Node, fn func(*sitter.Node)) {
	if n == nil {
		return
	}
	fn(n)
	for i := range int(n.ChildCount()) {
		walkAllIncludingAnonymous(n.Child(i), fn)
	}
}
