// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"context"
	"sort"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/paging"
)

// dispatch_edge_groups.go completes partially-observed candidate groups.
//
// WHY A GROUP CAN BE PARTIAL. The server iterates ALL outgoing edges of each
// frontier node, so a FORWARD walk observes every member of a group — they all
// leave the same node. A REVERSE walk from a candidate observes only the ONE
// member edge pointing at it; its siblings leave a node the reverse walk never
// expands, and under the uniform short-circuit it must never expand it. Without
// enrichment such a walk could say "1 of 3" but could not name the other two,
// and the ruling asks for the ambiguity to be REPORTED, not merely counted.
//
// THE TRIGGER IS EXACT, NOT HEURISTIC: Confidence is 1/N, so N is recoverable and
// a group with fewer observed members than N is provably partial.

// EnrichCandidateGroups fills in the unobserved members of incomplete groups and
// hydrates every candidate node, in AT MOST TWO Executes for the whole response,
// regardless of how many groups are partial.
//
// ZERO EXECUTES ON THE COMMON PATH: when every group is already complete — which
// is every group on a forward walk — this returns immediately, having issued no
// reads at all.
//
// WHICH CALLER HYDRATES, so neither grows a second path: renderTraversalResponse
// builds its candidate node map from its OWN traversal results and calls this
// only when some group is incomplete; the analyze composer ALWAYS calls it,
// because its candidates are not guaranteed to appear in its node slices.
//
// BEST-EFFORT, AND ITS FAILURE IS VISIBLE RATHER THAN SILENT. A failed sibling
// read returns the unenriched groups together with the error; the caller logs it,
// renders what it has, and flags the reconstruction incomplete. A traversal that
// succeeded must never become an error result because an enrichment nicety
// failed. NO CANDIDATE IS EVER FABRICATED to reach N.
func EnrichCandidateGroups(
	ctx context.Context,
	exec ExecuteFn,
	groups []CandidateGroup,
	target *knowledgev1.GraphSelector,
) ([]CandidateGroup, map[string]*knowledgev1.Node, error) {
	// STEP A — early exit. The distinct sources of the groups that are provably
	// short of their declared size.
	incompleteSources := make([]string, 0, len(groups))
	seenSource := make(map[string]bool, len(groups))
	for i := range groups {
		if groups[i].Complete() {
			continue
		}
		if from := groups[i].FromID; from != "" && !seenSource[from] {
			seenSource[from] = true
			incompleteSources = append(incompleteSources, from)
		}
	}

	if len(incompleteSources) == 0 {
		// The forward-walk path: nothing to complete, so nothing is read.
		return groups, nil, nil
	}

	// STEP B — ONE bounded pivot-edge read over the distinct sources.
	sort.Strings(incompleteSources) // deterministic request shape
	siblings, err := paging.DrainPivotEdges(incompleteSources, paging.EdgePivotPageSize, CorrelationsEdgeScanCap,
		func(idPage []string, fromIDGte, fromIDLt string) ([]knowledgev1.Edge, bool, error) {
			return pivotEdgePage(ctx, exec, idPage, paging.EdgeFromBandOrNil(fromIDGte, fromIDLt), false, target)
		})
	if err != nil {
		return groups, nil, err
	}

	enriched := mergeSiblings(groups, siblings)

	// STEP C — ONE bulk node hydrate for every candidate of every group, enriched
	// or not. Never per candidate: the no-N+1 guarantee is why bulkHydratePeers
	// exists.
	candidateIDs := make([]string, 0, len(enriched))
	seenCandidate := make(map[string]bool, len(enriched))
	for i := range enriched {
		for j := range enriched[i].Members {
			if id := enriched[i].Members[j].ToId; id != "" && !seenCandidate[id] {
				seenCandidate[id] = true
				candidateIDs = append(candidateIDs, id)
			}
		}
	}
	nodes, herr := bulkHydratePeers(ctx, exec, candidateIDs, target)
	if herr != nil {
		return enriched, nil, herr
	}
	return enriched, nodes, nil
}

// mergeSiblings adopts, into each incomplete group, the sibling edges that share
// its source AND its group key.
//
// THE Evidence MATCH IS WHAT MAKES THIS EXACT rather than a same-source guess:
// one declaration commonly holds several ambiguous references, and they are
// distinguished only by their key. The pivot read returns edges in BOTH
// directions, so an edge is adopted only when the pivot is its FromId.
//
// Dedupe is by ToId, so an already-observed member is never counted twice.
func mergeSiblings(groups []CandidateGroup, siblings []knowledgev1.Edge) []CandidateGroup {
	if len(siblings) == 0 {
		return groups
	}
	out := make([]CandidateGroup, len(groups))
	copy(out, groups)

	for i := range out {
		g := &out[i]
		if g.Complete() {
			continue
		}
		have := make(map[string]bool, len(g.Members))
		for j := range g.Members {
			have[g.Members[j].ToId] = true
		}
		added := false
		for k := range siblings {
			s := &siblings[k]
			if s.FromId != g.FromID || s.Evidence != g.Key {
				continue
			}
			if have[s.ToId] {
				continue
			}
			have[s.ToId] = true
			g.Members = append(g.Members, copyGroupEdge(s))
			added = true
		}
		if added {
			sort.Slice(g.Members, func(a, b int) bool { return g.Members[a].ToId < g.Members[b].ToId })
		}
	}
	return out
}
