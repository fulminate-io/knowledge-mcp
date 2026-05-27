// SPDX-License-Identifier: Apache-2.0

// Package tools — cross_graph_migrate.go retires the legacy SLUG-LESS practice
// proxies (proxy:practice:<foreign_id>, no language metadata) the server's
// slug-less FROM convention (proxyFromLocatedGraph, routing.go Name="") once
// produced, re-keying each to the DECIDED-correct SLUG-FUL convention
// (proxy:practice:<slug>:<foreign_id>). The re-key re-points every incident edge
// onto the slug-ful proxy PRESERVING all edge metadata (via the Phase 0
// LinkOneWithMeta carrier), STRICTLY before deleting the slug-less node — so no
// edge dangles and no double-proxy survives.
//
// The migration is once-per-session + idempotent: a second run finds no
// slug-less proxies (they were re-keyed) and is a no-op.

package tools

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/crossgraph"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	"github.com/fulminate-io/knowledge-mcp/internal/projects/render"
)

// practiceProxyMigrationOnce gates the slug-less→slug-ful practice-proxy
// migration to ONCE PER CLIENT SESSION, mirroring the verified RepoResolver
// sync.Once precedent (repo_resolve.go:35-41,77 — the resolver's own once gates
// its pipeline_list_graphs RPC, so one-per-session is exactly what we want).
// There is NO persistent client done-flag in the tree, so the once-per-session
// gate IS the lifecycle: the scan + re-key chain is idempotent, so a second
// session re-scans, finds zero slug-less proxies (re-keyed in the first), and
// no-ops. Recurring cost is one practice-narrowed proxy scan Execute per session.
var practiceProxyMigrationOnce sync.Once

// migratePracticeProxiesOnce fires migratePracticeProxies at most once per client
// session via practiceProxyMigrationOnce. It is invoked lazily from the
// cross-graph-link composer's first confirmed cross-graph link — the same
// lazy-on-first-use shape RepoResolver uses (fire on first ResolveCwd). A
// migration error is logged (best-effort) but never blocks the link the caller
// is composing.
func migratePracticeProxiesOnce(ctx context.Context, gc GraphCaller) {
	if gc == nil {
		return
	}
	practiceProxyMigrationOnce.Do(func() {
		migrated, err := migratePracticeProxies(ctx, gc)
		if err != nil {
			slog.Warn("practice-proxy migration: best-effort run failed", "error", err)
			return
		}
		if migrated > 0 {
			slog.Info("practice-proxy migration: re-keyed slug-less proxies to slug-ful", "migrated", migrated)
		}
	})
}

// scanSlugLessPracticeProxies returns every SLUG-LESS practice proxy in the
// knowledge graph. It issues ONE Match(NodeProxy) Execute carrying a server-side
// MetadataPredicate{foreign_graph OP_EQ practice} so only practice proxies come
// back (no over-fetch of code/cloud/cicd/linkage proxies — the predicate lowers
// to a server-side Meta("foreign_graph","practice") equality filter via
// applyMetadataPredicates), then filters the narrowed set client-side to the
// slug-less discriminant.
//
// The slug-less shape (buildPracticeProxy with Name=="") is
// proxy:practice:<foreign_id> with NO language metadata; the slug-ful shape is
// proxy:practice:<slug>:<foreign_id> WITH language set. The exact-ID-equality
// test (id == "proxy:practice:"+foreign_id) plus the empty-language guard
// uniquely selects the slug-less proxies and never a slug-ful one.
func scanSlugLessPracticeProxies(ctx context.Context, gc GraphCaller) ([]*knowledgev1.Node, error) {
	ex, err := persistExecutor(gc)
	if err != nil {
		return nil, err
	}
	resp, err := ex.Execute(ctx, &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
			Selection: &knowledgev1.Selection{
				NodeType: string(kgtypes.NodeProxy),
				MetadataPredicates: []*knowledgev1.MetadataPredicate{{
					Key:   "foreign_graph",
					Op:    knowledgev1.MetadataPredicate_OP_EQ,
					Value: "practice",
				}},
			},
		}},
		Target: &knowledgev1.GraphSelector{Graph: "knowledge"},
	})
	if err != nil {
		return nil, fmt.Errorf("scan slug-less practice proxies: %w", err)
	}
	proxies, derr := engine.DecodeNodes(resp)
	if derr != nil {
		return nil, fmt.Errorf("scan slug-less practice proxies: decode: %w", derr)
	}
	out := make([]*knowledgev1.Node, 0, len(proxies))
	for _, n := range proxies {
		foreignID := kgtypes.Value(n, "foreign_id")
		if foreignID == "" {
			continue
		}
		// Defensive re-check of foreign_graph (the server predicate already
		// guarantees it) + the slug-less discriminant: exact id equality + empty
		// language. A slug-ful proxy has id proxy:practice:<slug>:<foreign_id> with
		// language set, so it never matches.
		if kgtypes.Value(n, "foreign_graph") != "practice" {
			continue
		}
		if kgtypes.Value(n, "language") != "" {
			continue
		}
		if n.Id != "proxy:practice:"+foreignID {
			continue
		}
		out = append(out, n)
	}
	return out, nil
}

