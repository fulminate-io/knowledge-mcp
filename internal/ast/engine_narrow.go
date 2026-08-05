// SPDX-License-Identifier: Apache-2.0

// engine_narrow.go — the grammar-derived member-keyword narrowing and its
// disclosure. Split from engine_variants.go and match.go only to keep both under
// the per-file line budget; the filter, the reason and DescribeNarrowed are one
// subject, and compilePatternVariants (engine_variants.go) is its only caller.

package ast

import (
	"slices"

	sitter "github.com/smacker/go-tree-sitter"
)

// narrowMemberKeywordVariants drops a member-context variant whose root NAME
// field covers pattern bytes that another surviving variant reads as an
// anonymous keyword token — the grammar's own definition of "the leading token
// is a keyword, not a member name". It is the ticket's candidate (i) without the
// per-grammar keyword table that candidate implied: `ast explain` shows that
// javascript reads `if (...) {...}` as a method_definition whose NAME is `if`
// under the class-body wrapper and as an if_statement whose leading `if` is an
// anonymous token under the statement wrapper, so the grammar answers "is this a
// keyword" with no hand-kept list.
//
// The NAME-field scoping is load-bearing and was measured, not reasoned: Java's
// `return $X;` compiles to a field_declaration whose keyword `return` lands in
// the TYPE position while the name belongs to the variable_declarator holding
// the placeholder, so a rule phrased over the root's name field does not fire
// there and Java keeps both variants. A rule phrased over "the leading pattern
// bytes" would drop it.
//
// A context:"member" pin skips the filter entirely — that caller is asking for
// the member reading, including for a class member genuinely named `if`, which
// is legal JavaScript, so the pin is the escape hatch that keeps the union
// honest rather than opinionated. And a drop that would leave ZERO variants is
// refused: a narrowing that turns a working pattern into a hard compile error is
// worse than a disclosed extra reading.
//
// The same two-variant ambiguity affects javascript `while ($C) { $$$B }` and
// narrows it the same way; `for (...)`, `switch (...)` and `try {...} catch`
// never produce a member variant and are untouched. The rule generalizes past
// the JS family by construction: csharp `if (...) {...}` reads its `if` as a
// constructor name and narrows too. The filter is serial and runs once per
// compile over at most four variants — off the per-file walk entirely.
func narrowMemberKeywordVariants(variants []patternVariant, pinContext string) (kept, narrowed []patternVariant) {
	if pinContext == contextMember {
		return variants, nil
	}
	drop := make([]bool, len(variants))
	dropped := 0
	for i := range variants {
		if !slices.Contains(variants[i].Contexts, contextMember) {
			continue
		}
		name := variants[i].Tree.Root.ChildByFieldName("name")
		if name == nil {
			// No name field — this is the leg that spares Java's field_declaration,
			// whose name belongs to a child declarator, not the root.
			continue
		}
		relStart := name.StartByte() - uint32(variants[i].Tree.PrefixLen)
		relEnd := name.EndByte() - uint32(variants[i].Tree.PrefixLen)
		if !readsAsKeyword(variants, i, relStart, relEnd) {
			continue
		}
		if len(variants)-dropped < 2 {
			// Dropping this one would leave nothing; keep it and let the
			// disclosure speak rather than fail the compile.
			continue
		}
		drop[i] = true
		dropped++
	}
	if dropped == 0 {
		return variants, nil
	}
	for i := range variants {
		if drop[i] {
			narrowed = append(narrowed, variants[i])
		} else {
			kept = append(kept, variants[i])
		}
	}
	return kept, narrowed
}

// readsAsKeyword reports whether any variant other than variants[skip] covers
// the FRAGMENT-relative range [relStart,relEnd) with an anonymous childless
// token. The range is mapped into each candidate's own wrapped-source coordinate
// by adding its prefix length; the substituted fragment is byte-identical across
// wrappers (substitutePlaceholders runs once), so only the prefix differs and
// the arithmetic is exact.
func readsAsKeyword(variants []patternVariant, skip int, relStart, relEnd uint32) bool {
	for j := range variants {
		if j == skip {
			continue
		}
		o := variants[j].Tree
		if o == nil || o.Root == nil {
			continue
		}
		cover := smallestChildCovering(o.Root, relStart+uint32(o.PrefixLen), relEnd+uint32(o.PrefixLen))
		if cover != nil && !cover.IsNamed() && cover.ChildCount() == 0 {
			return true
		}
	}
	return false
}

// smallestChildCovering is smallestNodeCovering's ALL-CHILDREN sibling: it
// descends through anonymous tokens too, so it can return the keyword token a
// named-only descent would step over. The narrowing needs exactly that — the
// question is whether the member variant's NAME bytes are an anonymous keyword
// in another variant, and a keyword IS an anonymous childless token.
func smallestChildCovering(n *sitter.Node, start, end uint32) *sitter.Node {
	if n == nil || n.StartByte() > start || n.EndByte() < end {
		return nil
	}
	for i := range int(n.ChildCount()) {
		c := n.Child(i)
		if c != nil && c.StartByte() <= start && c.EndByte() >= end {
			return smallestChildCovering(c, start, end)
		}
	}
	return n
}

// narrowedReason is the sentence every DescribeNarrowed entry carries: the FACT
// (the member reading was dropped because this grammar reads the leading token
// as a keyword) and the REMEDY (context:"member" restores it). It names both so
// the disclosure is actionable on the success path, where every real caller
// lives, not only in a compile-failure message.
const narrowedReason = `member reading dropped: this grammar reads the pattern's leading token as a keyword, not a member name — pin context:"member" to keep the member variant`

// DescribeNarrowed renders the member-context variants the keyword rule dropped,
// each carrying narrowedReason. It is a SIBLING channel to Describe rather than a
// field on a surviving variant, because a dropped variant is by construction no
// longer in cp.Variants for Describe to iterate.
func (cp *CompiledPattern) DescribeNarrowed() []CompiledVariant {
	if cp == nil {
		return nil
	}
	out := make([]CompiledVariant, 0, len(cp.Narrowed))
	for i := range cp.Narrowed {
		v := &cp.Narrowed[i]
		out = append(out, CompiledVariant{
			Contexts: v.Contexts,
			Wrappers: v.Wrappers,
			RootKind: v.RootKind,
			Reason:   narrowedReason,
		})
	}
	return out
}
