// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// declKey identifies a declaration by its exact declared position in a scope.
// Parent and Name are BASE names — the "#<astPathHash>" suffix a colliding
// declaration takes belongs to its IDENTITY, never to this key, because a
// reference writes Thing and never Thing#a1b2c3d4.
type declKey struct {
	Scope  string
	Parent string
	Name   string
}

// scopeNameKey is the any-parent view of a scope: every declaration of a name
// within one scope unit regardless of which parent declares it. This is the
// candidate set of a runtime dispatch, which cannot know the parent statically.
type scopeNameKey struct {
	Scope string
	Name  string
}

// declRec is one declaration, as resolution sees it. Every field has a named
// reader — a carrier arrives with its consumer or not at all.
type declRec struct {
	// NodeID is the emitted edge's target, and the resolved source of a Go
	// receiver containment edge. It is the SUFFIXED identity.
	NodeID string
	// File backs file-order assertions on a lookup's candidate slice.
	File string
	// Scope is the resolution unit this declaration lives in.
	Scope string
	// Ref is the DECLARING file's reference site — the only site that can bind
	// a spelling this declaration wrote, since a qualifier means whatever this
	// file's own imports say it means.
	//
	// IT COSTS ONE POINTER WORD AND NO ALLOCATION: the site is built once per
	// file and shared, so this field takes a pointer that already exists. It is
	// carried on the record because the declared-supertype lookup runs in the
	// emitter, where the index is complete, and the emitter is handed the index
	// alone — so the inputs that lookup needs have to travel with the record.
	Ref *treesitter.RefSite
	// Lang is the language of the file that declared this record. Its ONE
	// reader is the satisfaction derivation's index views, which skip every
	// record that is not Go.
	//
	// IT IS STAMPED FROM THE SAME AUTHORITY THE SCOPE LINE ALREADY READS —
	// treesitter.Result.Language, two statements away, which ScopeID consults to
	// pick the resolution unit — so there is one authority and no second
	// predicate to keep in sync. That INCLUDES the cpp header fallback's
	// reassignment: a .h file adopted under the cpp grammar carries cpp here
	// exactly as it does everywhere else.
	Lang treesitter.Language
	// Parent is the BASE name of the declaring container, or "".
	Parent string
	// Owner is the NODE ID of the declaration that LEXICALLY ENCLOSES this
	// one, or "" when the chunker could not address that container.
	//
	// IT EXISTS BECAUSE Parent IS A BASE NAME AND BASE NAMES COLLIDE. Two
	// declarations in one scope may share a name — a scala companion object
	// beside its trait, a C# partial class, a re-opened PHP namespace — and
	// their members are then indexed under the SAME {Scope, Parent, Name} key.
	// Parent cannot tell them apart, by design: a member's ParentName is kept
	// as the source wrote it, and only the disambiguated container carries the
	// path-hash suffix. Owner is that disambiguated identity.
	//
	// IT IS REUSED, NOT INVENTED. The chunker already addresses a member's
	// container POSITIONALLY, and resolveSlotEdges rewrites that slot into the
	// container's final node ID before this index is built — so the resolved
	// parent-to-member containment edge already states this fact and
	// stampDeclOwners simply reads it off.
	//
	// EMPTY IS A REAL ANSWER AND NOT AN ERROR. A Go method's container is its
	// receiver TYPE, a sibling declaration that may not even live in the same
	// file, so no slot can address it and this stays empty — which is correct,
	// because Go has no construct that puts two same-named containers in one
	// scope. A reader must treat empty as "not addressable" rather than as
	// "top level".
	Owner string
	// Name is the declaration's BASE name.
	Name string
	// ResultTypes are this declaration's declared result types, IN ORDER,
	// already resolved against the DECLARING file's imports. Position is
	// load-bearing: a multi-value binding's ResultIndex indexes into it.
	ResultTypes []typeRef
	// FieldTypes maps this declaration's struct field names to their resolved
	// types, for the one-struct-field qualifier hop.
	FieldTypes map[string]typeRef
	// SigKey is this declaration's RESOLVED signature key, empty when the
	// declaration carries no signature. Read by the interface-satisfaction
	// derivation, which requires a concrete method and an interface method spec
	// to agree on it exactly. Composition is rendered by the chunker; the leaves
	// are resolved here, against the DECLARING file's imports, so the same
	// spelling in two packages yields two different keys.
	SigKey string
	// Conforms are the supertypes this declaration DECLARED, carried UNRESOLVED
	// as the spelling plus the clause kind. Read by the declared-conformance
	// emitter, which resolves each entry against a COMPLETE index.
	//
	// IT IS NOT RESOLVED HERE, AND THERE IS NO COMPANION FLAG RECORDING THAT IT
	// COULD NOT BE. Whether a spelling names something in-repo is only decidable
	// once every file has contributed, so the emitter's own counter carries that
	// outcome — a flag set at index-build time would be a second authority
	// stating a fact only the emitter can know.
	Conforms []conformRef
	// SlotBinds are the composite-literal slots this declaration filled, carried
	// VERBATIM from the chunker with both spellings UNRESOLVED.
	//
	// RESOLUTION IS DEFERRED TO THE EMITTER, AND THAT IS THE DESIGN. The
	// index-aware type-text resolver takes a reference site and a SPELLING, so
	// storing a resolved target here would mean resolving at index-build time —
	// against a half-built index, where the answer depends on file order — and
	// would also destroy the two inputs the emitter needs to do it properly.
	// The same correction Conforms above documents, applied to a second carrier.
	//
	// IT HOLDS THE CHUNKER'S OWN TYPE rather than a parser-side copy of it. A
	// second struct with identical fields would be pure drift with no consumer,
	// and declRec already carries a treesitter type in Lang.
	SlotBinds []treesitter.SlotBind
	// FieldOrder is a STRUCT declaration's field names in SOURCE ORDER, which is
	// what a POSITIONAL slot bind's Index indexes into. Carried verbatim.
	FieldOrder []string
	// Embeds are this declaration's embedded types, RESOLVED, with the entries
	// that named no in-repo scope DROPPED. Read by the derivation's transitive
	// method-set promotion: a type embedding an interface acquires that
	// interface's method set.
	Embeds []typeRef
	// ExtEmbed is true when at least one embed spelling was DROPPED because it
	// resolved to no in-repo scope — so this declaration's promoted method set is
	// a LOWER BOUND rather than the whole of it.
	//
	// IT GATES NOTHING, AND THAT IS THE DISPOSITION, not an oversight. A
	// declaration carrying an unresolvable embed is still matched and still
	// derives satisfaction pairs; because its method set is under-known, those
	// pairs are a known false-positive source, and the count is reported rather
	// than suppressed. The derivation's `extembed_still_derives` subtest is what
	// holds this field and its consumer to the same story — if a future change
	// makes ExtEmbed suppress a derivation, that subtest fails and this comment
	// is what tells the reader the suppression was never the contract.
	ExtEmbed bool
	// IsInterface marks a type declaration whose spec declares an interface.
	// Read by the derivation to tell a contract from a concrete type without
	// re-parsing anything.
	IsInterface bool
	// PartialBody marks a container that is ONE BODY of a type which may have
	// several. Read by the declared-conformance member pairing, which lets two
	// bodies of one type own each other's members while keeping every other
	// same-named pair confined.
	PartialBody bool
	// IsGeneric marks a type declaration carrying type PARAMETERS. Read by the
	// satisfaction derivation, which reports such an interface as UNDECIDED
	// rather than as having no implementers — a type parameter does not unify
	// under a syntax-level signature comparison, so the honest answer is that the
	// question was not settled.
	//
	// IT CANNOT BE INFERRED FROM A RESOLVED SIGNATURE, which is why it is carried
	// from the parse tree. A type parameter resolves into its own declaring scope
	// exactly as a same-package type does, and "names something the index does
	// not declare" catches type ALIASES too — real types the declaration query
	// never captures.
	IsGeneric bool
}

