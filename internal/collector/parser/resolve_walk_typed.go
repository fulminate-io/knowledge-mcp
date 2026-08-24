// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// resolveTypedQualifier is rung R2T: the qualifier is a VALUE whose DECLARED
// TYPE the chunker recorded per declaration — a receiver, a parameter, a local
// variable — so the reference is resolved by looking up TYPE.name in the index.
//
// IT IS BIND-ONLY, AND EVERY `return false` IS THAT PROPERTY. Falling through
// means the ladder continues to R2P, R2X and R3 exactly as it does today; it
// does NOT mean the rung emits a suppressed or empty result. This function must
// never return a refResolution carrying an empty candidate set together with
// true — a group this rung cannot answer has to be indistinguishable from one
// resolved before the rung existed.
//
// AN INTERFACE METHOD SPEC IS AN ORDINARY STEP-4 HIT, AND NO INTERFACE DETECTION IS WRITTEN ANYWHERE.
// The Go TopLevel query now captures an interface's method specs as
// declarations of their own, indexed under Parent=<Interface>, so the step-4
// lookup for an interface-typed qualifier BINDS — through the same lookup that
// serves a struct receiver, with no branch that knows what an interface is. The
// second half of that sentence was true before this and is still true now, for a
// different reason: it used to hold because the lookup was empty by
// construction, and it holds today because the hit needs no special handling.
// Go still forbids declaring a method on an interface type, so an interface's
// Parent key holds its method specs and nothing else — the bind is unambiguous.
//
// THE OUTCOME IS THE TWO-HOP CONTRACT MODEL. A call through an interface-typed
// qualifier targets the INTERFACE METHOD DECLARATION — one edge, to the contract
// — and the concrete implementers are reached one hop further over IMPLEMENTS,
// rather than by fanning this call out across every type that happens to declare
// a method of that name. TestInterfaceQualifierBindsToMethodSpec pins all three
// halves: the bind, the absence of fan-out, and the rule it is attributed to.
//
// DECLINE CENSUS FOR THIS WHOLE FILE, established by mutating EVERY decline in
// turn and running the package — not by reading. 18 declines: 6 are GATED, 12
// are UNOBSERVABLE. Recorded here rather than at each site because the 12 share
// ONE structural reason and repeating it 12 times would bury it.
//
//	GATED (a mutation reddens a named subtest):
//	  - the step-4 empty-candidate check in this function — the bind-only
//	    property itself, and the single load-bearing decline of the whole
//	    empty-typeRef family below;
//	  - the in-repo lookup in typedQualifierTarget's DIRECT arm
//	    (orphan_receiver_declines) — a type absent from the corpus must not
//	    bind through its own indexed methods;
//	  - exactly-one OWNER in fieldHopTypeRef (ambiguous_owner_declines);
//	  - callee-resolution failure in typedQualifierTarget (nil deref);
//	  - the ResultIndex bounds check (short_result_list_declines, a panic);
//	  - exactly-one CALLEE in typedQualifierCallee (ambiguous_callee_declines).
//
//	UNOBSERVABLE, and the ONE reason: every other decline either produces or
//	forwards a ZERO typeRef, and a zero typeRef cannot survive the step-4
//	lookup because NO DECLARATION IS EVER INDEXED UNDER AN EMPTY SCOPE. So each
//	is subsumed by the gated step-4 check. They are kept for legibility —
//	"declined here, for this reason" beats an empty lookup several calls later
//	— and they are LABELED rather than defended with tests that would pass in
//	both worlds. Verified individually, and for the FieldTypes pair also in
//	conjunction: removing the missing-key check AND the empty-Scope check
//	together still declines.
//
// A CAUTION THIS CENSUS EARNED THE HARD WAY: "green under every current
// fixture" is NOT unreachable. The direct arm's in-repo lookup read green
// through three separate sweeps and was labeled legibility each time; the
// cumulative review then CONSTRUCTED the discriminating fixture — a method
// indexed under Parent:Server with Server itself absent — and the guard's
// removal BOUND. A decline is only unobservable once someone has tried to build
// the shape that would need it, and a sweep that only mutates cannot do that.
//
// THE CENSUS WAS RE-READ AGAINST THE INTERFACE CHANGE AND ITS CONCLUSIONS STAND,
// on this basis rather than on a re-run. Indexing interface method specs changes
// WHAT the step-4 lookup finds, never WHETHER a decline can be observed: the 12
// unobservable declines are subsumed by the step-4 check for one structural
// reason — a zero typeRef cannot survive it, because no declaration is ever
// indexed under an EMPTY SCOPE — and a method spec is indexed under its
// interface's scope like any other declaration, so it adds hits to that lookup
// without creating an empty-scope key. The 6 gated declines are likewise
// untouched: each is gated by a fixture whose subject is a concrete type, and
// none of those fixtures acquires an interface. The mutation sweep is NOT re-run
// here, per this census's own caution that a sweep which only mutates cannot
// construct the discriminating fixture.
//
// A NOTE ON THE SWEEP ITSELF, because it nearly produced two false negatives:
// neutralizing a guard by prefixing `false &&` is WRONG for any condition
// containing `||`, since `&&` binds tighter and the right-hand disjunct stays
// live. Two conditions here have one, and both first read "unobservable" before
// being re-run with the condition parenthesised — after which the bounds check
// correctly reddened. A mutation harness needs its own control.
func resolveTypedQualifier(
	ix *declIndex, ref *treesitter.RefSite, qualifier, name string, narrow func([]*declRec) []*declRec,
) (refResolution, bool) {
	// FIRST BRANCH, deliberately: a nil map is the case for every language with
	// no registered arm and for every Go declaration that binds no qualifier,
	// which together are the overwhelming majority of qualified references. The
	// common path costs one nil map read and allocates nothing.
	if ref.QualifierTypes == nil {
		return refResolution{}, false
	}
	tr, route, ok := qualifierTypeRef(ix, ref, qualifier)
	if !ok {
		return refResolution{}, false
	}

	// APPLY narrow AS EVERY OTHER RUNG DOES. Dropping it would re-present a
	// suffixed-name narrowing the chunker already settled as an ambiguous group.
	c := narrow(ix.lookup(declKey{Scope: tr.Scope, Parent: tr.Name, Name: name}))
	if len(c) == 0 {
		return refResolution{}, false
	}
	res := classify(RuleTypedQualifier, c)
	res.Route = route
	return res, true
}

