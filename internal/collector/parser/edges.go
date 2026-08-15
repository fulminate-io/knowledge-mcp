// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"log/slog"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// maxAmbiguousGroup is the fanout above which an AMBIGUOUS group is reported.
// A closed group that large means the language's scope unit is too coarse for
// this corpus — the measured maximum is 7. It is deliberately NOT applied to
// dynamic groups, whose measured maximum is 27 and whose large sets are normal:
// a package with 27 methods named Get genuinely offers 27 dispatch targets. One
// threshold across both kinds would train the reader to ignore the warning.
const maxAmbiguousGroup = 8

// resolveEdges turns the chunker's raw edges into graph edges.
//
// CONTAINMENT ARRIVES ALREADY RESOLVED and does not consult the declaration
// index at all: the slot pre-pass rewrote its endpoints from the chunk slots
// the chunker recorded, so a containment edge is exact by construction. THE ONE
// EXCEPTION is a Go method's parent-to-member source — its container is the
// receiver TYPE, a sibling declaration that may live in another file, so no
// slot can address it and it is resolved here against the index at package
// scope, which is Go's own rule.
//
// REFERENCES are resolved by the scope walk, with FOUR outcomes. This
// supersedes the rule that an unresolvable edge is simply dropped, which is now
// a correct description of only the last of them:
//
//   - BOUND — exactly one declaration satisfies the rule that fired. One edge,
//     carrying none of the residue fields.
//   - AMBIGUOUS — several surviving declarations satisfy it. NOT silently
//     narrowed to one: one edge per candidate, each at Confidence 1/N, all
//     sharing one group key in Evidence, each stamped Method ambiguous-name.
//     The group is CLOSED: exactly one of these IS the referent, and the
//     collector does not know which.
//   - DYNAMIC with candidates — the language dispatches at runtime. Same shape,
//     stamped Method dynamic, and the group is OPEN: the referent is one of
//     these OR something no static enumeration can reach. A consumer must never
//     read an open group as closed, and must never collapse one to a single
//     edge — that asserts a closure the language denies.
//   - DYNAMIC with no candidates, and EXTERNAL — nothing is emitted. An empty
//     group would represent nothing, and for a stdlib call it is a correct
//     report of no in-repo target rather than the loss of a known one.
//
// The two Method values above are written here as the LITERAL strings a reader
// of the graph sees; the emitters use kgtypes.EdgeMethodAmbiguousName and
// kgtypes.EdgeMethodDynamic, which carry exactly those values.
func resolveEdges(results []*treesitter.Result, ix *declIndex, nodeIDs map[string]bool) []*knowledgev1.Edge {
	resolved, stats := resolveEdgesWithStats(results, ix, nodeIDs)
	stats.log()
	return resolved
}

// resolveStats counts what resolution DID over one populate run.
//
// DynamicUnbound is the reason this type exists. A dynamic reference whose
// scope declares nothing by that name emits no edge and forms no group, so it
// is invisible in the resulting graph — and it is the LARGEST population in the
// residue picture (69,517 measured against 12,604 dynamic groups, the values
// the committed corpus artifact carries). Counted, it is a reported outcome;
// uncounted, it is indistinguishable from a reference the walk never reached.
type resolveStats struct {
	Bound           int
	External        int
	AmbiguousGroups int
	AmbiguousEdges  int
	DynamicGroups   int
	DynamicEdges    int
	DynamicUnbound  int
	MaxDynamicGroup int

	// DotScopeBinds and DotScopeGroups are the DOT-SCOPE residue: references
	// the gathered unqualified rung resolved across a scope a dot import
	// folded in. A bind is one exact cross-scope answer; a group is one closed
	// ambiguous set, counted once per group rather than once per member.
	//
	// THIS IS RESOLUTION RESIDUE AND NOT A CENSUS OF THE CONSTRUCT. A corpus
	// can carry dot imports that bind nothing at all, and those are invisible
	// here by design — the pass reports that separately, because a count of
	// what an arm reported is not a resolution outcome.
	DotScopeBinds  int
	DotScopeGroups int
}

