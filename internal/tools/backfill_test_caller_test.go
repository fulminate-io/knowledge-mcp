// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// backfillFakeCaller is an Execute-only GraphCaller for the origin think-path
// tests. Unlike the shared fakeGraphCaller (whose NodeType scan ignores
// limit/offset and so cannot catch the engine's limit:0 cap), this fake HONORS
// GetLimit()/GetOffset() on a type-browse — slicing a backing node slice exactly
// like the server's applyNodePage — so a test that seeds >page-size nodes proves
// the drain pages the whole corpus (and a reversion to a single capped browse
// goes red). It also serves the bulk EdgeKGContains read and records every
// mutation plan + drives the seeded create ids.
type backfillFakeCaller struct {
	thoughts []*knowledgev1.Node // served on a type=thought browse
	sessions []*knowledgev1.Node // served on a type=thought_session browse
	agents   []*knowledgev1.Node // served on a type=agent browse (origin resolution)
	edges    []*knowledgev1.Edge // served on the RETURN_MODE_EDGES bulk read

	mutateIDs          []string                    // ids returned for the NEXT create plan
	createQueue        [][]string                  // per-create id batches (FIFO); falls back to mutateIDs
	mutations          []*knowledgev1.MutationPlan // every recorded mutation plan
	browseCalls        int                         // count of type-browse Execute calls (any type)
	sessionBrowseCalls int                         // count of type=thought_session browse Execute calls
	edgeReads          int                         // count of RETURN_MODE_EDGES Execute calls

	// ignoreFieldPredicates models an OLD / third-party / predicate-BLIND server
	// that DROPS Selection.field_predicates and returns the unfiltered capped
	// page. When true, servePage skips the symbol_name-EQ pre-filter — the harness
	// for the resolver defense-in-depth guard test (the wrong-attachment hazard).
	// Default false: the fake HONORS field predicates like the embedded/cloud
	// executors (nodeMatchesField), returning only exact-name matches.
	ignoreFieldPredicates bool
}

func (c *backfillFakeCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	if m := req.GetMutation(); m != nil {
		c.mutations = append(c.mutations, m)
		ids := c.mutateIDs
		if len(c.createQueue) > 0 {
			ids = c.createQueue[0]
			c.createQueue = c.createQueue[1:]
		}
		return &knowledgev1.ExecuteResponse{Ids: ids, AffectedCount: int64(len(ids))}, nil
	}
	q := req.GetQuery()
	if q == nil {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		c.edgeReads++
		return &knowledgev1.ExecuteResponse{Edges: c.edges}, nil
	}
	switch q.GetSelection().GetNodeType() {
	case string(kgtypes.NodeThought):
		c.browseCalls++
		return c.servePage(c.thoughts, q), nil
	case string(kgtypes.NodeThoughtSession):
		c.browseCalls++
		c.sessionBrowseCalls++
		return c.servePage(c.sessions, q), nil
	case string(kgtypes.NodeAgent):
		c.browseCalls++
		return c.servePage(c.agents, q), nil
	default:
		return &knowledgev1.ExecuteResponse{}, nil
	}
}

// browseDefaultLimitCap mirrors the engine's browseDefaultLimit (compile_query.go):
// a limit<=0 browse is silently rewritten to this row cap on the client compile
// path. The fake replicates that rewrite so a source reversion to a single
// limit:0 browse (instead of the positive-limit offset drain) genuinely returns
// only the first 10 rows — driving the above-cap fails-when-absent assertion red.
const browseDefaultLimitCap = 10

// servePage slices the backing set by the request's offset/limit, mirroring BOTH
// the server's applyNodePage (positive limit → honest offset paging) AND the
// engine's limit<=0 → browseDefaultLimit cap (limit<=0 → at most 10 rows from
// offset 0, every later offset empty). The positive-limit drain bypasses the cap
// exactly as it does in production.
//
// BEFORE paging, it applies the request's symbol_name-EQ field predicate (the
// server-side WHERE the embedded/cloud executors render via nodeMatchesField),
// unless ignoreFieldPredicates models a predicate-blind server. A browse carrying
// no field predicates (the origin/think thought + agent browses) is unaffected,
// so the existing paging-dependent tests keep their full backing set.
func (c *backfillFakeCaller) servePage(all []*knowledgev1.Node, q *knowledgev1.QueryPlan) *knowledgev1.ExecuteResponse {
	if !c.ignoreFieldPredicates {
		all = filterBySymbolNameEQ(all, q.GetSelection().GetFieldPredicates())
	}
	offset := int(q.GetOffset())
	limit := int(q.GetLimit())
	if limit <= 0 {
		// Engine cap: limit<=0 → browseDefaultLimit rows from offset 0 only.
		if offset > 0 {
			return &knowledgev1.ExecuteResponse{}
		}
		end := min(browseDefaultLimitCap, len(all))
		return &knowledgev1.ExecuteResponse{Nodes: all[:end]}
	}
	if offset >= len(all) {
		return &knowledgev1.ExecuteResponse{}
	}
	end := min(offset+limit, len(all))
	return &knowledgev1.ExecuteResponse{Nodes: all[offset:end]}
}

// filterBySymbolNameEQ reproduces the server evaluator nodeMatchesField for the
// one predicate shape the session resolver emits: a {symbol_name, OP_EQ, v}
// FieldPredicate keeps only nodes whose SymbolName == v. Any other predicate
// shape (or none) leaves the set untouched, so non-session browses pass through.
func filterBySymbolNameEQ(all []*knowledgev1.Node, preds []*knowledgev1.FieldPredicate) []*knowledgev1.Node {
	for _, p := range preds {
		if p.GetField() != "symbol_name" || p.GetOp() != knowledgev1.MetadataPredicate_OP_EQ {
			continue
		}
		want := p.GetValue()
		filtered := make([]*knowledgev1.Node, 0, len(all))
		for _, n := range all {
			if n.GetSymbolName() == want {
				filtered = append(filtered, n)
			}
		}
		all = filtered
	}
	return all
}
