// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// codeRefMethod is the edge Method tag born-link writes on the
// thought--relates-to-->code-proxy edge (composeThoughtCreate). It is duplicated
// here as a literal because the canonical constant is unexported in package tools
// (code_referent_resolve.go); the in-package adjacency test already pins the same
// literal "code-ref", so the two stay in lockstep by convention.
const codeRefMethod = "code-ref"

// ResolveCitedCodeNodes resolves each thought's born-linked code referents to the
// HYDRATED code-graph nodes they point at, following the
// thought --relates-to(Method=code-ref)--> proxy --(repo + foreign_id)--> code node
// chain. It is the reusable cross-graph boundary deliberately returning the full
// hydrated nodes (UpdatedAt AND Content), not a derived scalar, so distinct
// consumers can read whatever facet they need off the same single resolution: the
// code-change staleness facet folds each node's UpdatedAt into a staleness signal
// (buildCitedCodeUpdatedAt below), while a verified-negation gate reads
// node.GetContent() off the SAME nodes to compare a thought against its cited
// source as it stands today. Exported because that gate lives in package tools,
// which imports package thought (no cycle: thought imports nothing from tools).
//
// It mirrors FetchSessionLabelsByThought (genre.go:87) step-for-step — a bulk
// edge read, group, bulk hydrate, fold — and is genuinely distinct only in that
// it filters a different edge Method and crosses into the CODE graph grouped by
// repo. Performance is first-class: AT MOST ONE edge read + ONE knowledge-graph
// proxy hydrate + ONE code-graph hydrate per DISTINCT cited repo (small N) — every
// read is a bulk ids[] read, NO N+1 over the cited nodes. Best-effort: any read
// error or empty stage yields an empty map (no flag, never a panic).
//
// src is the per-pass read memo. When it covers these thought ids the relates-to
// edges are served from the pass's ONE unified pivot-edge read and this issues NO
// edge read of its own; a nil src (the on-demand single-thought path) takes the
// narrow read exactly as before.
func ResolveCitedCodeNodes(ctx context.Context, gc Caller, thoughtIDs []string, src CorpusSource) map[string][]*knowledgev1.Node {
	out := map[string][]*knowledgev1.Node{}
	if gc == nil || len(thoughtIDs) == 0 {
		return out
	}

	// (1) Bulk edge read over the thought set, relates-to only — served from the
	// pass's unified pivot read when src covers these ids. Method survives the
	// decode (engine.EdgesFromProto copies e.GetMethod()), which is what lets
	// codeRefProxiesByThought below filter a wider input down to the born-links.
	edges, err := memoTypedEdges(ctx, gc, thoughtIDs, []kgtypes.EdgeType{kgtypes.EdgeRelatesTo}, src)
	if err != nil {
		return out
	}

	// (2) Keep only born-link code-ref edges whose From is an in-scope thought,
	// yielding each thought's proxy IDs + the distinct proxy-ID set.
	proxyIDsByThought, proxyIDs := codeRefProxiesByThought(edges, thoughtIDs)
	if len(proxyIDs) == 0 {
		return out
	}

	// (3) ONE bulk hydrate of the distinct proxy nodes (proxies live in the
	// KNOWLEDGE graph, IDs proxy:<repo>:<foreign_id>).
	proxyNodes := fetchNodesByIDs(ctx, gc, proxyIDs)

	// (4) Resolve each code proxy to its (repo, foreign_id) and group the foreign
	// IDs by repo for one code hydrate per repo.
	refByProxy, fidsByRepo := codeRefsFromProxies(proxyNodes)
	if len(refByProxy) == 0 {
		return out
	}

	// (5) ONE code-graph hydrate per DISTINCT repo (grouped, NOT per cited node).
	codeByRepoFID := make(map[string]map[string]*knowledgev1.Node, len(fidsByRepo))
	for repo, fids := range fidsByRepo {
		codeByRepoFID[repo] = fetchCodeNodesByIDs(ctx, gc, repo, fids)
	}

	// (6) Fold: for each thought, for each of its proxies that resolved to a
	// hydrated code node, append that node to the thought's slice.
	for tid, pids := range proxyIDsByThought {
		for _, pid := range pids {
			ref, ok := refByProxy[pid]
			if !ok {
				continue
			}
			if node, ok := codeByRepoFID[ref.repo][ref.fid]; ok && node != nil {
				out[tid] = append(out[tid], node)
			}
		}
	}
	return out
}

