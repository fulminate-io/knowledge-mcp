// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"strconv"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// RefStatus is what resolution concluded about one reference.
type RefStatus string

const (
	// RefBound — exactly one declaration satisfies the rule that fired.
	RefBound RefStatus = "bound"
	// RefAmbiguous — several declarations satisfy it, and the language
	// genuinely permits all of them. A CLOSED "exactly one of these".
	RefAmbiguous RefStatus = "ambiguous"
	// RefExternal — nothing in the index satisfies any rule.
	RefExternal RefStatus = "external"
	// RefDynamic — the language dispatches this at runtime. An OPEN
	// "one of these, or beyond".
	RefDynamic RefStatus = "dynamic"
)

// RefRule names the rung of the ladder that produced a resolution. It is
// recorded rather than inferred so a surprising binding can be attributed.
type RefRule string

const (
	RuleQualifiedImport RefRule = "qualified-import"
	RuleQualifiedParent RefRule = "qualified-parent"
	// RuleExternalQualifier sits between the qualified-parent and dynamic
	// rungs. It is numbered R2X rather than renumbering the ladder because
	// three dependent plans cite the later rungs by number.
	RuleExternalQualifier RefRule = "external-qualifier"
	RuleDynamicScope      RefRule = "dynamic-scope"
	RuleUnqualifiedImport RefRule = "unqualified-import"
	RuleSiblingMember     RefRule = "sibling-member"
	RuleOwnScope          RefRule = "own-scope"
	// RuleDotScope attributes an unqualified reference that a DOT SCOPE
	// supplied — a scope folded into the file's namespace wholesale rather
	// than a name bound one at a time. It is an ATTRIBUTION and never a
	// precedence: a single candidate from a dot scope binds under this rule
	// instead of RuleOwnScope purely to record where the one answer came from.
	RuleDotScope RefRule = "dot-scope"
	// RuleQualifiedMember attributes a reference that reached a declaration
	// through an import bind's CONTAINER — an imported class's member, or the
	// type a static-member import names. It is an ATTRIBUTION and never a
	// precedence: a single candidate from the parent-keyed lookup binds under
	// this rule instead of RuleQualifiedImport purely to record which of the two
	// lookups produced the one answer. When both lookups hit, the union is a
	// CLOSED ambiguous group and this rule records that the top-level rival could
	// not be excluded — it never picks a winner.
	RuleQualifiedMember RefRule = "qualified-member"
	// RuleQualifiedPath attributes a FULLY-QUALIFIED reference that resolved
	// through its qualifier's own scope rather than through a bind — a
	// `com.acme.foo.Bar` written out in full, which carries no import statement
	// for any arm to have bound.
	RuleQualifiedPath RefRule = "qualified-path"
	RuleNotDeclared   RefRule = "not-declared"
)

// refResolution is one reference's outcome.
type refResolution struct {
	Status     RefStatus
	Rule       RefRule
	Candidates []*declRec
}

// The two Edge.Method values a multi-bind group is tagged with are NOT declared
// here. kgtypes.EdgeMethodAmbiguousName and kgtypes.EdgeMethodDynamic are the
// single authority, because Method is a persisted wire field a reader must be
// able to interpret without importing this producer.

// COLLAPSING A GROUP — DOCUMENTED, NOT BUILT. Nothing in this package collapses
// a group; this describes what a future consumer would have to do, and the one
// obstacle it will hit.
//
//  1. An AMBIGUOUS group is collapsed ATOMICALLY once something learns which
//     member is the referent: delete the other N-1 edges, set the survivor's
//     Confidence to 1 and clear its Evidence. Partway through, the graph states
//     a total probability other than 1 for a reference that has exactly one
//     referent, so the three writes belong in one transaction.
//  2. The group key is deterministic and stable across collects of an unchanged
//     file, so a collapse decision made in one run still addresses the same
//     group in the next.
//  3. THE HONEST OBSTACLE: the store keys per-edge metadata by
//     edgeMetaKey{From, Type, To} (cmd/knowledge-server/internal/store/graph_graph.go:25)
//     and holds NO index from Evidence to edges. Collapsing by key today is
//     therefore a full edge scan; making it cheap needs a server-side secondary
//     index, which is out of scope here.
//  4. COLLAPSE MEANS SOMETHING DIFFERENT FOR A DYNAMIC GROUP. Promoting one
//     member to Confidence 1 asserts a closure the open-set semantics deny —
//     the referent may be something no static enumeration reached. What a
//     consumer should do with an open group instead is the rendering ticket's
//     design question, not a mechanism to copy from the ambiguous case.

