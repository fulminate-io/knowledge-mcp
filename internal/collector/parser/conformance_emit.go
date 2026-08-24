// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"log/slog"
	"sort"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// conformanceStats is what the declared-conformance pass could not turn into an
// edge, counted by REASON.
//
// THE REASONS ARE SEPARATE COUNTERS AND MUST STAY SEPARATE. A graph shows the
// same nothing for "this declaration named no supertype", "the supertype named
// nothing in this repository", "the supertype is not a contract" and "the
// spelling was ambiguous" — only a counter tells them apart, which is the same
// argument the method-set derivation's own log line rests on.
type conformanceStats struct {
	// Supertypes counts declared supertype entries SEEN, whatever became of
	// them — the denominator every other counter is read against.
	Supertypes int
	// TypePairs counts emitted type-level relationships.
	TypePairs int
	// MemberPairs counts emitted member-level relationships.
	MemberPairs int
	// Unresolvable counts supertypes whose spelling named no in-repo
	// declaration: an out-of-repo supertype, or one in a part of the tree that
	// contributed nothing. It is computed HERE, against a COMPLETE index, which
	// is the only point the question has a stable answer — asked while the index
	// is still filling, it would answer differently depending on file order.
	Unresolvable int
	// NonContract counts supertypes that resolved to exactly one declaration
	// which is NOT a contract — a concrete base class, typically.
	//
	// SEPARATE FROM Unresolvable DELIBERATELY. The two describe opposite
	// situations: one is a supertype this repository does not contain, the other
	// is one it contains and declines to fan out from. Folding them would make
	// each indistinguishable from the other in the only place either is visible.
	NonContract int
	// AmbiguousSupertype counts supertype spellings that resolved to MORE THAN
	// ONE declaration. Nothing is emitted at either level: with two candidates
	// the supertype is genuinely unknown, and taking the head is a wrong-target
	// generator.
	AmbiguousSupertype int
	// ReopenedSupertype counts supertype spellings resolved by DISCARDING
	// REOPENINGS OF THE CONTRACT: the spelling collided, exactly one candidate
	// was a contract, and every other candidate was another body of that same
	// nominal type. Each one counted here would otherwise have been an
	// AmbiguousSupertype decline, so the two are read together.
	ReopenedSupertype int
	// AmbiguousMember counts member pairings declined because one side's name
	// matched more than one record — overloading, most often. The TYPE-LEVEL
	// edge still stands; only the overloaded pair declines.
	AmbiguousMember int
}

// conformPair is one resolved declared-conformance relationship.
type conformPair struct {
	supertype *declRec
	subtype   *declRec
	kind      treesitter.ConformanceKind
	members   []conformMemberPair
}

// emitDeclaredConformanceEdges projects every DECLARED supertype clause into
// IMPLEMENTS edges at both levels.
//
// THIS IS WHERE RESOLUTION HAPPENS, AND IT HAPPENS NOWHERE ELSE ON THIS PATH.
// The capture stage stores a spelling and a clause kind and reads no index at
// all; every lookup below runs against an index that is complete across every
// file. A lookup at capture time would see a half-built index and its answer
// would depend on file order — the same reason signature keys are deferred.
//
// DIRECTION IS SUPERTYPE OUTWARD TO SUBTYPE, and SOURCE ORDER IS THE OPPOSITE
// OF EDGE ORDER: the source writes `class Server: Greeter`, and the edge runs
// Greeter → Server. Stating it is worth the line, because getting it backwards
// produces a graph that looks entirely plausible and answers every traversal
// wrongly. It matches the method-set derivation, which emits
// interface → concrete, and the edge type's own documented contract.
//
// A SUPERTYPE THAT IS NOT A CONTRACT EMITS NOTHING. The edge type records that
// something satisfies an interface, directed from the interface outward so a
// caller standing on a call's target reaches the implementers in one hop. A
// concrete base class's method IS the callable implementation; fanning a call
// resolved to it out across every subclass would state a fact the edge type
// does not mean. The outcome is counted rather than emitted.
//
// A MODULE-LEVEL-ONLY RESULT IS A SUPPORTED OUTCOME, NOT A DEFECT. Where a
// language's container and its members are the same node kind, the members
// carry no container name, so no member pairing can exist — the type-level edge
// is emitted alone and that is the complete, correct answer for that language.
//
// These edges never enter the resolution walk: both endpoints are already node
// IDs taken off the declaration records.
func emitDeclaredConformanceEdges(ix *declIndex) []*knowledgev1.Edge {
	pairs, stats := deriveDeclaredConformance(ix)
	if len(pairs) == 0 {
		conformanceLog(stats, 0)
		return nil
	}

	// Pre-sized from the pair count plus one member edge per paired member,
	// rather than grown from nil across thousands of appends — the same shape
	// the method-set emitter uses.
	size := len(pairs)
	for _, p := range pairs {
		size += len(p.members)
	}
	out := make([]*knowledgev1.Edge, 0, size)
	for _, p := range pairs {
		// ONE VALUE PER PAIR, stamped on the type-level edge and on every member
		// edge beneath it, byte-for-byte — the contract the method-set emitter
		// already holds to. The suffix is the DECLARED clause kind, carried
		// through capture unmodified; it is never a fabricated cardinality,
		// because no method set was measured here and a number in that field
		// would be a false statement in the one field consumers weight on.
		method := kgtypes.EdgeMethodDeclaredConformance + string(p.kind)
		out = append(out, implementsEdge(p.supertype.NodeID, p.subtype.NodeID, method))
		for _, m := range p.members {
			out = append(out, implementsEdge(m.spec.NodeID, m.impl.NodeID, method))
		}
	}
	conformanceLog(stats, len(out))
	return out
}

