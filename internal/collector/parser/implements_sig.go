// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// sigKeyLeafSep is the rendered separator between a resolved leaf's scope and
// its name. A scope ID and an identifier can neither of them contain a NUL.
const sigKeyLeafSep = "\x00"

// resolveSigKey renders a declaration's composed signature as ONE resolved key.
//
// THE CHUNKER DECIDED COMPOSITION AND THIS DECIDES IDENTITY. Each TypeExpr
// arrives as a Shape carrying TypeExprLeafSep placeholders plus the leaves'
// written spellings; this interleaves the two, substituting each leaf's RESOLVED
// form. Resolution uses the DECLARING file's site, because a type written
// `other.T` names a scope only that file's own imports can bind — the same
// contract resolveTypeText documents and the reason the caller must not hand it
// a referencing site.
//
// A DECLINING LEAF RENDERS `ext:<name>`, NEVER THE EMPTY STRING. An empty
// rendering silently makes two different signatures compare equal, which is the
// FALSE-MATCH direction — the one direction a precision floor cannot absorb.
// `ext:` is also what makes two unresolvable-but-differently-named leaves stay
// distinct from each other.
//
// The shape is `(params)(results)`, comma-joined in source order, with the
// receiver absent — a concrete method and the interface spec it satisfies must
// render identically or nothing ever matches.
func resolveSigKey(ix *declIndex, ref *treesitter.RefSite, sig *treesitter.SigFacts) string {
	if sig == nil {
		return ""
	}
	var b strings.Builder
	b.Grow(sigKeyHint(sig))
	b.WriteString("(")
	writeResolvedExprs(&b, ix, ref, sig.Params)
	b.WriteString(")(")
	writeResolvedExprs(&b, ix, ref, sig.Results)
	b.WriteString(")")
	return b.String()
}

// sigKeyHint pre-sizes the builder from the shapes and leaves already in hand,
// so a signature costs one allocation rather than a growth sequence.
func sigKeyHint(sig *treesitter.SigFacts) int {
	n := 4
	for _, group := range [][]treesitter.TypeExpr{sig.Params, sig.Results} {
		for _, e := range group {
			n += len(e.Shape) + 1
			for _, l := range e.Leaves {
				// A resolved leaf is a scope plus a separator plus a name, and a
				// scope is typically longer than the spelling it came from.
				n += len(l) + 24
			}
		}
	}
	return n
}

// writeResolvedExprs writes one comma-joined group of type expressions.
func writeResolvedExprs(b *strings.Builder, ix *declIndex, ref *treesitter.RefSite, exprs []treesitter.TypeExpr) {
	for i, e := range exprs {
		if i > 0 {
			b.WriteString(",")
		}
		writeResolvedExpr(b, ix, ref, e)
	}
}

// writeResolvedExpr interleaves one expression's shape with its resolved leaves.
//
// The leaf count and the separator count agree BY CONSTRUCTION — the chunker
// appends exactly one leaf per separator it writes — so a mismatch means the two
// halves have drifted, and the surplus separators render `ext:` rather than
// consuming a leaf that is not there.
func writeResolvedExpr(b *strings.Builder, ix *declIndex, ref *treesitter.RefSite, e treesitter.TypeExpr) {
	parts := strings.Split(e.Shape, treesitter.TypeExprLeafSep)
	for i, part := range parts {
		b.WriteString(part)
		if i == len(parts)-1 {
			break
		}
		if i < len(e.Leaves) {
			b.WriteString(resolvedLeaf(ix, ref, e.Leaves[i]))
			continue
		}
		b.WriteString("ext:")
	}
}

// goPredeclaredTypes is Go's universe block of type names.
//
// THEY MUST RENDER `ext:`, AND resolveTypeText WILL NOT DO IT. That helper has
// three outcomes and only the qualifier-unbound one declines: an UNQUALIFIED
// spelling resolves to "the site's own scope, base-named". `error` carries no
// qualifier, so it would resolve to the DECLARING package's scope — and an
// interface in one package would then render `dir:a\x00error` against an
// implementer's `dir:b\x00error` and never match it. Since nearly every Go
// signature names error, string or int, that would silently destroy
// cross-package matching while every same-package fixture stayed green.
//
// THE SET IS GO-SPECIFIC AND SO IS EVERY CALLER, structurally: SigFacts is
// produced by the Go type-facts arm alone, so no other language's spelling ever
// reaches this map.
//
// In Go an unqualified type name can only be declared in the SAME package
// (scope-qualifying is right), predeclared (this set), or a type parameter
// (excluded from satisfaction by a separate rule). So this set is exactly the
// gap between the shared resolver's behavior and a resolved signature identity.
//
// RESIDUAL: a package that SHADOWS a predeclared name with its own type — legal,
// and vanishingly rare — renders ext:<Name> here. That costs precision only
// between two packages that both shadow the same name with different types,
// which is strictly narrower than the recall the rule preserves.
var goPredeclaredTypes = map[string]struct{}{
	"any": {}, "bool": {}, "byte": {}, "comparable": {}, "complex64": {},
	"complex128": {}, "error": {}, "float32": {}, "float64": {}, "int": {},
	"int8": {}, "int16": {}, "int32": {}, "int64": {}, "rune": {}, "string": {},
	"uint": {}, "uint8": {}, "uint16": {}, "uint32": {}, "uint64": {}, "uintptr": {},
}