// resolveRef returns the FIRST rule that yields a non-empty candidate set.
//
// Every rung is a FILTER OVER THE INDEX BY THE LANGUAGE'S OWN SCOPING — none is
// a name heuristic, and every candidate set, dynamic included, is bounded by a
// language scope unit. There is deliberately no name-wide rule: probing one
// produced 245,888 edges on a graph whose total is 147,410, with fanout to 68.
//
// THE ONE RUNG THAT GATHERS ACROSS SCOPE UNITS is the last unqualified one, and
// it stays inside that discipline: a dot import makes another scope part of
// THIS file's namespace by the language's own rule, so the union it searches is
// still a language scope and not a name search. See resolveUnqualified.
//
// R4's position relative to R5/R6 is NOT universal, and it is now PER-LANGUAGE
// rather than fixed: resolveUnqualified orders the rungs by
// profileFor(lang).ImportsBeatLocals. Import-first is correct for languages
// where an import and a local declaration of one name cannot coexist (ES
// modules, Rust, Java, PHP, Go — a compile error, so the order is
// unobservable), and it is WRONG for python, csharp, elixir, ruby, scala and
// kotlin, where a local legally shadows an import and import-first would bind
// the import where the language binds the local. Those six carry
// ImportsBeatLocals false; every other language defaults to true. Do not
// collapse the two orders into one: in an ES module a bare foo() inside a class
// method refers to the module-scope import, not to a sibling method, so a
// single locals-first order would only trade one wrong-edge class for another.
func resolveRef(ix *declIndex, ref *treesitter.RefSite, target string) refResolution {
	if ref == nil || target == "" {
		return refResolution{Status: RefExternal, Rule: RuleNotDeclared}
	}

	// Split at the LAST occurrence of any separator the language writes: the
	// qualifier is everything before it. THE SECOND RETURN BINDS TO rawName AND
	// NOT TO name — resolveRef narrows through a suffix filter that keys on the
	// RAW name while the index key uses the base name, so collapsing the two
	// would throw the chunker's alias narrowing away.
	qualifier, rawName := splitQualifier(ref.Lang, target)
	// Keys are BASE names, because a reference normally writes Thing and never
	// Thing#a1b2c3d4. A target that DOES carry the suffix has already been
	// narrowed to one exact declaration upstream — the chunker's type-reference
	// alias rule repoints a reference to a collided container onto the single
	// declaration that can be the type. Base-naming it here would throw that
	// narrowing away and hand the reference the whole collided set back as an
	// ambiguous group, so the suffix is kept as a post-lookup filter.
	name := baseDeclName(rawName)
	if name == "" {
		return refResolution{Status: RefExternal, Rule: RuleNotDeclared}
	}
	narrow := func(c []*declRec) []*declRec { return filterBySuffixedName(c, rawName) }

	if qualifier != "" {
		return resolveQualified(ix, ref, qualifier, name, narrow)
	}
	return resolveUnqualified(ix, ref, name, narrow)
}

// THE UNQUALIFIED ARM — resolveUnqualified, resolveImportBound and
// resolveLocalScopes — lives in resolve_walk_unqualified.go, moved verbatim
// when this file reached the 500-line block. resolveRef already dispatched on
// exactly that boundary, so the split follows the ladder rather than cutting
// across it.