// typeRef is a type named by a declaration, resolved to the scope that would
// hold it. An EMPTY Scope means the text resolved to no in-repo scope, and it
// is the DECLINE signal every consumer reads — never a scope to guess at.
type typeRef struct {
	Scope string
	Name  string
}

// resolveTypeText resolves a type written AS TEXT into a typeRef, against one
// reference site's imports.
//
// WHICH SITE IS PASSED IS THE WHOLE CONTRACT, and it differs by caller BY
// DESIGN. Indexing passes the DECLARING file's site, because a result or field
// type written `other.T` names a scope only the declaring file's own imports
// can resolve; resolution passes the REFERENCING file's site, because a
// qualifier's type as written in that file is bound by that file's imports.
// Passing the wrong one resolves to another package or to nothing, silently.
//
// Three outcomes:
//   - no qualifier      -> the site's own scope, base-named
//   - qualifier bound   -> the scope that bind establishes, last segment named
//   - qualifier unbound -> the zero typeRef, which declines
//
// It allocates nothing: both outputs are substrings or map reads.
func resolveTypeText(ref *treesitter.RefSite, text string) typeRef {
	if ref == nil || text == "" {
		return typeRef{}
	}
	qualifier, rawName := splitQualifier(ref.Lang, text)
	name := baseDeclName(rawName)
	if name == "" {
		return typeRef{}
	}
	if qualifier == "" {
		return typeRef{Scope: ref.Scope, Name: name}
	}
	bind, ok := ref.Binds[baseDeclName(qualifier)]
	if !ok {
		return typeRef{}
	}
	return typeRef{Scope: bind.Scope, Name: name}
}