// log emits the run's residue picture. The two group kinds are reported on
// separate keys and never summed: an ambiguous group is CLOSED and a dynamic
// group is OPEN, so a total over both would state a property neither has.
func (s resolveStats) log() {
	slog.Info("collector: reference resolution",
		"bound", s.Bound,
		"external", s.External,
		"ambiguous_groups", s.AmbiguousGroups,
		"ambiguous_edges", s.AmbiguousEdges,
		"dynamic_groups", s.DynamicGroups,
		"dynamic_edges", s.DynamicEdges,
		"dynamic_unbound", s.DynamicUnbound,
		"max_dynamic_group_size", s.MaxDynamicGroup,
		"dot_scope_binds", s.DotScopeBinds,
		"dot_scope_groups", s.DotScopeGroups)
}

// resolveEdgesWithStats is resolveEdges plus the residue counts. It is the arm
// the corpus verification reads, so the numbers in the artifact come from the
// production walk rather than from a probe's re-implementation of it.
func resolveEdgesWithStats(
	results []*treesitter.Result, ix *declIndex, nodeIDs map[string]bool,
) ([]*knowledgev1.Edge, resolveStats) {
	var resolved []*knowledgev1.Edge
	var stats resolveStats
	for _, result := range results {
		for i := range result.Edges {
			e := &result.Edges[i]
			switch kgtypes.EdgeType(e.Type) {
			case kgtypes.EdgeImports:
				// File path → import path; nothing to resolve.
				resolved = append(resolved, &knowledgev1.Edge{
					FromId: e.FromID, ToId: e.ToID, Type: string(e.Type), Weight: e.Weight,
				})
			case kgtypes.EdgeContains:
				resolved = append(resolved, resolveContainment(e, ix, nodeIDs)...)
			default:
				resolved = append(resolved, resolveReference(e, ix, nodeIDs, &stats)...)
			}
		}
	}
	return resolved, stats
}

// resolveContainment passes through an already-exact containment edge and
// resolves the one shape that is not exact: a Go method's receiver source.
//
// The endpoint check against nodeIDs is load-bearing and must not be dropped —
// it is what discards the containment edge of a COMMENT orphan. collectOrphans
// admits comment nodes, but populate folds comment text into the following
// symbol's description and never creates a node for one, so that edge points at
// an ID no node carries.
func resolveContainment(e *treesitter.Edge, ix *declIndex, nodeIDs map[string]bool) []*knowledgev1.Edge {
	if !nodeIDs[e.ToID] {
		return nil
	}
	if nodeIDs[e.FromID] {
		return []*knowledgev1.Edge{{
			FromId: e.FromID, ToId: e.ToID, Type: string(e.Type), Weight: e.Weight,
		}}
	}

	// A GO RECEIVER SOURCE: a non-empty FromID that is not a node ID, on an
	// edge still carrying its reference site. Go's own rule says the receiver
	// type is declared in the same package, so it is looked up at package scope
	// against the collision-safe index — never against a global name map, and
	// never by retrying a bare last segment against every package.
	if e.FromID == "" || e.Ref == nil {
		return nil
	}
	receiver := e.FromID
	if i := strings.LastIndexByte(receiver, '.'); i >= 0 {
		receiver = receiver[i+1:]
	}
	candidates := ix.lookup(declKey{Scope: e.Ref.Scope, Name: baseDeclName(receiver)})
	switch len(candidates) {
	case 0:
		// Declared in no file of this package. Emit nothing rather than
		// reaching for a same-named declaration somewhere else.
		return nil
	case 1:
		return []*knowledgev1.Edge{{
			FromId: candidates[0].NodeID, ToId: e.ToID, Type: string(e.Type), Weight: e.Weight,
		}}
	default:
		// Two top-level types of one name in one Go package — a compile error
		// outside build tags, so Go's own rule says this should not happen. It
		// is honest to say the method belongs to one of them without claiming
		// to know which.
		slog.Warn("collector: ambiguous Go receiver containment",
			"scope", e.Ref.Scope, "receiver", receiver, "candidates", len(candidates))
		key := groupKey(e.Ref.File, e.RefByte, string(e.Type), e.FromID)
		return groupEdges(candidates, e.ToID, string(e.Type), kgtypes.EdgeMethodAmbiguousName, key, true)
	}
}

