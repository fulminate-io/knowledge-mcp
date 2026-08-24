// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"log/slog"
	"sort"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// slotBindStats is what the slot-bind pass could not turn into an edge, counted
// by REASON.
//
// THE REASONS ARE SEPARATE COUNTERS AND MUST STAY SEPARATE, for the reason the
// declared-conformance pass gives about its own: a graph shows the same nothing
// for "the struct type named nothing", "the slot has no field node", "the
// target named nothing" and "the target was ambiguous", and only a counter
// tells them apart.
type slotBindStats struct {
	// Binds counts slot binds SEEN, whatever became of them — the denominator
	// every other counter is read against.
	Binds int
	// Edges counts emitted relationships.
	Edges int
	// UnresolvedType counts binds whose STRUCT TYPE spelling named no in-repo
	// declaration, so the slot could not be located at all.
	UnresolvedType int
	// UnknownSlot counts binds whose struct resolved but whose slot has NO
	// FIELD NODE. That is the ordinary outcome for a plain data field: only a
	// function-pointer field becomes a node, so the slot's
	// function-pointer-ness is enforced by the node's EXISTENCE rather than by
	// a second predicate that could drift from the query that creates it.
	UnknownSlot int
	// UnresolvedTarget counts binds whose TARGET spelling named no in-repo
	// declaration — a macro that expands to a name the preprocessor owns, or a
	// function defined outside this repository. A macro is indistinguishable
	// from a function name in the parse tree, so it is captured as a spelling
	// and declines HERE rather than at capture.
	UnresolvedTarget int
	// AmbiguousTarget counts targets resolving to MORE THAN ONE declaration.
	// Nothing is emitted: with two candidates the target is genuinely unknown,
	// and taking the head is a wrong-target generator.
	AmbiguousTarget int
	// DeclinedInitializers counts POSITIONAL binds whose Index falls outside
	// the resolved struct's recorded field order. That means the type resolved
	// to the wrong declaration, so the whole initializer is untrustworthy
	// rather than one element of it.
	DeclinedInitializers int
}

// emitSlotBindEdges projects every captured composite-literal slot bind into an
// IMPLEMENTS edge from the FIELD to the declaration filling it.
//
// A C STRUCT OF FUNCTION POINTERS IS C'S INTERFACE, and a composite literal
// filling one of its slots is C's implementation of it. The language declares
// no supertype, so there is no clause to read; this is the relationship it
// writes instead.
//
// DIRECTION IS FIELD OUTWARD TO TARGET, matching the edge type's own contract:
// a consumer standing on a call's target — which after this phase is the field
// node, because a bound dispatch resolves there — walks out over IMPLEMENTS to
// reach the concrete functions in one hop.
//
// RESOLUTION HAPPENS HERE AND NOWHERE ELSE ON THIS PATH. The capture stage
// stores spellings and reads no index; every lookup below runs against an index
// complete across every file, which is also what lets a target declared in an
// included header bind through the include binds that already exist.
//
// These edges never enter the resolution walk: both endpoints are node IDs
// taken off declaration records.
func emitSlotBindEdges(ix *declIndex) []*knowledgev1.Edge {
	edges, stats := deriveSlotBindEdges(ix)
	slotBindLog(stats)
	return edges
}

// deriveSlotBindEdges is the derivation emitSlotBindEdges projects.
//
// It is separate from the emitter for the reason the declared-conformance
// pass's own split exists: the counts are this pass's only account of what it
// declined, and a projection that folded them into an edge slice would leave
// every decline reason unobservable.
func deriveSlotBindEdges(ix *declIndex) ([]*knowledgev1.Edge, slotBindStats) {
	var stats slotBindStats
	if ix == nil {
		return nil, stats
	}
	recs := slotBindDecls(ix)
	size := 0
	for _, rec := range recs {
		size += len(rec.SlotBinds)
	}
	if size == 0 {
		return nil, stats
	}

	out := make([]*knowledgev1.Edge, 0, size)
	for _, rec := range recs {
		for _, bind := range rec.SlotBinds {
			stats.Binds++
			field, target, ok := resolveSlotBind(ix, rec, bind, &stats)
			if !ok {
				continue
			}
			out = append(out, implementsEdge(field.NodeID, target.NodeID, slotBindMethod(bind)))
			stats.Edges++
		}
	}
	if len(out) == 0 {
		return nil, stats
	}
	return out, stats
}