// resolveQualified walks rungs R1 through R3, the ones that apply when a
// reference carries a qualifier. It is a arm of resolveRef rather than a
// separate policy: the ladder's order is unchanged and every rung still filters
// the index by the language's own scoping.
//
// R2P sits between R2 and R2X and is lettered rather than numbered for the same
// reason R2X is: three dependent plans cite the later rungs by number, so a
// renumbering would invalidate their text.
func resolveQualified(
	ix *declIndex, ref *treesitter.RefSite, qualifier, name string, narrow func([]*declRec) []*declRec,
) refResolution {
	// R1 — the qualifier is a name an import bound to another scope.
	bind, bound := ref.Binds[qualifier]
	if bound {
		if res, ok := resolveQualifiedImport(ix, bind, qualifier, name, narrow); ok {
			return res
		}
	}
	// R2 — the qualifier is a declared PARENT in the reference's own scope: a
	// Go receiver type, a class whose member is accessed.
	if c := narrow(ix.lookup(declKey{Scope: ref.Scope, Parent: baseDeclName(qualifier), Name: name})); len(c) > 0 {
		return classify(RuleQualifiedParent, c)
	}
	// R2P — the qualifier is a PACKAGE PATH rather than a name anything bound.
	// A fully-qualified `com.acme.foo.Bar` needs no import statement at all, so
	// no arm ever records a bind for `com.acme.foo` and every rung above misses;
	// without this rung the reference falls to R3 and emits an open-set dynamic
	// edge to any LOCAL Bar.
	//
	// IT IS REACHED ONLY WHEN THE QUALIFIER IS UNBOUND, and the guard is here
	// rather than inside the rung because it is a statement about the LADDER's
	// order: a qualifier that IS bound has already been answered by R1, and
	// re-deriving a scope for it would second-guess the arm that resolved it.
	// What the rung then does with the derivation is resolveQualifiedPath's.
	if !bound {
		if res, ok := resolveQualifiedPath(ix, ref, qualifier, name, narrow); ok {
			return res
		}
	}
	// R2X — the qualifier is bound to a target that contributes NOTHING to the
	// index, so the reference terminates here instead of falling into R3.
	//
	// This is the one rung that removes a WRONG-EDGE class rather than an index
	// gap: without it, `slog.Info` inside a package that also declares Info
	// falls to R3 and emits an open-set dynamic edge to the LOCAL Info. An
	// open set asserts "dispatches to one of these, or beyond", but for an
	// unindexed target every listed candidate is known NOT to be the referent,
	// so the enumeration would be entirely false rather than merely incomplete.
	// Emitting nothing is the only truthful option.
	//
	// AFTER R2 is right in both language classes. Where an import and a local
	// container cannot share a name, R2 cannot match and only this rung stands
	// between the reference and a wrong R3 edge; where a local may shadow an
	// import, R2 catches the local first and it correctly wins.
	// THE INDEX IS THE AUTHORITY, not a flag on the bind. add() is already the
	// single write path for every view, so the scope set is not a second
	// derivation of "what is indexed" — it IS the index, summarized. There is
	// no predicate to keep in sync because there is no second predicate.
	if bound && !ix.hasScope(bind.Scope) {
		return refResolution{Status: RefExternal, Rule: RuleExternalQualifier}
	}
	// R3 — the qualifier is a VALUE, so the language dispatches at runtime.
	// Candidates are every declaration of the name in this scope regardless of
	// parent.
	//
	// THE LADDER STOPS HERE for a qualified reference. It never discards the
	// qualifier to retry the bare name — that last-dotted-segment retry is the
	// heuristic this work exists to delete, and it is what let a reference
	// escape its own scope entirely.
	c := narrow(ix.lookupScopeName(scopeNameKey{Scope: ref.Scope, Name: name}))
	return refResolution{Status: RefDynamic, Rule: RuleDynamicScope, Candidates: c}
}

// resolveQualifiedPath is R2P's body — the qualifier is a PACKAGE PATH rather
// than a name anything bound. It reports false when the language has no
// package-qualified reference form, and when the derived scope IS indexed but
// holds no declaration of the name, so the caller falls through to the later
// rungs unchanged in both cases.
//
// BOTH OUTCOMES COME FROM ONE DERIVATION, which is the point: the same
// qualifier-to-scope mapping either finds the scope in the index and binds
// exactly, or names a scope the index has never heard of and TERMINATES. The
// termination REUSES RuleExternalQualifier rather than minting a second constant
// — the fact recorded is identical, that the qualifier's target contributes
// nothing to the index, and a second name would split one concept in two.
func resolveQualifiedPath(
	ix *declIndex, ref *treesitter.RefSite, qualifier, name string, narrow func([]*declRec) []*declRec,
) (refResolution, bool) {
	scope, ok := qualifierScope(ref.Lang, qualifier)
	if !ok {
		return refResolution{}, false
	}
	if c := narrow(ix.lookup(declKey{Scope: scope, Name: name})); len(c) > 0 {
		return classify(RuleQualifiedPath, c), true
	}
	if !ix.hasScope(scope) {
		return refResolution{Status: RefExternal, Rule: RuleExternalQualifier}, true
	}
	return refResolution{}, false
}

