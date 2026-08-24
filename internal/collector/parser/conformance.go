// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// conformRef is ONE supertype a declaration declared, as written and as
// classified, carried UNRESOLVED.
//
// THERE IS NO Target FIELD, AND ITS ABSENCE IS THE DESIGN. Resolution happens
// in the emitter, against a COMPLETE declaration index; a target stored here
// would have to be computed while the index is still being built, where a
// lookup sees only the files indexed so far and the answer depends on file
// order. Carrying the spelling forward is what lets the lookup run at the one
// point where its answer is stable.
type conformRef struct {
	// Text is the supertype's spelling as the source wrote it, under the
	// carrier's normalization contract: type arguments stripped, qualifier and
	// any leading namespace separator retained.
	Text string
	// Kind is the clause the supertype was declared under, carried through to
	// the emitted edge unmodified.
	Kind treesitter.ConformanceKind
}

// captureDeclConforms copies one declaration's declared supertypes onto the
// shape resolution reads later.
//
// IT RESOLVES NOTHING, AND IT TAKES NO REFERENCE SITE. Both are deliberate: a
// signature accepting a site would invite index-build-time resolution back in,
// which is the shape that cannot work, because the index a lookup would consult
// there is half-built.
//
// An entry whose Text is EMPTY is dropped — an empty spelling names nothing and
// could only ever resolve to nothing, so carrying it forward would inflate the
// unresolvable count with entries that were never a supertype. A nil or empty
// input returns nil, so a declaration that declares no supertype costs one
// length check and no allocation, which is the common case in every language.
func captureDeclConforms(decls []treesitter.DeclaredSupertype) []conformRef {
	if len(decls) == 0 {
		return nil
	}
	out := make([]conformRef, 0, len(decls))
	for _, d := range decls {
		if d.Text == "" {
			continue
		}
		out = append(out, conformRef{Text: d.Text, Kind: d.Kind})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