// qualRoute names WHICH OF THE RUNG'S THREE ENTRY ROUTES reached an answer.
//
// IT IS RECORDED BECAUSE THE RULE CANNOT CARRY IT. Every reference this rung
// decides is stamped RuleTypedQualifier on Edge.Method, which is the right
// granularity for a reader standing on one edge — but it collapses three
// mechanisms with three different reaches into one label, and the three are not
// interchangeable when the question is how far the rung gets. The direct-type
// route resolves across files through the import binds; the call-return route
// and the field hop resolve same-file only. Aggregate counters split this way
// are the only place that difference is observable, because an edge cannot say
// which route produced it and a total says only that the rung fired.
type qualRoute uint8

const (
	// qualRouteNone is the zero value: no route, for every resolution this rung
	// did not produce.
	qualRouteNone qualRoute = iota
	// qualRouteDirectType — the chunker recorded a TYPE for the qualifier: a
	// receiver, a parameter, an annotated local, a constructor local.
	qualRouteDirectType
	// qualRouteCallReturn — the chunker recorded a CALLEE, whose declared result
	// type at the recorded index is the qualifier's type.
	qualRouteCallReturn
	// qualRouteFieldHop — a dotted qualifier `a.b`, where a's type declares a
	// field b.
	qualRouteFieldHop
)

// String renders the route for the log line's key set.
func (r qualRoute) String() string {
	switch r {
	case qualRouteDirectType:
		return "direct_type"
	case qualRouteCallReturn:
		return "call_return"
	case qualRouteFieldHop:
		return "field_hop"
	case qualRouteNone:
		return "none"
	}
	return "none"
}

// qualifierTypeRef resolves a qualifier to the typeRef whose members the rung
// should search, by either of the rung's TWO ENTRY ROUTES.
//
// The direct route is a qualifier the chunker recorded a type for — a receiver,
// a parameter, a local. The FIELD-HOP route is a dotted qualifier `a.b`, where
// a's type declares a field b: a different route to the same fact, which is why
// both report RuleTypedQualifier rather than splitting one concept across two
// constants.
//
// The direct map is consulted FIRST and costs one read, because a dotted
// qualifier is never a key in it — QualifierTypes is keyed by single
// identifiers — so the common case never reaches the hop.
//
// THE ROUTE IS DECIDED HERE, AT THE ENTRY, AND NOWHERE DEEPER. That placement
// is load-bearing rather than tidy: the field hop resolves its own base
// qualifier by calling typedQualifierTarget internally, so a route stamped
// inside that function would attribute every field hop to whichever arm resolved
// its base and count one reference twice. The entry is the only point at which
// the three routes are mutually exclusive.
func qualifierTypeRef(
	ix *declIndex, ref *treesitter.RefSite, qualifier string,
) (typeRef, qualRoute, bool) {
	if qt, ok := ref.QualifierTypes[qualifier]; ok {
		route := qualRouteDirectType
		if qt.FromCall {
			route = qualRouteCallReturn
		}
		tr, ok := typedQualifierTarget(ix, ref, qt)
		return tr, route, ok
	}
	tr, ok := fieldHopTypeRef(ix, ref, qualifier)
	return tr, qualRouteFieldHop, ok
}