// resolveQualifiedImport is R1's body — the qualifier is a name an import bound
// to another scope. It runs TWO lookups over that scope and reports false when
// both miss, so the caller falls through to the later rungs unchanged.
//
// THE TOP-LEVEL LOOKUP IS THE LANDED ONE, byte-identical, and it still answers
// alone whenever the member lookup misses. The PARENT-KEYED one is what lets an
// imported CLASS's member be reached at all: an arm binds the container's name
// while a member is indexed with its container as Parent, so without this key
// every rung reachable from an import could only ever see a top-level
// declaration.
//
// THE PARENT KEY IS THE BIND'S DECLARED NAME WHEN IT HAS ONE, ELSE THE
// QUALIFIER, and the fallback is EXACT rather than approximate: declaredName
// (treesitter/chunker_binds.go) records Bind.Name only when the imported name
// DIFFERS from the local one, so an empty Name means the qualifier IS the
// target's declared name.
//
// THIS IS NOT THE OVERRIDE THE UNQUALIFIED RUNG'S COMMENT FORBIDS HERE, and the
// difference is the KEY it is applied to. Bind.Name must not reach the NAME key
// of a qualified reference — `import * as ns` renames the MODULE and not its
// members, so a namespace member keeps the spelling the reference used, and the
// name below is still the reference's own. It is applied to the PARENT key,
// where the bind's Name is the CONTAINER's declared name and is exactly what a
// member's Parent is indexed under. Different key, different question, both
// correct: deleting either lookup because the two looked contradictory is the
// regression this note exists to prevent. `import * as ns` is unaffected either
// way — its Name is empty, so the parent key is "ns", nothing in the target is
// parented to a container of that name, and the top-level lookup answers.
//
// A DOUBLE HIT IS A CLOSED GROUP, NEVER A PICKED WINNER. A module may export
// both a top-level `method` and a class `Foo` with a member `method`, so
// `Foo.method()` genuinely reaches two candidates and the ladder has no type
// information to tell which is the referent. classify turns the union into an
// ambiguous group at Confidence 1/N — a TRUE claim that exactly one of these is
// it — and the union is ordered top-level first purely so a group's member edges
// are byte-identical across collects. The attribution is by WHICH LOOKUP hit,
// not by cardinality: adding a precedence to pick a winner would state a fact
// the reference does not carry.
//
// No explicit external-skip is needed: a bind into an unindexed scope misses
// both lookups by construction, and R2X terminates it.
func resolveQualifiedImport(
	ix *declIndex, bind treesitter.Bind, qualifier, name string, narrow func([]*declRec) []*declRec,
) (refResolution, bool) {
	parent := qualifier
	if bind.Name != "" {
		parent = bind.Name
	}
	top := narrow(ix.lookup(declKey{Scope: bind.Scope, Name: name}))
	member := narrow(ix.lookup(declKey{Scope: bind.Scope, Parent: parent, Name: name}))

	switch {
	case len(member) == 0 && len(top) == 0:
		return refResolution{}, false
	case len(member) == 0:
		return classify(RuleQualifiedImport, top), true
	case len(top) == 0:
		return classify(RuleQualifiedMember, member), true
	default:
		union := make([]*declRec, 0, len(top)+len(member))
		union = append(union, top...)
		union = append(union, member...)
		return classify(RuleQualifiedMember, union), true
	}
}

// filterBySuffixedName narrows a candidate set to the ONE declaration whose
// identity carries the exact suffixed name the reference asked for, and is a
// no-op for the ordinary unsuffixed reference.
//
// Suffixed names reach resolution only from the chunker's type-reference alias
// rule, which has ALREADY decided which of several colliding declarations a
// type reference can mean. Honoring that decision here is what keeps the
// narrowing alive: the keyed views are base-named, so an unfiltered lookup
// would return every collided rival and re-present a settled question as an
// ambiguous group.
func filterBySuffixedName(candidates []*declRec, rawName string) []*declRec {
	if len(candidates) == 0 || rawName == baseDeclName(rawName) {
		return candidates
	}
	out := make([]*declRec, 0, 1)
	for _, c := range candidates {
		if strings.HasSuffix(c.NodeID, ":"+rawName) || strings.HasSuffix(c.NodeID, "."+rawName) {
			out = append(out, c)
		}
	}
	return out
}

// classify turns a non-empty candidate set into a status by CARDINALITY: one
// candidate binds, more than one is genuinely ambiguous. RefDynamic is never
// produced here — it is a property of the rule that fired, not of the count.
func classify(rule RefRule, candidates []*declRec) refResolution {
	if len(candidates) == 1 {
		return refResolution{Status: RefBound, Rule: rule, Candidates: candidates}
	}
	return refResolution{Status: RefAmbiguous, Rule: rule, Candidates: candidates}
}

// groupKey identifies the ONE reference site that produced a multi-candidate
// edge group. Every member edge of a group carries this same string in the
// existing Evidence field — no shape change anywhere in the pipeline.
//
// The EDGE TYPE is a component because one declaration routinely emits two
// different-typed edges to the same verbatim target: `type S struct { Foo }`
// emits USES_TYPE and EMBEDS to "Foo".
//
// The TARGET is VERBATIM, never its last dotted segment — extractCallEdges
// dedupes callees by captured text within one declaration, so (declaration
// offset, edge type, captured text) is unique per emitted edge.
//
// The BYTE is the emitting DECLARATION's StartByte, not the reference token's:
// extractCallEdges aggregates every call site of one callee into a single
// weighted edge, so no per-token offset exists. The declaration's offset is the
// finest that does, and it is byte-stable across collects of an unchanged file.
func groupKey(file string, refByte int, edgeType string, target string) string {
	return file + ":" + strconv.Itoa(refByte) + ":" + edgeType + ":" + target
}
