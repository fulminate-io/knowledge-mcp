// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"sort"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// THE UNQUALIFIED ARM of the resolution ladder — rungs R4 through R7, the ones
// that apply when a reference carries no qualifier. resolveRef dispatches here
// and into resolveQualified, which stays in resolve_walk.go beside the rule
// constants and the shared helpers.
//
// THE SPLIT IS ACROSS FILES AND NOTHING ELSE. These three functions moved
// verbatim when resolve_walk.go reached the 500-line block; the ladder, its
// order and every rung's behavior are unchanged by the move, and the arm is the
// natural seam because resolveRef already dispatched on exactly this boundary.

// resolveUnqualified walks rungs R4 through R7, the four that apply when a
// reference carries no qualifier. It is an arm of resolveRef rather than a
// separate policy — the same shape resolveQualified takes, split out for the
// same reason: the gathered last rung pushes resolveRef past the funlen cap.
//
// THE ORDER OF R4 AGAINST R5/R6 IS THE LANGUAGE'S, read from its profile. R7 is
// last in both orders: it is not a rung that looks anything up, it is what is
// left when every other rung missed.
func resolveUnqualified(
	ix *declIndex, ref *treesitter.RefSite, name string, narrow func([]*declRec) []*declRec,
) refResolution {
	prof := profileFor(ref.Lang)
	importsFirst := prof.ImportsBeatLocals
	if importsFirst {
		if res, ok := resolveImportBound(ix, ref, name, narrow); ok {
			return res
		}
	}
	// THE PROFILE IS THREADED, NOT RE-READ. It is already in hand one line
	// above, and resolveLocalScopes is a different function, so looking it up
	// again there would be a second map lookup per bare reference — 62,509
	// reference edges' worth of one.
	if res, ok := resolveLocalScopes(ix, ref, name, narrow, prof); ok {
		return res
	}
	if !importsFirst {
		if res, ok := resolveImportBound(ix, ref, name, narrow); ok {
			return res
		}
	}
	// R7 — declared nowhere this reference can see.
	return refResolution{Status: RefExternal, Rule: RuleNotDeclared}
}

// resolveImportBound is R4 — a bare name an import bound directly. It reports
// false when the name carries no bind, or when the bind's target scope holds no
// declaration of it, so the caller can place this rung either side of the
// bare-name rungs without the two orders diverging in anything but position.
//
// THE NAME OVERRIDE APPLIES HERE AND ONLY HERE. For a renaming import —
// `import {A as B}`, `use x::y as z`, `from x import a as b` — the reference
// writes B while the target declares A, so the bind carries the declared name
// and the lookup uses it. An empty Bind.Name means the reference's own name is
// already the declared one.
//
// There is deliberately NO bare-name analog of R2X. A bind into an unindexed
// scope simply misses this lookup and falls through: terminating would be wrong
// for a language whose local of that name legally shadows the import, and
// indistinguishable from falling through where no such local can exist.
func resolveImportBound(
	ix *declIndex, ref *treesitter.RefSite, name string, narrow func([]*declRec) []*declRec,
) (refResolution, bool) {
	b, ok := ref.Binds[name]
	if !ok {
		return refResolution{}, false
	}
	lookupName := name
	if b.Name != "" {
		lookupName = b.Name
	}
	if c := narrow(ix.lookup(declKey{Scope: b.Scope, Name: lookupName})); len(c) > 0 {
		return classify(RuleUnqualifiedImport, c), true
	}
	// A BIND THAT NAMES A MEMBER OF A TYPE gets a second lookup keyed on the
	// container the arm recorded. `import static a.b.C.d` binds the bare name
	// "d" while the declaration satisfying it is parented to "C", so the
	// top-level lookup above cannot reach it and nothing else in Bind carries
	// "C". The empty Container every other arm records skips this entirely, so
	// the common path is one string compare.
	//
	// IT IS ATTRIBUTED UNDER RuleQualifiedMember, the constant the qualified
	// rung introduced: the RUNG differs but the fact recorded is the same one —
	// a declaration reached through an import bind's container — and a second
	// constant would split one concept across two names.
	if b.Container != "" {
		if c := narrow(ix.lookup(declKey{Scope: b.Scope, Parent: b.Container, Name: lookupName})); len(c) > 0 {
			return classify(RuleQualifiedMember, c), true
		}
	}
	return refResolution{}, false
}

