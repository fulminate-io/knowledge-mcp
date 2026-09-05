// SPDX-License-Identifier: Apache-2.0

package foundation

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/paging"
)

// wire_nodes_by_ids.go holds the ONE by-ids bulk hydrate in this binary. Three
// copies of it existed before this file: projects/render's exported pair,
// thought's package-local pair, and nothing in foundation — the lower layer all
// three consumers already sit above. The copies are gone; render and the
// context-pack composer call this, and thought keeps only a one-return
// delegating adapter over it because fifteen in-package callers use that
// convention.
//
// IT IS A SEPARATE FILE FROM wire.go DELIBERATELY. wire.go is 434 lines and
// lefthook's commit gate refuses a Go file over 500, so folding this in would
// have landed it at roughly 507 and failed at commit rather than at authoring.
//
// The behaviour is render's, which was the strongest of the three: paged at the
// shared page size, tombstones included, a per-page not-found tolerated, and the
// truncation verdict RETURNED rather than swallowed.

// TombstoneVisibility says whether a by-ids hydrate returns rows the server has
// tombstoned. It is a named type rather than a bare bool so a call site reads as
// a policy statement instead of a positional literal.
type TombstoneVisibility bool

const (
	// ExcludeTombstones drops deleted rows, which is what a caller wants when a
	// deleted node should behave as though it is gone.
	ExcludeTombstones TombstoneVisibility = false
	// IncludeTombstones returns deleted rows, which is what a caller wants when
	// it must still render or account for a node an edge still points at.
	IncludeTombstones TombstoneVisibility = true
)

// FetchNodesByIDs hydrates many nodes in one bounded paged read, replacing a
// per-id FetchNodeByID loop. It returns the nodes keyed by id, the read's
// truncation verdict, and any error. An empty graphType targets the
// knowledge/default graph.
//
// PAGED, AND THE SECOND RETURN IS WHY. The server flags truncation off the
// REQUEST — an id list longer than its row ceiling is clamped before any row is
// read — so an UNPAGED bulk hydrate of a large id set comes back short with the
// missing nodes silently absent. Draining in pages of paging.BrowsePageSize
// keeps every request under that ceiling, and paging at the shared constant
// rather than a hand-carried copy of the server's private ceiling is what keeps
// a second stamper of that number from drifting against it.
//
// THE VERDICT IS REPORTED, NOT ACTED ON, and that split is the point. A renderer
// legitimately shows a short list as short, so erroring inside this helper would
// regress its callers; a guard that must never ship over a resident it failed to
// see legitimately treats the same verdict as fatal. The helper reports, the
// caller decides.
//
// The thought copy this replaced did neither: it issued ONE unpaged Execute and
// dropped the verdict, so any id set over the row ceiling returned short with no
// error and no counter. Consolidating repairs that path as a side effect.
//
// TOMBSTONE VISIBILITY IS THE CALLER'S, for the same reason the truncation
// verdict is. The server drops tombstoned rows from a by-ids read unless the
// plan asks for them, and the two consumers of this helper genuinely want
// opposite answers: the project renderers show a deleted node so a dangling
// edge renders as something rather than vanishing, while the thought package
// treats a deleted node as gone and its corpus cache actively evicts one. A
// constant baked in here would silently impose one package's policy on the
// other — which is exactly what an earlier revision of this file did, turning
// deleted thoughts into live recall results.
//
// So visibility is a PARAMETER, spelled as a named pair rather than a bare bool.
// Fifteen call sites read it today, thirteen including and two excluding, and
// `foundation.ExcludeTombstones` at a call site states the policy where a bare
// `false` would state only a position.
//
// A REQUESTED ID MISSING FROM THE RESULT MAP MEANS THE NODE WAS NOT FOUND, the
// same thing the single-node fetch expresses with (nil, false). A per-page
// NotFound from the transport is tolerated the same way, because one page
// resolving to nothing is an absence rather than a failed read.
func FetchNodesByIDs(
	ctx context.Context,
	caller GraphCaller,
	graphType kgtypes.GraphType,
	name string,
	ids []string,
	tombstones TombstoneVisibility,
) (map[string]*knowledgev1.Node, bool, error) {
	if caller == nil || len(ids) == 0 {
		return map[string]*knowledgev1.Node{}, false, nil
	}
	out := make(map[string]*knowledgev1.Node, len(ids))
	truncated := false
	for start := 0; start < len(ids); start += paging.BrowsePageSize {
		end := min(start+paging.BrowsePageSize, len(ids))
		resp, err := caller.Execute(ctx, &knowledgev1.ExecuteRequest{
			Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
				Ids:               ids[start:end],
				IncludeTombstones: bool(tombstones),
			}},
			Target: graphTarget(graphType, name),
		})
		if err != nil {
			var ce *connect.Error
			if errors.As(err, &ce) && ce.Code() == connect.CodeNotFound {
				continue
			}
			return nil, false, fmt.Errorf("topology/wire: fetch nodes by ids: %w", err)
		}
		if resp.GetTruncated() {
			truncated = true
		}
		nodes, derr := engine.DecodeNodes(resp)
		if derr != nil {
			return nil, false, fmt.Errorf("topology/wire: decode nodes by ids: %w", derr)
		}
		for _, n := range nodes {
			if n == nil || n.Id == "" {
				continue
			}
			out[n.Id] = n
		}
	}
	return out, truncated, nil
}