// MethodlessCodeCitations returns the CODE node IDs one thought points at through
// relates-to edges that carry NO code-ref Method — sorted and deduplicated, empty
// when there are none. It is the READ THAT NAMES WHAT ResolveCitedCodeNodes ABOVE
// THREW AWAY: codeRefProxiesByThought drops every method-less relates-to edge
// before resolution, so a symbol cited through mutate's links param (which mints a
// plain relates-to with no Method) never enters the citation set at all. The
// verified-negation gate consumes this to tell a rejected negator WHICH citations
// were excluded and why, instead of leaving them to infer it from a message that
// names none.
//
// A CITATION IS IDENTIFIED BY ITS TARGET, NOT BY THE EDGE ALONE, and that is the
// whole discrimination. The links param mints a method-less relates-to edge to
// WHATEVER it is handed, so a thought routinely carries them to sibling thoughts,
// findings and decisions; none of those is an attempt to cite code. So the target
// set is hydrated and passed through codeRefsFromProxies
// — the same filter the born-link path uses — which keeps only nodes that are CODE
// proxies (NodeProxy + foreign_graph=code + repo + foreign_id). What survives is an
// edge pointing at code whose only defect is the missing method.
//
// IT IS DELIBERATELY SEPARATE FROM ResolveCitedCodeNodes RATHER THAN FOLDED INTO
// IT. Widening that boundary's proxy hydrate to carry method-less targets would be
// one extra id per plain relates-to edge on a read the corpus-wide propagation loop
// issues for EVERY thought (buildCitedCodeUpdatedAt → blind spots), hydrating every
// links-param neighbor in the corpus to serve an error message. This
// function is called only when a negation has ALREADY been rejected — a cold,
// human-facing path — so its edge read plus proxy hydrate are paid there and
// nowhere else. Best-effort in the same style as its sibling: any read failure
// yields an empty result, never a flag and never a panic.
func MethodlessCodeCitations(ctx context.Context, gc Caller, thoughtID string) []string {
	if gc == nil || thoughtID == "" {
		return nil
	}
	// nil read-memo: a single-thought on-demand resolution with no propagation pass
	// in hand, exactly as resolveThoughtCurrentSource takes it.
	edges, err := memoTypedEdges(ctx, gc, []string{thoughtID}, []kgtypes.EdgeType{kgtypes.EdgeRelatesTo}, nil)
	if err != nil {
		return nil
	}
	proxyIDSet := map[string]bool{}
	for i := range edges {
		e := &edges[i]
		if kgtypes.EdgeType(e.Type) != kgtypes.EdgeRelatesTo || e.Method == codeRefMethod {
			continue
		}
		if e.FromId != thoughtID { // pollution guard, mirroring codeRefProxiesByThought.
			continue
		}
		proxyIDSet[e.ToId] = true
	}
	if len(proxyIDSet) == 0 {
		return nil
	}
	proxyIDs := make([]string, 0, len(proxyIDSet))
	for pid := range proxyIDSet {
		proxyIDs = append(proxyIDs, pid)
	}
	refByProxy, _ := codeRefsFromProxies(fetchNodesByIDs(ctx, gc, proxyIDs))
	fidSet := make(map[string]bool, len(refByProxy))
	for _, ref := range refByProxy {
		fidSet[ref.fid] = true
	}
	fids := make([]string, 0, len(fidSet))
	for fid := range fidSet {
		fids = append(fids, fid)
	}
	// SORTED FOR THE SAME REASON codeOrigins SORTS: the ids arrive in graph
	// edge-read order, which is not stable, and they are rendered into an error
	// message that tests assert over.
	sort.Strings(fids)
	return fids
}

// codeRef is a hydrated code proxy's pointer into the code graph: the repo and the
// foreign (code-node) ID the proxy stands in for.
type codeRef struct {
	repo string
	fid  string
}

// codeRefProxiesByThought filters the relates-to edge set to the born-link code-ref
// edges (Method=="code-ref") whose From is an in-scope thought — the pollution
// guard mirroring genre.go's idSet gate — and returns each thought's proxy IDs plus
// the deduplicated proxy-ID slice to bulk-hydrate.
func codeRefProxiesByThought(edges []knowledgev1.Edge, thoughtIDs []string) (map[string][]string, []string) {
	idSet := make(map[string]bool, len(thoughtIDs))
	for _, id := range thoughtIDs {
		idSet[id] = true
	}
	proxyIDsByThought := make(map[string][]string)
	proxyIDSet := map[string]bool{}
	for i := range edges {
		e := &edges[i]
		if kgtypes.EdgeType(e.Type) != kgtypes.EdgeRelatesTo || e.Method != codeRefMethod {
			continue
		}
		if !idSet[e.FromId] {
			continue
		}
		proxyIDsByThought[e.FromId] = append(proxyIDsByThought[e.FromId], e.ToId)
		proxyIDSet[e.ToId] = true
	}
	proxyIDs := make([]string, 0, len(proxyIDSet))
	for pid := range proxyIDSet {
		proxyIDs = append(proxyIDs, pid)
	}
	return proxyIDsByThought, proxyIDs
}