// fieldHopTypeRef resolves `a.b` to the declared type of a's field b.
//
// EXACTLY TWO SEGMENTS, AND THE COUNT IS THE RULE. A deeper chain declines and
// is left at today's behavior — the g_chain stratum, 91 groups on knowledge
// and 85 on agent. This is expressed as a SEGMENT COUNT rather than as a split
// at the first separator, because splitting `a.b.c` at the first separator
// yields two pieces and would wrongly proceed. splitQualifier splits at the
// LAST separator, so `a.b.c` arrives here as base `a.b` — and a base that still
// contains a separator is exactly the deeper chain. The explicit check for that
// is STATED but not load-bearing; see the note on it below for what actually
// rejects a deeper chain today.
//
// THE FIELD'S typeRef WAS ALREADY RESOLVED AT INDEX TIME, through the OWNING
// TYPE's declaring file's binds. That is precisely why Phase 2 Step 3 resolves
// there and not here: a field written `other.T` names a scope only that file's
// imports can resolve, and the referencing file's imports would resolve it to
// the wrong package or to nothing, silently.
func fieldHopTypeRef(ix *declIndex, ref *treesitter.RefSite, qualifier string) (typeRef, bool) {
	base, field := splitQualifier(ref.Lang, qualifier)
	if base == "" || field == "" {
		return typeRef{}, false
	}
	// EXACTLY TWO SEGMENTS — the rule stated explicitly, and UNOBSERVABLE when
	// deleted. Both map lookups below already reject a deeper chain on their
	// own, under EITHER derivation of base and field, and that was verified by
	// mutation rather than reasoned about:
	//   - split at the LAST separator (what this does): `a.b.c` gives base
	//     `a.b`, and QualifierTypes is keyed by SINGLE IDENTIFIERS, so a dotted
	//     base is never a key;
	//   - split at the FIRST separator (the plausible future "fix"): `a.b.c`
	//     gives field `b.c`, and FieldTypes is keyed by SINGLE FIELD NAMES, so
	//     a dotted field is never a key either.
	// Deleting this check leaves the whole parser package green under BOTH. It
	// is kept because the plan's rule IS a segment count and a reader needs to
	// see it stated — not because anything currently depends on it. One of the
	// 12 unobservable declines in the census on resolveTypedQualifier above.
	if outer, _ := splitQualifier(ref.Lang, base); outer != "" {
		return typeRef{}, false
	}

	// Segment 0 must be a recorded qualifier type. Phase 2 Step 2's conflict
	// rule already REMOVES conflicted names from the map, so presence here IS
	// the not-conflicted check.
	qt, ok := ref.QualifierTypes[base]
	if !ok {
		return typeRef{}, false
	}
	tr, ok := typedQualifierTarget(ix, ref, qt)
	if !ok {
		return typeRef{}, false
	}

	// EXACTLY ONE owning declaration, on the same rule the call arm applies to
	// a callee: with two candidates, whose field table to read is genuinely
	// unknown, and reading the first is a wrong-target generator.
	owners := ix.lookup(declKey{Scope: tr.Scope, Name: tr.Name})
	if len(owners) != 1 {
		return typeRef{}, false
	}

	ft, ok := owners[0].FieldTypes[field]
	if !ok {
		return typeRef{}, false
	}
	// An empty Scope declines, which is ALSO how a field of a container type
	// (`[]*pkg.T`, `map[string]V`) declines: the chunker's descent refuses those
	// tops, so such a field never reaches the table in the first place.
	//
	// STRUCTURALLY UNREACHABLE, not merely unobservable: resolveTypeTextMap
	// (declindex.go:119) admits an entry ONLY when tr.Scope != "", so no
	// FieldTypes value can carry an empty Scope in the first place. The
	// preceding missing-key check is unobservable for the paired reason — each
	// subsumes the other for the missing case, and removing BOTH still declines
	// at the step-4 lookup. See the census on resolveTypedQualifier.
	if ft.Scope == "" {
		return typeRef{}, false
	}
	return ft, true
}

