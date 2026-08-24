// SPDX-License-Identifier: Apache-2.0

package parser

import "sort"

// THIS FILE IS THE MEMBER-PAIRING LAYER of the declared-conformance pass, split
// out of conformance_emit.go, which holds the emission and the SUPERTYPE
// resolution above it. The seam is the one the pass itself already has: a
// supertype resolves to exactly one contract first, and only then are that
// contract's members paired against the conforming declaration's. Nothing here
// is reachable until the layer above has answered.

// conformOwner identifies a container whose members are indexed under it.
type conformOwner struct {
	Scope string
	Name  string
}

// conformMemberPair is one supertype member and the subtype member that carries
// its name.
type conformMemberPair struct {
	spec *declRec
	impl *declRec
}

// conformMembers pairs each of the supertype's members with the subtype member
// of the same name.
//
// PAIRING IS BY NAME ONLY. No signature is compared: a declared clause states
// the relationship outright, so there is nothing to infer and nothing that
// would justify declining a member the source itself declared. Overloading is
// the breaking case, and it declines rather than guessing.
//
// A NAME IS NOT ENOUGH TO SAY WHOSE MEMBER IT IS, which is what the ownership
// check exists for. A member key names its container by BASE NAME and discards
// the container's own Parent, so members collide whenever two containers share
// a scope and a base name — a scala companion beside its trait, a family of
// generic arities, one library vendored twice — AND ALSO whenever two
// containers differ only in their Parent, whose own keys stay distinct while
// their members do not.
//
// OWNERSHIP IS THEREFORE THE PREDICATE ON BOTH SIDES, and an earlier revision
// that narrowed it to a "shared container key" question was wrong twice over:
// it no-opped for the differ-only-in-Parent shape, and it argued the spec side
// unreachable on the grounds that a resolved supertype is always top-level —
// which supertype resolution into a container has since disproved. Measured on
// the pinned scala corpus, the narrowed form emitted member relationships whose
// endpoint belonged to neither declaration named, with the ambiguity counter
// reading zero because exactly one record carried the key.
func conformMembers(
	ix *declIndex,
	members map[conformOwner][]string,
	sup, sub *declRec,
	stats *conformanceStats,
) []conformMemberPair {
	names := members[conformOwner{Scope: sup.Scope, Name: sup.Name}]
	if len(names) == 0 {
		return nil
	}
	supShared := conformKeyIsShared(ix, sup)
	subShared := conformKeyIsShared(ix, sub)
	out := make([]conformMemberPair, 0, len(names))
	for _, name := range names {
		specs := conformOwnedBy(ix, ix.lookup(declKey{Scope: sup.Scope, Parent: sup.Name, Name: name}), sup, supShared)
		impls := conformOwnedBy(ix, ix.lookup(declKey{Scope: sub.Scope, Parent: sub.Name, Name: name}), sub, subShared)
		if len(impls) == 0 {
			// The subtype declares no member of this name. That is ordinary —
			// an inherited member is not redeclared — and is not a decline. It
			// is also the answer when every candidate belonged to a container
			// that merely shares this one's name, which states the same fact.
			continue
		}
		if len(specs) == 0 {
			// THE SUPERTYPE'S NAME LIST IS DRAWN FROM A KEY OTHER CONTAINERS
			// ALSO WRITE INTO, so a name on it may belong to none of this
			// supertype's own members. Skipping here is not defensive padding:
			// without it the ambiguity check below hands a zero-length slice to
			// specs[0] and PANICS, which aborts an entire collect.
			continue
		}
		if len(specs) > 1 || len(impls) > 1 {
			stats.AmbiguousMember++
			continue
		}
		out = append(out, conformMemberPair{spec: specs[0], impl: impls[0]})
	}
	stats.MemberPairs += len(out)
	if len(out) == 0 {
		return nil
	}
	return out
}

// conformKeyIsShared reports whether more than one declaration is indexed under
// this declaration's OWN key.
//
// ITS ONE READER IS THE UNADDRESSABLE-OWNER CASE. It is NOT the condition under
// which members collide — members collide on a key that drops the container's
// Parent — so it decides only whether a candidate carrying no owner at all can
// still be attributed to this container.
func conformKeyIsShared(ix *declIndex, rec *declRec) bool {
	return len(ix.lookup(declKey{Scope: rec.Scope, Parent: rec.Parent, Name: rec.Name})) > 1
}

// conformOwnedBy narrows a member lookup to the candidates the named container
// actually owns.
//
// IT FILTERS ON EVERY CALL rather than only where a container key looks shared,
// for the reason conformMembers states: a shared container key is not the
// condition under which members collide.
//
// THERE IS NO ALLOCATION ON THE COMMON PATH. Almost every lookup is already
// wholly owned by the container it was asked about, so the candidates are
// scanned first and the INPUT SLICE is returned untouched when all of them
// pass. Only a lookup that must really lose a candidate allocates — and it
// allocates rather than compacting in place, because the input is the index's
// own slice and rewriting it would corrupt the index for every later reader.
//
// AN UNADDRESSABLE OWNER IS TRUSTED ONLY WHILE NOTHING SHARES THE KEY. A record
// carries no owner when the chunker could not address its container
// positionally; such a candidate cannot be attributed, so it is accepted while
// the container's own key is unshared — the condition under which it cannot
// belong to anyone else — and dropped once it could.
func conformOwnedBy(ix *declIndex, cands []*declRec, container *declRec, shared bool) []*declRec {
	keep := func(c *declRec) bool {
		if c.Owner == "" {
			return !shared
		}
		if c.Owner == container.NodeID {
			return true
		}
		return conformCoOwner(container, ix.byID[c.Owner])
	}
	for _, c := range cands {
		if keep(c) {
			continue
		}
		out := make([]*declRec, 0, len(cands))
		for _, keeper := range cands {
			if keep(keeper) {
				out = append(out, keeper)
			}
		}
		return out
	}
	return cands
}

// conformMemberNames maps each container to the member names declared under it,
// each list sorted so the emitted member edges are ordered deterministically.
//
// It is built ONCE per pass rather than per pair, because the question — which
// names does this container declare — is answered by a walk of the whole keyed
// view, and a pair-local walk would repeat it for every conforming declaration.
func conformMemberNames(ix *declIndex) map[conformOwner][]string {
	out := map[conformOwner][]string{}
	for k := range ix.byKey {
		if k.Parent == "" {
			continue
		}
		owner := conformOwner{Scope: k.Scope, Name: k.Parent}
		out[owner] = append(out[owner], k.Name)
	}
	for owner := range out {
		sort.Strings(out[owner])
	}
	return out
}
