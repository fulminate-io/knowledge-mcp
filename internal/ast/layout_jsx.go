// SPDX-License-Identifier: Apache-2.0

// layout_jsx.go — the anonymous-token text comparison, and the whitespace trim
// some grammars need it to apply.
//
// THE PROBLEM IT SOLVES. A grammar may absorb inter-child layout whitespace into
// the LEADING ANONYMOUS TOKEN of the following node rather than surfacing it as
// a child of its own: the JSX grammars do this, so a `<` token can span "\n<"
// and a `</` token can span "\n</" purely because the author put the child on
// its own line. Child COUNTS are unchanged, so alignment succeeds and the
// mismatch surfaces at the token text comparison — which then reads the source's
// line breaks as a constraint the pattern must reproduce byte for byte.
//
// WHY THIS IS NOT THE LAYOUT-TOKEN SKIP. LangConfig.LayoutTokens drops a WHOLE
// CHILD from both child lists before alignment. That mechanism cannot reach this
// one: there is no extra child to drop, only whitespace living inside a token
// that also carries meaningful bytes. The two are complementary and a grammar
// may declare either, both, or neither.
//
// THE SCOPE OF THE TRIM IS DELIBERATELY NARROW. It applies only to ANONYMOUS
// CHILDLESS tokens, and only under a grammar that declares the absorption.
// Named leaves keep byte-exact comparison, so whitespace inside a string
// literal, an identifier or any other content-bearing leaf still discriminates.
// Trimming is not "ignore whitespace": two tokens whose trimmed texts differ
// still fail to match.

package ast

import (
	"bytes"
	"unicode"
)

// tokenTextMatches reports whether a pattern token's source text matches a
// target token's. When trimWhitespace is set, both sides are compared with
// leading and trailing whitespace removed — BOTH sides, in this one place, which
// is what makes a one-line pattern reach a multi-line body AND a multi-line
// pattern reach a one-line body. Trimming one side only would fix one direction
// and corrupt the other.
//
// It takes byte slices rather than nodes so the gating decision can be exercised
// directly, without routing through a grammar's tokenization to produce the pair
// under test.
//
// ALLOCATION: this runs for every childless token of every candidate node, so it
// must not allocate. bytes.TrimSpace reslices in place and bytes.Equal compares
// without copying; a caller passing node source slices rather than a per-call
// content copy keeps the whole path allocation-free.
func tokenTextMatches(patText, tgtText []byte, trimWhitespace bool) bool {
	if !trimWhitespace {
		return bytes.Equal(patText, tgtText)
	}
	return bytes.Equal(bytes.TrimSpace(patText), bytes.TrimSpace(tgtText))
}

// alignedTokenRange narrows a compared token's source range to the bytes the
// comparison actually accepted it on: the whole token normally, and the
// whitespace-trimmed interior when the comparison trimmed.
//
// THE WRITE SIDE DEPENDS ON THIS, and it is the reason the trim cannot stop at
// the comparison. The splice reads its anchor list expecting each literal
// anchor's SOURCE text to be its PATTERN text — that equality is what lets it
// recognize the part of a template which merely repeats the pattern. A trimmed
// comparison breaks the equality (a pattern's "<" accepted against a source's
// "\n<"), and an anchor the template no longer agrees with pushes the splice off
// its identity path and onto a reconstruction that reflows the match onto one
// line. Recording the trimmed range restores the equality and leaves the
// absorbed whitespace OUTSIDE every anchor, where the splice's contiguous source
// slices carry it through untouched — the same way an unaligned region between
// two anchors already survives.
//
// Returns offsets, not a slice, because its caller records byte ranges; it
// allocates nothing.
// It derives the range from bytes.TrimSpace — the same trim tokenTextMatches
// compares under — rather than from its own cutset, so the recorded range and
// the accepted comparison cannot describe different bytes.
func alignedTokenRange(src []byte, start, end uint32, trimmed bool) (uint32, uint32) {
	if !trimmed {
		return start, end
	}
	text := src[start:end]
	kept := bytes.TrimSpace(text)
	if len(kept) == 0 {
		// A token that is entirely whitespace anchors nothing; an empty range
		// at its start keeps every byte of it on the unanchored side rather
		// than producing an inverted one.
		return start, start
	}
	lead := len(text) - len(bytes.TrimLeftFunc(text, unicode.IsSpace))
	return start + uint32(lead), start + uint32(lead+len(kept))
}