// codeRefsFromProxies reads each hydrated CODE proxy's (repo, foreign_id) and
// groups the foreign IDs by repo. foreign_graph + foreign_id are stamped by the
// PARENT BuildCrossGraphProxy (crossgraph/proxy_builder.go:38-43); repo by
// buildCodeProxy (:88). Non-code or incomplete proxies are skipped. Returns the
// proxyID→(repo,fid) map (for the rejoin fold) and the repo→[]foreign_id grouping
// (for the per-repo code hydrate).
func codeRefsFromProxies(proxyNodes map[string]*knowledgev1.Node) (map[string]codeRef, map[string][]string) {
	refByProxy := make(map[string]codeRef, len(proxyNodes))
	fidsByRepo := map[string][]string{}
	for pid, p := range proxyNodes {
		if p == nil || kgtypes.NodeType(p.Type) != kgtypes.NodeProxy {
			continue
		}
		if kgtypes.Value(p, "foreign_graph") != string(kgtypes.GraphCode) {
			continue
		}
		repo := kgtypes.Value(p, "repo")
		fid := kgtypes.Value(p, "foreign_id")
		if repo == "" || fid == "" {
			continue
		}
		refByProxy[pid] = codeRef{repo: repo, fid: fid}
		fidsByRepo[repo] = append(fidsByRepo[repo], fid)
	}
	return refByProxy, fidsByRepo
}

// buildCitedCodeUpdatedAt is the thin UpdatedAt→max fold over ResolveCitedCodeNodes,
// producing the thoughtID→newest-cited-code-UpdatedAt(nanos) map the blind-spots
// loop threads into blindSpotInputs.citedCodeUpdatedAt. It composes the cross-graph
// chain exactly ONCE (in ResolveCitedCodeNodes) and only folds here — no second
// resolution. A thought whose cited code all hydrated with a zero UpdatedAt (or
// that cites no resolvable code) is simply absent from the map (no flag).
func buildCitedCodeUpdatedAt(ctx context.Context, gc Caller, thoughtIDs []string, src CorpusSource) map[string]int64 {
	out := map[string]int64{}
	nodesByThought := ResolveCitedCodeNodes(ctx, gc, thoughtIDs, src)
	for tid, nodes := range nodesByThought {
		var newest int64
		for _, n := range nodes {
			if n != nil && n.UpdatedAt > newest {
				newest = n.UpdatedAt
			}
		}
		if newest > 0 {
			out[tid] = newest
		}
	}
	return out
}

// fetchCodeNodesByIDs hydrates code-graph nodes by ID in one Execute round-trip,
// the code-graph sibling of fetchNodesByIDs (wire.go) co-located with its sole
// caller, ResolveCitedCodeNodes. It marshals graph:code + repo into the query args
// so the engine's compileQuery routes the ids[] hydrate to the named code graph (a
// code-graph ids[] read falls through buildDefaultModePlan's ids[] arm; only
// id!="" / text!="" code reads are denied). A dedicated sibling is required
// because fetchNodesByIDs hardcodes the knowledge-graph target for its many
// callers; threading graph/repo through it would break all of them. The decoded
// Nodes are the value-embed wire protos verbatim (engine.DecodeNodes), so
// Node.UpdatedAt — the staleness signal — survives intact. Returns a map; missing
// IDs are absent.
func fetchCodeNodesByIDs(ctx context.Context, gc Caller, repo string, ids []string) map[string]*knowledgev1.Node {
	out := map[string]*knowledgev1.Node{}
	if gc == nil || repo == "" || len(ids) == 0 {
		return out
	}
	raw, err := json.Marshal(map[string]any{"ids": ids, "graph": "code", "repo": repo})
	if err != nil {
		slog.Warn("thought: fetchCodeNodesByIDs: marshal failed", "err", err)
		return out
	}
	resp, err := executeViaEngine(ctx, gc, "query", raw)
	if err != nil {
		slog.Warn("thought: fetchCodeNodesByIDs: execute failed", "err", err)
		return out
	}
	nodes, derr := engine.DecodeNodes(resp)
	if derr != nil {
		slog.Warn("thought: fetchCodeNodesByIDs: decode failed", "err", derr)
		return out
	}
	for _, n := range nodes {
		out[n.Id] = n
	}
	return out
}