// resolveReference runs the scope walk over one CALLS / TEST_CALLS / USES_TYPE
// / EMBEDS edge and emits whatever its outcome calls for.
//
// It is the DEFAULT arm of the dispatch above rather than a listed set, so it
// takes every edge type that is neither IMPORTS nor CONTAINS — TEST_CALLS
// included, which resolves exactly as CALLS does and differs only in the type
// the emitted edge carries.
func resolveReference(
	e *treesitter.Edge, ix *declIndex, nodeIDs map[string]bool, stats *resolveStats,
) []*knowledgev1.Edge {
	if !nodeIDs[e.FromID] {
		return nil
	}
	res := resolveRef(ix, e.Ref, e.ToID)
	switch res.Status {
	case RefBound:
		stats.Bound++
		if res.Rule == RuleDotScope {
			stats.DotScopeBinds++
		}
		return []*knowledgev1.Edge{{
			FromId: e.FromID, ToId: res.Candidates[0].NodeID, Type: string(e.Type), Weight: e.Weight,
		}}
	case RefAmbiguous:
		if len(res.Candidates) >= maxAmbiguousGroup {
			slog.Warn("collector: oversized ambiguous reference set",
				"scope", e.Ref.Scope, "target", e.ToID, "candidates", len(res.Candidates))
		}
		stats.AmbiguousGroups++
		stats.AmbiguousEdges += len(res.Candidates)
		if res.Rule == RuleDotScope {
			// ONCE PER GROUP, not once per member: the two counters answer
			// "how many references did a dot scope decide", and a group is one
			// reference whose answer is a set.
			stats.DotScopeGroups++
		}
		key := groupKey(e.Ref.File, e.RefByte, string(e.Type), e.ToID)
		return groupEdges(res.Candidates, e.FromID, string(e.Type), kgtypes.EdgeMethodAmbiguousName, key, false)
	case RefDynamic:
		if len(res.Candidates) == 0 {
			// The largest population in the whole residue picture, and it
			// emits nothing: this scope declares no such name at all. Counted
			// rather than dropped silently — an outcome that leaves no edge is
			// otherwise indistinguishable from a reference never walked.
			stats.DynamicUnbound++
			return nil
		}
		stats.DynamicGroups++
		stats.DynamicEdges += len(res.Candidates)
		if len(res.Candidates) > stats.MaxDynamicGroup {
			// The only observable for dynamic fan-out: no per-group warning
			// fires on this kind, because large dynamic sets are normal.
			stats.MaxDynamicGroup = len(res.Candidates)
		}
		key := groupKey(e.Ref.File, e.RefByte, string(e.Type), e.ToID)
		return groupEdges(res.Candidates, e.FromID, string(e.Type), kgtypes.EdgeMethodDynamic, key, false)
	default:
		stats.External++
		return nil
	}
}

// groupEdges emits one edge per candidate, each carrying Confidence 1/N, the
// group's Method, and the group's shared key in Evidence.
//
// When reverse is true the candidates are the edge SOURCES — the containment
// case, where the ambiguity is in which declaration CONTAINS the target — and
// fixed is the shared target. Otherwise the candidates are the targets and
// fixed is the shared source.
func groupEdges(candidates []*declRec, fixed, edgeType, method, key string, reverse bool) []*knowledgev1.Edge {
	conf := 1 / float64(len(candidates))
	out := make([]*knowledgev1.Edge, 0, len(candidates))
	for _, c := range candidates {
		edge := &knowledgev1.Edge{
			Type:       edgeType,
			Confidence: conf,
			Method:     method,
			Evidence:   key,
		}
		if reverse {
			edge.FromId, edge.ToId = c.NodeID, fixed
		} else {
			edge.FromId, edge.ToId = fixed, c.NodeID
		}
		out = append(out, edge)
	}
	return out
}