// resolveLocalScopes is R5 and R6 — the two bare-name rungs that look inside
// the reference's own container and its own scope. It reports false when both
// miss, which is R7's condition and the point at which a locals-first language
// still has its import rung left to try.
//
// THE PROFILE ARRIVES AS A PARAMETER rather than being read here: the single
// caller has already read it, and re-reading would cost a second map lookup per
// bare reference.
func resolveLocalScopes(
	ix *declIndex, ref *treesitter.RefSite, name string, narrow func([]*declRec) []*declRec,
	prof langProfile,
) (refResolution, bool) {
	// R5 — a bare name inside a container resolves against that container
	// first: a sibling member.
	//
	// THE RUNG IS PER-LANGUAGE, because whether a bare name can mean a sibling
	// member at all is a property of the language and not of the ladder. The
	// languages that SKIP it are the ones whose receiverless call carries no
	// implicit receiver — a bare `a()` inside a method is a compile error or a
	// runtime name error rather than a call on self — so binding a sibling
	// there would state an edge the language does not have. Which languages
	// those are, and the execution that decided each one, live in the
	// SkipSiblingRung column of lang_profile.go; ruby and java were run too and
	// KEEP the rung, so this is a gate and never a removal.
	if ref.Parent != "" && !prof.SkipSiblingRung {
		if c := narrow(ix.lookup(declKey{Scope: ref.Scope, Parent: baseDeclName(ref.Parent), Name: name})); len(c) > 0 {
			return classify(RuleSiblingMember, c), true
		}
	}

	// R6 — a bare name declared at the top of the reference's own scope.
	//
	// THE ZERO-DOT-SCOPE PATH IS THE EXISTING CODE, NOT A GATHER THAT AGREES
	// WITH IT. Every reference in the measured corpus takes this branch, so it
	// is kept verbatim and the gather is guarded behind a nil-map length check
	// — which is what makes the characterization guard meaningful rather than
	// circular, and what keeps the common path's cost at one len().
	if len(ref.DotScopes) == 0 {
		if c := narrow(ix.lookup(declKey{Scope: ref.Scope, Name: name})); len(c) > 0 {
			return classify(RuleOwnScope, c), true
		}
		return refResolution{}, false
	}

	// R6 GATHERED — the file's namespace is a UNION of its own scope and every
	// scope a dot import folded in, so candidates are collected from all of
	// them and CARDINALITY decides, exactly as it does on every other rung.
	//
	// ORDER IS NOT PRECEDENCE. Own scope is gathered first and the dot scopes
	// in ascending scope-ID order PURELY so a group's member edges are
	// byte-identical across collects; every member carries Confidence 1/N and
	// none is preferred. There is no shadowing rule to encode: at package level
	// Go FORBIDS a dot-import collision rather than resolving it, so a
	// multi-candidate result is a program the language rejects, and picking a
	// winner would state a fact the language refuses to state.
	scopes := make([]string, 0, len(ref.DotScopes))
	for s := range ref.DotScopes {
		scopes = append(scopes, s)
	}
	sort.Strings(scopes)

	candidates := narrow(ix.lookup(declKey{Scope: ref.Scope, Name: name}))
	// ownCount is what tells the two single-candidate outcomes apart. It is
	// read from a count already in hand rather than re-derived by comparing the
	// winner's Scope to ref.Scope.
	ownCount := len(candidates)
	for _, s := range scopes {
		candidates = append(candidates, narrow(ix.lookup(declKey{Scope: s, Name: name}))...)
	}

	switch {
	case len(candidates) == 0:
		// Declared nowhere this reference can see, own scope or dotted.
		return refResolution{}, false
	case len(candidates) == 1 && ownCount == 1:
		// Byte-identical to today for a file that carries dot scopes but
		// resolves locally.
		return classify(RuleOwnScope, candidates), true
	default:
		// One candidate from a dot scope binds exactly — the ticket's point.
		// More than one, in any combination, is a CLOSED ambiguous group.
		return classify(RuleDotScope, candidates), true
	}
}