// resolveSlotBind applies the two gates one bind must pass, counting every
// outcome that is not an edge.
//
// THERE ARE EXACTLY TWO GATES AND NO THIRD. Nothing here asserts the target IS
// a function: declRec carries no declaration-kind field, and adding one to a
// shared record for this one case would be a shared-surface change for a
// residual class the corpora say is rare — a function-POINTER VARIABLE filling
// a slot rather than a function. Such a target is a real declaration that
// genuinely fills the slot, so the edge is INDIRECT rather than false. The
// precision measurement reports the target-kind distribution so the residual is
// measured rather than assumed negligible.
func resolveSlotBind(ix *declIndex, rec *declRec, bind treesitter.SlotBind, stats *slotBindStats) (field, target *declRec, ok bool) {
	// GATE A — the slot must have a FIELD NODE.
	owner, found := slotBindOwner(ix, rec, bind.Type)
	if !found {
		stats.UnresolvedType++
		return nil, nil, false
	}
	name, inRange := slotBindFieldName(owner, bind)
	if !inRange {
		stats.DeclinedInitializers++
		return nil, nil, false
	}
	fields := ix.lookup(declKey{Scope: owner.Scope, Parent: owner.Name, Name: name})
	if len(fields) != 1 {
		stats.UnknownSlot++
		return nil, nil, false
	}

	// GATE B — the target must resolve to EXACTLY ONE declaration.
	tr := resolveTypeTextThroughIndex(ix, rec.Ref, bind.Target)
	if tr.Scope == "" {
		stats.UnresolvedTarget++
		return nil, nil, false
	}
	cands := ix.lookup(declKey{Scope: tr.Scope, Name: tr.Name})
	switch {
	case len(cands) == 0:
		stats.UnresolvedTarget++
		return nil, nil, false
	case len(cands) > 1:
		stats.AmbiguousTarget++
		return nil, nil, false
	}
	return fields[0], cands[0], true
}

// slotBindOwner resolves the struct type a bind's literal initialized, and
// requires it to name EXACTLY ONE declaration.
func slotBindOwner(ix *declIndex, rec *declRec, typeText string) (*declRec, bool) {
	tr := resolveTypeTextThroughIndex(ix, rec.Ref, typeText)
	if tr.Scope == "" {
		return nil, false
	}
	cands := ix.lookup(declKey{Scope: tr.Scope, Name: tr.Name})
	if len(cands) != 1 {
		return nil, false
	}
	return cands[0], true
}

// slotBindFieldName returns the slot's field name, reporting false when a
// POSITIONAL index falls outside the struct's recorded field order.
//
// AN OUT-OF-RANGE INDEX MEANS THE TYPE RESOLVED TO THE WRONG DECLARATION, so
// the whole initializer is untrustworthy rather than one element of it. FEWER
// elements than fields is legal C and stays exact, because the mapping is
// per-position and trailing fields are simply unmentioned.
func slotBindFieldName(owner *declRec, bind treesitter.SlotBind) (string, bool) {
	if bind.Field != "" {
		return bind.Field, true
	}
	if bind.Index < 0 || bind.Index >= len(owner.FieldOrder) {
		return "", false
	}
	// An EMPTY entry is an anonymous member: it holds its position so later
	// elements stay aligned, and it names no slot an edge could run from.
	return owner.FieldOrder[bind.Index], true
}

// slotBindMethod renders the Edge.Method value: the prefix followed by the
// SHAPE that produced the edge.
//
// THE SHAPE RATHER THAN THE SLOT NAME. A reader judging precision needs to know
// which capture shape produced an edge — the designated form states its field
// outright while the positional form derives it from the declaration's field
// order — and the set stays small and closed.
func slotBindMethod(bind treesitter.SlotBind) string {
	if bind.Field != "" {
		return kgtypes.EdgeMethodSlotBind + "designated"
	}
	return kgtypes.EdgeMethodSlotBind + "positional"
}

// slotBindDecls returns every declaration carrying a slot bind, in NODE ID
// ORDER.
//
// The order is imposed rather than inherited: the index's identity view is a
// map, and emitting from a map range would reorder the edge slice between runs
// of one unchanged repository.
func slotBindDecls(ix *declIndex) []*declRec {
	out := make([]*declRec, 0, len(ix.byID))
	for _, rec := range ix.byID {
		if len(rec.SlotBinds) > 0 {
			out = append(out, rec)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out
}

// slotBindLog reports what the pass saw and what it declined to emit.
func slotBindLog(stats slotBindStats) {
	slog.Info("collector: slot binds",
		"binds", stats.Binds,
		"edges", stats.Edges,
		"unresolved_type", stats.UnresolvedType,
		"unknown_slot", stats.UnknownSlot,
		"unresolved_target", stats.UnresolvedTarget,
		"ambiguous_target", stats.AmbiguousTarget,
		"declined_initializers", stats.DeclinedInitializers)
}