// typedQualifierTarget resolves one recorded qualifier type to the typeRef whose
// members the rung should search, or declines.
//
// The two arms differ in ONE respect: whether the recorded text names the type
// directly, or names a callee whose DECLARED RESULT type is the type. Both end
// at a typeRef that must be resolvable to an in-repo scope.
func typedQualifierTarget(
	ix *declIndex, ref *treesitter.RefSite, qt treesitter.QualType,
) (typeRef, bool) {
	if !qt.FromCall {
		// DIRECT TYPE. resolveTypeTextThroughIndex wraps the single shared
		// text-to-typeRef resolver with the import-bind rung; it is called HERE
		// with the REFERENCING file's site, because a qualifier's type as
		// written in this file is bound by this file's imports. The bind rung
		// fires only where the index-blind answer names nothing the index
		// declares, so a qualifier typed to a same-file declaration resolves
		// exactly as it did before.
		tr := resolveTypeTextThroughIndex(ix, ref, qt.Text)
		// Subsumed by the in-repo lookup below, which declines on the zero
		// typeRef regardless; kept for the same legibility reason as the call
		// arm's.
		if tr.Scope == "" {
			return typeRef{}, false
		}
		// THE TYPE MUST ACTUALLY BE DECLARED IN THE INDEXED CORPUS, and this is
		// LOAD-BEARING rather than legibility — it declines a qualifier whose
		// TYPE is not indexed even when that type's METHODS are.
		//
		// It is NOT subsumed by step 4. The shape that separates them is
		// ordinary: a method indexed under Parent:Server while Server itself is
		// absent, which is what a build-tag split, a partially-indexed repo or
		// a discovery exclusion produces. Step 4 looks up {Scope, Parent:Server,
		// Name:method} and FINDS the orphan method, so without this check the
		// rung confidently answers a reference whose qualifier type the corpus
		// never declared. Gated by orphan_receiver_declines, which reddens when
		// this check is removed.
		if len(ix.lookup(declKey{Scope: tr.Scope, Name: tr.Name})) == 0 {
			return typeRef{}, false
		}
		return tr, true
	}

	// CALL RETURN. The recorded text is a CALLEE, so the callee is resolved to
	// exactly one declaration and its declared result type at ResultIndex is
	// the qualifier's type.
	callee, ok := typedQualifierCallee(ix, ref, qt.Text)
	if !ok {
		return typeRef{}, false
	}
	// BOUNDS CHECK, and it is load-bearing rather than defensive: Results holds
	// a slot for every declared result INCLUDING the ones whose type declined,
	// so an index is only meaningful inside the recorded arity.
	if qt.ResultIndex < 0 || qt.ResultIndex >= len(callee.ResultTypes) {
		return typeRef{}, false
	}
	tr := callee.ResultTypes[qt.ResultIndex]
	// A DECLINING SLOT IS THE FULL ZERO typeRef, so this early return is a
	// LEGIBILITY guard rather than a behavior one: resolveTypeText returns
	// typeRef{} — both fields empty — on every decline, and no declaration is
	// ever indexed under an empty scope, so the step-4 lookup below would
	// return nothing anyway. Deleting it is UNOBSERVABLE, which is why no test
	// can discriminate it; it stays because "declined here, for this reason" is
	// worth more at the point of decision than an empty lookup two calls later.
	if tr.Scope == "" {
		return typeRef{}, false
	}
	return tr, true
}

// typedQualifierCallee resolves a callee's written text to EXACTLY ONE
// declaration, or declines.
//
// EXACTLY ONE IS THE RULE, not "the first of several". Taking the head of an
// ambiguous set is a wrong-target generator that no fixture would surface,
// because the wrong answer is a real declaration with a plausible name — and
// this rung's entire acceptance gate is zero wrong targets.
func typedQualifierCallee(ix *declIndex, ref *treesitter.RefSite, text string) (*declRec, bool) {
	q, rawName := splitQualifier(ref.Lang, text)
	calleeName := baseDeclName(rawName)
	if calleeName == "" {
		return nil, false
	}

	key := declKey{Scope: ref.Scope, Name: calleeName}
	if q != "" {
		// A QUALIFIED CALLEE RESOLVES ONLY THROUGH A BIND. `pkg.New()` names
		// another scope, which only this file's imports can identify.
		bind, bound := ref.Binds[q]
		if !bound {
			// THIS CLAUSE IS WHAT DECLINES `y.Bar()` — the chained
			// receiver-method hop, where y is itself a local rather than an
			// imported package. The simulation that produced this ticket's
			// measured coverage never executed that branch (both of its
			// buildEnv call sites pass a nil env), so the scored figures were
			// produced WITHOUT it and implementing it here would resolve
			// references the measurement never credited.
			return nil, false
		}
		key = declKey{Scope: bind.Scope, Name: calleeName}
	}

	candidates := ix.lookup(key)
	if len(candidates) != 1 {
		return nil, false
	}
	return candidates[0], true
}