// resolvedLeaf renders one written type spelling as its resolved identity.
//
// A LEAF RESOLVING OUTSIDE THE INDEXED UNIVERSE RENDERS `ext:` REGARDLESS OF
// WHETHER ITS QUALIFIER BOUND, and that is the whole of the second rule here.
// Without it one external package renders TWO different leaves depending only on
// how the importing file SPELLED its import. A non-aliased
// `import "github.com/stripe/stripe-go/v83"` binds under the path's last segment
// `v83`, so `stripe.CheckoutSession` finds no bind, declines, and renders
// `ext:CheckoutSession`; the same type in a file importing it as
// `stripe "…/v83"` binds and renders `dir:github.com/stripe/stripe-go/v83` plus
// the name. Two spellings of ONE type produced two keys, and an interface and
// its implementer that disagreed only about import style never matched.
// Measured on the frozen corpora before the fix: 308 qualified-leaf declines on
// knowledge across 19 qualifiers, 135 on agent across 12.
//
// hasScope IS THE TEST, not the shape of the scope string. It asks the index
// whether the scope contributes any declaration at all, which is the same
// question the external-qualifier rung asks and the one the Go binds arm exists
// to defer ("THE ARM MAKES NO IN-REPO / OUT-OF-REPO JUDGMENT"). It is only
// answerable once the index is complete, which is why the caller is a drained
// pending list rather than the index-build loop.
//
// THE EXTERNAL LEAF KEEPS THE QUALIFIER AS WRITTEN, which is what makes the
// rule above safe to apply as widely as it is. Collapsing to a bare base name
// would also merge two DIFFERENT external packages that happen to declare the
// same type name — measured on the frozen corpora as 52 such base names on
// knowledge and 54 on agent, `Client` alone reachable under http, pubsub, gcs
// and eight more. Carrying the qualifier separates them while still collapsing
// the aliased/non-aliased split, because Go source writes
// `stripe.CheckoutSession` either way: the import spelling changes what BINDS,
// never what the type expression SAYS. See externalLeafName.
func resolvedLeaf(ix *declIndex, ref *treesitter.RefSite, text string) string {
	if _, predeclared := goPredeclaredTypes[text]; !predeclared {
		if tr := resolveTypeText(ref, text); tr.Scope != "" && ix.hasScope(tr.Scope) {
			return tr.Scope + sigKeyLeafSep + tr.Name
		}
	}
	// The spelling named no INDEXED scope — a stdlib type, an unindexed or
	// external package, or a type parameter.
	return "ext:" + externalLeafName(ref, text)
}

// externalLeafName renders the identity of a type that resolved outside the
// indexed universe: `<qualifier>.<base>` when the source wrote a qualifier, and
// `<base>` alone when it did not.
//
// IT SPLITS WITH THE SAME SPLITTER THE RESOLVER USES, deliberately. The
// qualifier this writes is byte-for-byte the one resolveTypeText looked up in
// the file's binds, so the leaf can never disagree with the resolution attempt
// that produced it — a second hand-rolled split at the last dot would be a
// separate rule free to drift.
//
// AN ABSENT QUALIFIER IS A DEFINED CASE, NOT A LEFTOVER. Two populations reach
// it, and neither has a package to name. PREDECLARED types belong to Go's
// universe block, and synthesizing a qualifier for them from the declaring file
// would be actively wrong: `error` has to render one way in every file of every
// package, or an interface and its cross-package implementer stop matching on
// nearly every signature in the tree. TYPE PARAMETERS likewise name no package,
// and are excluded from satisfaction by a separate rule. So the leaf records
// what the source wrote and invents nothing; a qualified and an unqualified
// spelling of one base name stay distinct because the qualified form carries the
// dot.
//
// THE RESIDUAL, stated because it is the shape of the next collision: two
// different modules imported under the SAME qualifier text in different files
// still render one leaf. The scoring-harness control measures exactly that
// population rather than assuming it empty.
func externalLeafName(ref *treesitter.RefSite, text string) string {
	if ref == nil {
		return baseDeclName(text)
	}
	qualifier, name := splitQualifier(ref.Lang, text)
	base := baseDeclName(name)
	if qualifier == "" {
		return base
	}
	return baseDeclName(qualifier) + "." + base
}

// resolveDeclEmbeds resolves a declaration's embedded-type spellings, DROPPING
// the ones that name no in-repo scope and reporting whether any was dropped.
//
// IT FILTERS AFTER THE SHARED CALL RATHER THAN ASKING FOR A FILTERED VARIANT.
// resolveTypeTexts deliberately PRESERVES POSITION, returning a zero typeRef for
// a declining entry, because a result slice's ResultIndex indexes into it;
// changing that contract to suit this consumer would silently rebind every
// multi-value assignment in the tree.
// A PREDECLARED NAME IS A DROP HERE TOO, for the reason goPredeclaredTypes
// documents: it carries no qualifier, so the shared resolver hands back the
// declaring file's own scope rather than declining. `error` is the case that
// matters — an interface embedding it has a method set the syntax cannot see all
// of, and recording `error` as an in-repo embed would report that set as fully
// known. `comparable` and `any` are embeddable in constraints and take the same
// treatment.
func resolveDeclEmbeds(ref *treesitter.RefSite, texts []string) (embeds []typeRef, extEmbed bool) {
	for i, tr := range resolveTypeTexts(ref, texts) {
		_, predeclared := goPredeclaredTypes[texts[i]]
		if tr.Scope == "" || predeclared {
			extEmbed = true
			continue
		}
		embeds = append(embeds, tr)
	}
	return embeds, extEmbed
}
