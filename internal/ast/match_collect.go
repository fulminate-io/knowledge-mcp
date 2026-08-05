// SPDX-License-Identifier: Apache-2.0

// match_collect.go — turning a file's raw walk hits into the deduped result
// set. Split out of match_walk.go so each file stays under the file-size
// warning threshold while keeping match_walk.go focused on the worker pool and
// the per-node match application.
//
// Two responsibilities live here: absorbMatch + its dedupe bookkeeping
// (variantMatch/dedupeSlot/outerSpan), which collapse two variants that found
// the SAME outer span into one entry; and toRawMatch, which builds the
// per-match RawMatch. Both are called from collectMatches (match_walk.go).

package ast

import (
	sitter "github.com/smacker/go-tree-sitter"
)

// variantMatch is a RawMatch plus the index of the variant that produced it,
// so the dedupe can prefer the earliest candidate regardless of the order the
// query cursors and the shared walk emitted them in.
type variantMatch struct {
	match   RawMatch
	variant int
}

// dedupeSlot records where a span's surviving match sits in the output and
// which variant produced it.
type dedupeSlot struct {
	pos     int
	variant int
}

// absorbMatch merges one match into the per-file result set, collapsing two
// variants that found the SAME outer span into one entry stamped by the
// earliest candidate.
//
// This is correctness, not tidiness. Two RawMatches with identical outer spans
// reach replace.go::buildFileEdits, which reads identical or nested spans as an
// overlap and REFUSES THE WHOLE FILE — so an unmerged union would silently
// break every replace over a two-context pattern.
func absorbMatch(out *[]RawMatch, seen map[byteRange]dedupeSlot, vm variantMatch) {
	key := outerSpan(vm.match)
	if slot, ok := seen[key]; ok {
		if vm.variant < slot.variant {
			(*out)[slot.pos] = vm.match
			seen[key] = dedupeSlot{pos: slot.pos, variant: vm.variant}
		}
		return
	}
	seen[key] = dedupeSlot{pos: len(*out), variant: vm.variant}
	*out = append(*out, vm.match)
}

// outerSpan is the dedupe key: the byte range of the reserved "match" capture,
// which is the outer node the pattern bound.
func outerSpan(rm RawMatch) byteRange {
	outer := rm.Captures["match"]
	return byteRange{Start: outer.StartByte, End: outer.EndByte}
}

// toRawMatch builds the per-match RawMatch from a successful structural
// match. The "match" capture key is reserved for the outer-node binding;
// individual placeholder captures already live in caps.byName. The
// internal "$match" key (synthesized by matchTreeWithNodes for where-tree
// resolution) is filtered out — the bare "match" key is the user-facing
// surface, $match is engine-internal scope plumbing.
//
// DroppedSpans is SEEDED with the variant's absorbed spans before the walker's
// own dropped spans are appended. An absorbed token was never compared against
// the target, so it earns no alignment entry; without the seed an identity
// template still carrying the pattern's `;` would splice over source that ends
// without one and emit `;;`.
func toRawMatch(outer *sitter.Node, relPath string, caps *Captures, src []byte, v *compiledVariant) RawMatch {
	dropped := append([]byteRange(nil), v.Absorbed...)
	dropped = append(dropped, caps.copyDropped()...)
	rm := RawMatch{
		FilePath:         relPath,
		StartLine:        int(outer.StartPoint().Row) + 1,
		EndLine:          int(outer.EndPoint().Row) + 1,
		Captures:         make(map[string]Capture, len(caps.byName)+1),
		Alignment:        caps.copyAligns(),
		DroppedSpans:     dropped,
		CommentSpans:     caps.copyComments(outer.StartByte(), outer.EndByte()),
		CompiledKind:     v.RootKind,
		CompiledContexts: v.Contexts,
	}
	rm.Captures["match"] = nodeToCapture(outer, src)
	for name, cap := range caps.byName {
		if name == "$match" {
			continue
		}
		rm.Captures[name] = cap
	}
	return rm
}