// migratePracticeProxies is the migration entry point. It scans for slug-less
// practice proxies and re-keys each to the slug-ful convention, preserving edge
// metadata. It fetches the foreign graph list ONCE (4 per-type
// RETURN_MODE_GRAPH_NAMES reads via crossgraph.ListForeignGraphs) and reuses it
// across every proxy. Returns the count of proxies migrated.
//
// A proxy whose practice target can no longer be located is a NON-FATAL skip
// (logged + not counted, the migration continues); a genuine store/Execute error
// from a re-key is surfaced (returned), aborting the run loudly.
func migratePracticeProxies(ctx context.Context, gc GraphCaller) (int, error) {
	ex, err := persistExecutor(gc)
	if err != nil {
		return 0, nil //nolint:nilerr // no Execute seam → nothing to migrate
	}
	slugLess, err := scanSlugLessPracticeProxies(ctx, gc)
	if err != nil {
		return 0, err
	}
	if len(slugLess) == 0 {
		return 0, nil
	}
	graphs, err := crossgraph.ListForeignGraphs(ctx, ex)
	if err != nil {
		return 0, fmt.Errorf("migrate practice proxies: list foreign graphs: %w", err)
	}
	migrated := 0
	for _, proxy := range slugLess {
		ok, rerr := rekeySlugLessPracticeProxy(ctx, gc, ex, graphs, proxy)
		if rerr != nil {
			return migrated, fmt.Errorf("migrate practice proxy %s: %w", proxy.Id, rerr)
		}
		if !ok {
			slog.Debug("migrate practice proxies: skipped (target not locatable)", "proxy", proxy.Id)
			continue
		}
		migrated++
	}
	return migrated, nil
}

// rekeySlugLessPracticeProxy re-keys one slug-less practice proxy to the slug-ful
// convention, preserving every incident edge AND its metadata. Returns
// (true, nil) on a successful re-key, (false, nil) for a NON-FATAL skip (the
// practice target is no longer locatable — re-pointing has nowhere to land, so
// the slug-less proxy is left untouched), or (false, err) for a genuine
// store/Execute error.
//
// Order is load-bearing: read incident edges → create slug-ful proxy → re-point
// every edge (metadata-preserving, via the Phase 0 LinkOneWithMeta carrier) →
// DELETE the slug-less proxy. Re-point STRICTLY before delete so no edge dangles.
func rekeySlugLessPracticeProxy(ctx context.Context, gc GraphCaller, ex render.Executor, graphs []crossgraph.ForeignGraph, slugLess *knowledgev1.Node) (bool, error) {
	foreignID := kgtypes.Value(slugLess, "foreign_id")
	if foreignID == "" {
		return false, nil // not a recoverable practice proxy — skip defensively.
	}

	// (1) Recover the slug: locate the foreign practice node by its foreign_id.
	// The located practice graph's name IS the slug. A foreign_graph=practice
	// proxy's foreign_id only resolves in a practice graph, so the practice-only
	// restriction is automatic. No hit → the target is gone → non-fatal skip
	// (deleting would lose the edge data with nowhere to re-point).
	gt, slug, practiceNode, found := crossgraph.LocateForeignNode(ctx, gc, graphs, foreignID)
	if !found || gt != kgtypes.GraphPractice {
		return false, nil
	}

	// (2) Read the incident edges FIRST, both directions, WITH metadata
	// (render.IterEdges decodes the full edges_json carrier — Weight/Confidence/
	// Method/Evidence/LastValidated are present on read).
	edges, eerr := render.IterEdges(ctx, gc, slugLess.Id, kgwire.BothEdges)
	if eerr != nil {
		return false, fmt.Errorf("read incident edges: %w", eerr)
	}

	// (3) Create the slug-ful proxy (idempotent UPSERT) — id is
	// proxy:practice:<slug>:<foreign_id>, the DECIDED-correct convention.
	slugFul, uerr := crossgraph.UpsertForeignProxy(ctx, ex, "knowledge", kgtypes.GraphPractice, slug, foreignID, practiceNode)
	if uerr != nil {
		return false, fmt.Errorf("create slug-ful proxy: %w", uerr)
	}

	// (4) Re-point every incident edge onto the slug-ful proxy, PRESERVING all 5
	// metadata fields via LinkOneWithMeta (the Phase 0 carrier). Copy the edge and
	// swap ONLY the slug-less endpoint for slugFul.Id; Type + metadata ride along.
	for _, e := range edges {
		// Build a fresh edge literal with the slug-less endpoint swapped for
		// slugFul.Id (copying an existing knowledgev1.Edge value is copylocks-forbidden
		// after the proto value-embed flip; e is a *knowledgev1.Edge into the decoded
		// backing array). Type + all 5 metadata fields ride along verbatim.
		fromID, toID := e.FromId, e.ToId
		switch {
		case e.FromId == slugLess.Id && e.ToId == slugLess.Id:
			continue // self-edge on the slug-less proxy — defensive skip.
		case e.FromId == slugLess.Id:
			fromID = slugFul.Id
		case e.ToId == slugLess.Id:
			toID = slugFul.Id
		default:
			continue // neither endpoint is the slug-less id — defensive skip.
		}
		re := &knowledgev1.Edge{
			FromId:        fromID,
			ToId:          toID,
			Type:          e.Type,
			Weight:        e.Weight,
			Confidence:    e.Confidence,
			Method:        e.Method,
			Evidence:      e.Evidence,
			LastValidated: e.LastValidated,
		}
		if lerr := LinkOneWithMeta(ctx, gc, re); lerr != nil {
			return false, fmt.Errorf("re-point edge %s->%s: %w", re.FromId, re.ToId, lerr)
		}
	}

	// (5) DELETE the slug-less proxy — STRICTLY after every edge is re-pointed.
	// The by-id DELETE removes the node and its own (now-superseded) edges only.
	if _, derr := ex.Execute(ctx, &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Mutation{Mutation: &knowledgev1.MutationPlan{
			Kind:      knowledgev1.MutationPlan_MUTATION_KIND_DELETE,
			Selection: &knowledgev1.Selection{Ids: []string{slugLess.Id}},
		}},
		Target: &knowledgev1.GraphSelector{Graph: "knowledge"},
	}); derr != nil {
		return false, fmt.Errorf("delete slug-less proxy: %w", derr)
	}
	return true, nil
}