// deriveDeclaredConformance resolves every declared supertype against the
// COMPLETE index and returns the relationships that survive, with a count of
// every outcome that did not.
//
// It is separate from the emitter for the reason the method-set derivation's
// own split exists: the counts are the pass's only account of what it declined,
// and a projection that folded them into an edge slice would leave every
// decline reason unobservable.
func deriveDeclaredConformance(ix *declIndex) ([]conformPair, conformanceStats) {
	var stats conformanceStats
	if ix == nil {
		return nil, stats
	}
	members := conformMemberNames(ix)

	var pairs []conformPair
	for _, rec := range conformingDecls(ix) {
		for _, c := range rec.Conforms {
			stats.Supertypes++
			sup, ok := conformResolveSupertype(ix, rec, c, &stats)
			if !ok {
				continue
			}
			pairs = append(pairs, conformPair{
				supertype: sup,
				subtype:   rec,
				kind:      c.Kind,
				members:   conformMembers(ix, members, sup, rec, &stats),
			})
		}
	}
	stats.TypePairs = len(pairs)
	return pairs, stats
}

// conformResolveSupertype resolves ONE declared supertype to the single contract
// declaration it names, counting every outcome that is not exactly that.
//
// THE EXACTLY-ONE-OWNER RULE is the same discipline the typed qualifier hop
// applies: with two candidates the supertype is genuinely unknown, and picking
// one would manufacture a wrong target rather than admit the uncertainty.
//
// A COLLIDED SET IS OFFERED TO THE NARROWING FIRST, AND ONLY A COLLISION THAT IS
// NOT A REAL AMBIGUITY SURVIVES IT: a set holding one contract and nothing else
// but reopenings of that contract names ONE referent, so it resolves to the
// contract and the narrowing is counted. Every other collided set still
// declines. The narrowing is applied HERE rather than inside the lookup because
// conformSupertypeCandidates may answer from the inner container or from file
// scope, and only this caller knows which set came back.
func conformResolveSupertype(ix *declIndex, rec *declRec, c conformRef, stats *conformanceStats) (*declRec, bool) {
	tr := resolveTypeTextThroughIndex(ix, rec.Ref, c.Text)
	if tr.Scope == "" {
		stats.Unresolvable++
		return nil, false
	}
	cands := conformSupertypeCandidates(ix, rec, tr)
	// THE COLLISION GUARD IS LOAD-BEARING, NOT DECORATION. Without it a
	// ONE-element candidate set also reaches the helper, which returns that
	// single element, the length test holds, and the counter increments on
	// essentially every resolution instead of on the narrowings. Measured by
	// mutation on a pinned swift corpus: removing the guard moves
	// reopened_supertype from 122 to 254 — exactly type_pairs plus non_contract,
	// i.e. every single-candidate resolution counted — and the number stops
	// meaning anything.
	if len(cands) > 1 {
		if narrowed := conformNarrowToContract(cands); len(narrowed) == 1 {
			cands = narrowed
			stats.ReopenedSupertype++
		}
	}
	switch {
	case len(cands) == 0:
		stats.Unresolvable++
		return nil, false
	case len(cands) > 1:
		stats.AmbiguousSupertype++
		return nil, false
	}
	sup := cands[0]
	if !sup.IsInterface {
		stats.NonContract++
		return nil, false
	}
	return sup, true
}