// resolveTypeTexts resolves a declaration's result types, PRESERVING POSITION.
// An entry that declines becomes the zero typeRef rather than being dropped,
// because ResultIndex indexes into this slice — dropping one would silently
// rebind every later multi-value assignment to the wrong type.
func resolveTypeTexts(ref *treesitter.RefSite, texts []string) []typeRef {
	if len(texts) == 0 {
		return nil
	}
	out := make([]typeRef, len(texts))
	for i, text := range texts {
		out[i] = resolveTypeText(ref, text)
	}
	return out
}

// resolveTypeTextMap resolves a declaration's struct field types. A field whose
// type declines is omitted: this map is keyed by name rather than by position,
// so an absent entry and a zero entry mean the same thing to every reader.
func resolveTypeTextMap(ref *treesitter.RefSite, texts map[string]string) map[string]typeRef {
	if len(texts) == 0 {
		return nil
	}
	out := make(map[string]typeRef, len(texts))
	for name, text := range texts {
		if tr := resolveTypeText(ref, text); tr.Scope != "" {
			out[name] = tr
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// declIndex is the set-valued, identity-keyed replacement for the scalar
// symbol map whose bare last-write-wins assignment destroyed one declaration
// per collision.
//
// THREE VIEWS, and deliberately no by-file view: nothing reads one. byID serves
// identity; byKey serves the qualified, sibling and own-scope rules plus the Go
// receiver containment source; byScopeName serves the dynamic rule. A field
// with no consumer is the same dead carrier this work removes elsewhere.
//
// The two keyed views answer DIFFERENT questions and are populated in one pass
// so they cannot disagree. byKey is parent-qualified by construction and so
// cannot answer "what could a dispatch on this name reach here regardless of
// parent"; iterating it per lookup to find out would be O(keys) per reference.
type declIndex struct {
	byID        map[string]*declRec
	byKey       map[declKey][]*declRec
	byScopeName map[scopeNameKey][]*declRec

	// scopes is the set of scope IDs that contribute at least one declaration.
	// The external-qualifier rule reads it to tell "this bind's target is
	// indexed" from "this bind's target contributes nothing", which is what
	// lets a reference through an unindexed target terminate instead of
	// manufacturing an edge to a same-named local.
	//
	// Written in add(), which is already the single write path, so the set
	// cannot drift from the index it summarizes.
	scopes map[string]bool

	// pendingSigs holds the signature facts whose resolution must WAIT for the
	// scope set above to be complete, drained by resolveSigKeys.
	//
	// THE DEFERRAL IS FORCED, NOT A PREFERENCE. A signature leaf renders
	// `ext:<name>` when it names no in-repo scope, and the only honest test for
	// that is hasScope — the same test the external-qualifier rung uses, and the
	// question the Go binds arm deliberately refuses to answer for itself. But
	// scopes ACCUMULATES as declarations are added, so asking mid-build is
	// order-dependent: a file indexed before the package its signature names
	// would render ext:T while the implementer indexed afterwards renders
	// dir:pkg\x00T, and the two would silently never match. Resolving after the
	// last add is what makes the answer the same for every declaration.
	pendingSigs []pendingSig
}

// pendingSig is one declaration's signature awaiting the completed scope set.
// It holds the DECLARING file's reference site, which is the only site that can
// resolve the spellings this declaration wrote.
type pendingSig struct {
	rec *declRec
	ref *treesitter.RefSite
	sig *treesitter.SigFacts
}

// deferSigKey records a declaration's signature for resolution at index
// completion. Called only after a successful add, so a record rejected as a
// duplicate never receives a key.
func (ix *declIndex) deferSigKey(rec *declRec, ref *treesitter.RefSite, sig *treesitter.SigFacts) {
	if sig == nil {
		return
	}
	ix.pendingSigs = append(ix.pendingSigs, pendingSig{rec: rec, ref: ref, sig: sig})
}

// resolveSigKeys renders every deferred signature key and MUST run after the
// last add and before any read of declRec.SigKey.
//
// It DRAINS the pending list, so a second call is a no-op rather than a repeat.
func (ix *declIndex) resolveSigKeys() {
	for _, p := range ix.pendingSigs {
		p.rec.SigKey = resolveSigKey(ix, p.ref, p.sig)
	}
	ix.pendingSigs = nil
}

// newDeclIndex pre-sizes all three maps from the total declaration count.
// Growing a map from zero across tens of thousands of declarations costs
// several rehashes for no reason — the count is known cheaply before the build.
func newDeclIndex(capacity int) *declIndex {
	return &declIndex{
		byID:        make(map[string]*declRec, capacity),
		byKey:       make(map[declKey][]*declRec, capacity),
		byScopeName: make(map[scopeNameKey][]*declRec, capacity),
		scopes:      make(map[string]bool),
	}
}

// add records one declaration in every view.
//
// It RETURNS AN ERROR on a duplicate NodeID, and that error is the whole
// enforcement of "a collision is unrepresentable": the property is only real
// because something checks it. A duplicate ID means ChunkNodeID or
// DeduplicateChunks has regressed — a defect to alarm on, not a case to serve —
// so the caller logs it and keeps the FIRST record.
func (ix *declIndex) add(rec *declRec) error {
	if prior, ok := ix.byID[rec.NodeID]; ok {
		return fmt.Errorf("duplicate declaration node ID %q (already held by %s:%s.%s)",
			rec.NodeID, prior.File, prior.Parent, prior.Name)
	}
	ix.byID[rec.NodeID] = rec

	k := declKey{Scope: rec.Scope, Parent: rec.Parent, Name: rec.Name}
	ix.byKey[k] = append(ix.byKey[k], rec)

	sk := scopeNameKey{Scope: rec.Scope, Name: rec.Name}
	ix.byScopeName[sk] = append(ix.byScopeName[sk], rec)

	ix.scopes[rec.Scope] = true
	return nil
}

// hasScope reports whether a scope contributes any declaration to the index.
func (ix *declIndex) hasScope(scope string) bool {
	return ix.scopes[scope]
}

// lookup returns every declaration under an exact parent in a scope. The
// returned slice is in build order, which is file order then in-file byte
// order — deterministic by construction, never a map range.
func (ix *declIndex) lookup(k declKey) []*declRec {
	return ix.byKey[k]
}

// lookupScopeName returns every declaration of a name within one scope,
// REGARDLESS of parent — the candidate set of a runtime dispatch. Its sets are
// legitimately larger than lookup's: a package with many same-named methods
// genuinely offers that many dispatch targets, whereas the same count under one
// exact key would mean the scope unit is too coarse.
func (ix *declIndex) lookupScopeName(k scopeNameKey) []*declRec {
	return ix.byScopeName[k]
}

// baseDeclName strips the "#<astPathHash>" suffix that resolveCollisionNames
// appends to a declaration whose (parent, name) collides inside its file.
//
// The suffix is part of the declaration's IDENTITY and flows into its node ID;
// it is never part of a KEY, because a reference carries the base name only.
// Keying on the base name is what lets a reference to a collided declaration
// find the whole surviving set rather than nothing at all.
func baseDeclName(name string) string {
	base, _, _ := strings.Cut(name, "#")
	return base
}

// stampDeclOwners records, on every indexed declaration, the node ID of the
// declaration that lexically encloses it.
//
// IT READS A FACT THE PIPELINE ALREADY ESTABLISHED rather than deriving a new
// one. resolveSlotEdges has, by this point, rewritten every containment slot
// into the container's final node ID, so a parent-to-member containment edge
// already names (container, member) with the path-hash disambiguation applied.
// Both sides of that identity are produced by ChunkNodeID — slotNodeID for the
// edge and appendChunkNode for the record — so the strings agree by
// construction rather than by convention.
//
// IT MUST RUN AFTER THE INDEX IS COMPLETE, because it addresses records by node
// ID and a record added later would not be stamped.
//
// A FILE-TO-SYMBOL CONTAINMENT EDGE IS SKIPPED WITHOUT A SPECIAL CASE: its
// source is a file path, which no declaration is indexed under, so the lookup
// simply misses. The same is true of a Go method's parent-to-member edge, whose
// source stays a NAME rather than a node ID — and leaving those unstamped is
// correct, because Go cannot put two same-named containers in one scope.
func stampDeclOwners(ix *declIndex, results []*treesitter.Result) {
	if ix == nil {
		return
	}
	for _, result := range results {
		for i := range result.Edges {
			e := &result.Edges[i]
			if e.Type != treesitter.EdgeContains {
				continue
			}
			member, ok := ix.byID[e.ToID]
			if !ok {
				continue
			}
			if _, ok := ix.byID[e.FromID]; !ok {
				continue
			}
			member.Owner = e.FromID
		}
	}
}