// conformNarrowToContract narrows a COLLIDED candidate set to the single
// contract it holds, when every other candidate is a REOPENING of that contract
// rather than a rival declaration.
//
// RETURNING THE INPUT UNCHANGED IS THE ABSTENTION, and it is the same discipline
// the type-reference alias states: claim the narrowing only when EXACTLY ONE
// colliding declaration survives, and claim nothing when two or more contracts
// survive or when a candidate is no reopening at all.
//
// PREFERRING THE CONTRACT CANNOT MANUFACTURE AN EDGE THE EMITTER WOULD HAVE
// SUPPRESSED, because a supertype resolving to a non-contract already emits
// nothing and is counted instead.
//
// THE REOPENING FENCE IS NOT WHAT MAKES THIS SAFE IN SWIFT, and stating that
// plainly is the point of this paragraph — a wrong reason on a right rule is the
// sentence that later licenses loosening it. The swift arm sets PartialBody
// UNCONDITIONALLY on every reopenable container, and ONE node kind serves class,
// struct, enum, actor and extension, so an unrelated `class Greeter` is
// byte-identical to an extension of `protocol Greeter` in every field this rule
// reads. conformSameNominal adds nothing discriminating either: every candidate
// came from ONE {Scope, Parent, Name} lookup and so already agrees on all three,
// leaving Lang the only field it newly compares. Measured, first-hand: a fixture
// of `protocol Greeter` beside an unrelated `public class Greeter` in one module
// DOES narrow to the protocol.
//
// WHAT MAKES IT SAFE IS UNREACHABILITY. Two same-named top-level types in one
// swift module do not compile, so that candidate set cannot arise from source a
// swift toolchain accepts; and swiftModuleScope keys `Sources/X` and `Tests/X`
// as DIFFERENT scopes, so a same-named type in a test target never joins the
// candidate set.
//
// WHAT THE FENCE DOES BUY is the languages where PartialBody is CONDITIONAL —
// C# sets it per `partial` block — plus the two shapes the abstention fixtures
// pin: a collider that is not a reopening at all, and two same-named contracts.
func conformNarrowToContract(cands []*declRec) []*declRec {
	var contract *declRec
	for _, c := range cands {
		if !c.IsInterface {
			continue
		}
		if contract != nil {
			return cands
		}
		contract = c
	}
	if contract == nil {
		return cands
	}
	for _, c := range cands {
		if c == contract {
			continue
		}
		if !c.PartialBody || !conformSameNominal(contract, c) {
			return cands
		}
	}
	return []*declRec{contract}
}

// conformSupertypeCandidates looks a supertype spelling up in the scopes the
// declaring declaration can actually see, INNERMOST CONTAINER FIRST.
//
// A DECLARATION WRITTEN INSIDE A BRACED CONTAINER SEES ITS CONTAINER'S OWN
// DECLARATIONS BEFORE THE FILE'S, and asking only with an empty parent misses
// every sibling declared beside it. Measured: a php `namespace App { interface
// Writer ... class Server implements Writer }` indexes both under Parent "App",
// so the empty-parent lookup found nothing and EVERY conformance declared
// inside a braced namespace resolved to nothing at all. C#'s braced namespace
// has the identical shape. TypeScript's `namespace X {}` does NOT — it is not a
// container here, so its declarations stay top-level and were never affected.
//
// THE ORDER MIRRORS THE LANGUAGES' OWN NAME RESOLUTION rather than preferring
// whichever answer exists. Both php and C# resolve an unqualified name in the
// enclosing namespace before the outer scope, so the inner lookup is tried
// first and the outer one answers when the inner names nothing. This is not a
// recovery path for a failed lookup: it is the lookup, asked in the order the
// language asks it.
//
// THE CALLER STILL DECIDES, and it applies one rule to whichever set comes back
// — an inner lookup returning several candidates is narrowed, or declined as
// ambiguous, on exactly the terms a top-level one is. A declaration with no
// container is unaffected: it skips the inner lookup entirely and its candidate
// set is byte-identical to what it was.
func conformSupertypeCandidates(ix *declIndex, rec *declRec, tr typeRef) []*declRec {
	if rec.Parent != "" {
		if inner := ix.lookup(declKey{Scope: tr.Scope, Parent: rec.Parent, Name: tr.Name}); len(inner) > 0 {
			return inner
		}
	}
	return ix.lookup(declKey{Scope: tr.Scope, Name: tr.Name})
}

// conformCoOwner reports whether two container declarations are BODIES OF ONE
// TYPE, so a member declared in either satisfies a conformance declared on the
// other.
//
// IT IS A LANGUAGE-SEMANTICS QUESTION AND IS ANSWERED PER LANGUAGE, because two
// containers sharing a scope and a name mean opposite things in different
// languages. C# `partial` blocks ARE one type, so a member in one block
// implements an interface the other block names. A scala companion object
// beside its trait, a family of generic arities sharing a base name, a class
// redefined later in a python module, and one library vendored twice are the
// OPPOSITE — distinct types that merely collide — and each must keep confining,
// because crossing between them is the wrong-edge defect this rule exists to
// stop. A rust inherent impl belongs on the confining side too: a trait's
// default method satisfies the trait, not the inherent block.
//
// THE FLAG IS THE LANGUAGE ARM'S TO SET, which is what makes a further language
// a DATA addition rather than a change here.
//
// SWIFT IS THE SECOND LANGUAGE WIRED AND IT REACHES THE FLAG BY A DIFFERENT
// ROUTE THAN C#, which is the case that shows why the flag is the arm's to set
// rather than something this predicate could infer. C# reads a `partial`
// keyword each block states for itself; swift has no such keyword and needs
// none, because ANY class, struct or enum is reopenable by an extension. Its
// arm therefore sets the flag on EVERY CONTAINER OF A REOPENABLE TYPE — the
// type and its extensions alike — which is what the flag's own definition asks
// for, and it is what lets this predicate stay a symmetric both-sides test.
// FLAGGING ONLY THE EXTENSIONS WOULD NOT WORK: an unflagged class paired
// against a flagged extension would still lose its member, which is the very
// edge the wiring exists to keep.
//
// A SWIFT PROTOCOL IS NOT FLAGGED even though it is equally reopenable, and
// the exclusion is LOAD-BEARING. A protocol extension supplies DEFAULT
// IMPLEMENTATIONS of the very requirements the protocol declares, so flagging
// the protocol would put a requirement and its default under ONE owner, hand
// this predicate's caller two candidates for one name, and lose the member
// edge — measured on the mutated tree as member_pairs=0 with
// ambiguous_member=1. Unflagged, the requirement pairs and the extension's
// default declines.
//
// IT BECAME SO WHEN THE SUPERTYPE COLLISION STOPPED DECLINING. Such a protocol
// was previously declined as an ambiguous supertype one layer above this
// predicate, so no pair reached member pairing at all and the exclusion decided
// nothing. conformNarrowToContract now resolves that collided set to the
// contract, so every such pair reaches here. The swift arm states the same
// boundary and records the measurement behind it.
func conformCoOwner(a, b *declRec) bool {
	if a == nil || b == nil {
		return false
	}
	if !a.PartialBody || !b.PartialBody {
		return false
	}
	return conformSameNominal(a, b)
}

// conformSameNominal reports whether two declarations name the SAME NOMINAL
// IDENTITY — the same language, resolution scope, container and base name.
//
// IT IS THE SINGLE AUTHORITY FOR THAT COMPARISON, extracted rather than copied
// when a second reader appeared: an ast census over the whole collector for this
// shape returned exactly one match, and two spellings of one identity rule is
// how the two drift apart.
//
// IT IS NOT ON ITS OWN A CO-OWNERSHIP TEST. Two same-named declarations may be
// two bodies of one type or two unrelated types that merely collide, and only
// the language's own reopening flag tells those apart — which is why
// conformCoOwner guards this call rather than being replaced by it.
func conformSameNominal(a, b *declRec) bool {
	return a.Lang == b.Lang && a.Scope == b.Scope && a.Parent == b.Parent && a.Name == b.Name
}

func conformingDecls(ix *declIndex) []*declRec {
	out := make([]*declRec, 0, len(ix.byID))
	for _, rec := range ix.byID {
		if len(rec.Conforms) > 0 {
			out = append(out, rec)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out
}

// conformanceLog reports what the pass saw and what it declined to emit.
func conformanceLog(stats conformanceStats, edges int) {
	slog.Info("collector: declared conformance",
		"supertypes", stats.Supertypes,
		"type_pairs", stats.TypePairs,
		"member_pairs", stats.MemberPairs,
		"edges", edges,
		"unresolvable", stats.Unresolvable,
		"non_contract", stats.NonContract,
		"ambiguous_supertype", stats.AmbiguousSupertype,
		"reopened_supertype", stats.ReopenedSupertype,
		"ambiguous_member", stats.AmbiguousMember)
}
