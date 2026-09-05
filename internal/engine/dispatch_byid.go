// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"context"
	"encoding/json"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	"github.com/fulminate-io/knowledge-mcp/internal/paging"
)

// dispatch_byid.go holds the client-side composition of the
// query(id, include_edges) / query(id, include_cross_links) reads. The engine
// does not absorb those carriers, so the client orchestrates the edge-summary +
// cross-link sections over generic Execute calls, every one of them bounded and
// none of them a per-peer N+1. Split into a sibling file so dispatch.go stays
// under the 500-line cap.

// dispatchQueryByID intercepts a query(id) read that carries include_edges
// and/or include_cross_links — the absorption shapes the engine does not
// serve. It composes the requested sections client-side and renders the node
// with them, returning handled=true. For any other query shape (no id, or id
// with neither flag) it returns handled=false so Dispatch proceeds to the
// generic Compile/exec/Render flow unchanged.
//
// BOUNDEDNESS (the load-bearing invariant): every read the orchestration issues
// carries an explicit bound, and none of them fans out per peer:
//   - base node read: one by-id read
//   - include_edges: the pivot edge read in bounded pages + ONE bulk ids[] peer
//     hydrate — NOT a per-peer round-trip
//   - include_cross_links: a by-id linkage proxy lookup + a paged foreign_id
//     browse + per-proxy paged edge reads + ONE bulk peer hydrate
func dispatchQueryByID(ctx context.Context, exec ExecuteFn, args json.RawMessage) (kgtools.ToolResult, bool) {
	var a queryArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return kgtools.ToolResult{}, false // malformed → let the generic flow surface it.
	}
	// Only the default-mode by-id read with an absorption flag is intercepted.
	// A code/logs graph, a SPECIALIZED mode, or a thought-filter shape stays on
	// its existing path (those never carried include_edges anyway).
	if a.ID == "" || a.Mode != "" || isCodeGraph(a.Graph) || a.Graph == "logs" {
		return kgtools.ToolResult{}, false
	}
	wantEdges := boolPtr(a.IncludeEdges)
	wantCrossLinks := boolPtr(a.IncludeCrossLinks)
	if !wantEdges && !wantCrossLinks {
		return kgtools.ToolResult{}, false // plain by-id read → generic flow.
	}

	label := queryGraphLabelFor(a)
	isKnowledge := a.Graph == "" || a.Graph == "knowledge"
	target := buildTarget(a.Graph, a.Repo, a.Account, a.Name, a.Language, a.Branch)

	// (1) Base node read — confirm the node exists and get the render base.
	node, found, err := byIDNodeRead(ctx, exec, a.ID, a.IncludeTombstones, target)
	if err != nil {
		return renderEngineError(err), true
	}
	if !found {
		return errorResult(nodeNotFoundMsg(a.ID, label)), true
	}

	// Edge summary (include_edges): paged pivot edge read + ONE bulk peer hydrate.
	// truncated is the HYDRATE's verdict; the edge drain itself is complete-or-loud.
	truncated := false
	var edges []nodeEdgeInfo
	if wantEdges {
		var edgesTruncated bool
		edges, edgesTruncated, err = composeEdgeSummary(ctx, exec, a.ID, a.IncludeTombstones, target)
		if err != nil {
			return renderEngineError(err), true
		}
		truncated = truncated || edgesTruncated
	}

	// Cross-links (include_cross_links): generic linkage-graph queries.
	var links []crossLink
	if wantCrossLinks {
		var linksTruncated bool
		links, linksTruncated, err = composeCrossLinks(ctx, exec, a.ID)
		if err != nil {
			return renderEngineError(err), true
		}
		truncated = truncated || linksTruncated
	}

	// THE DISCLOSURE IS THE PEER HYDRATES', not the edge drains'. Dispatch returns
	// this result BEFORE Compile, so nothing here reaches engine.Render. The base
	// node read cannot truncate (one row against a 10,000-row ceiling) and both
	// edge drains are complete-or-loud, but each composition ends in ONE unbounded
	// QueryPlan{Ids} over a peer set the drain can make arbitrarily large, and the
	// server clamps an id set above 10,000 on the request alone. A clamped hydrate
	// renders edge peers under their id-prefix fallback name and DROPS cross-link
	// rows outright, both indistinguishable from the honest absence — so the arm
	// discloses for itself.
	//
	// THE ROW COUNT IS BOTH SECTIONS, not the edge list. Either flag can raise the
	// verdict independently, and an include_cross_links-only read has no edges at
	// all — counting len(edges) there rendered "Showing 0 rows — the server row
	// ceiling engaged", a correct disclosure attached to a number that contradicts
	// it.
	//
	// THE FORMAT AND PROJECTION CHOICES ARE renderByIDResult's, not this function's:
	// this file is at the 500-line cap, and the shape rules belong beside the
	// renderers they describe. a.Format and a.Fields are both THREADED rather than
	// dropped, and EACH WAS DROPPED ONCE: format:"json" used to return prose, and
	// `fields` was honored WITHOUT include_edges and ignored WITH it.
	rows := len(edges) + len(links)
	return WithTruncationNoticeFor(
		renderByIDResult(node, label, isKnowledge, a.Format, a.Fields, a.IncludeTombstones, edges, links, truncated),
		truncated, rows), true
}

// byIDNodeRead issues ONE Execute for the bare by-id node (RETURN_MODE_NODES)
// and returns the resolved node + found flag. A NotFound engine error maps to
// found=false (the renderer surfaces the not-found message); any other error is
// returned.
func byIDNodeRead(ctx context.Context, exec ExecuteFn, id string, includeTombstones bool, target *knowledgev1.GraphSelector) (*knowledgev1.Node, bool, error) {
	p := &knowledgev1.QueryPlan{ById: id}
	applyTombstones(p, includeTombstones)
	resp, err := exec(ctx, &knowledgev1.ExecuteRequest{
		Plan:   &knowledgev1.ExecuteRequest_Query{Query: p},
		Target: target,
	})
	if err != nil {
		// A by-id miss returns CodeNotFound; treat it as not-found rather than a
		// hard error so the renderer surfaces the same not-found message the
		// plain by-id path does.
		if isNotFound(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	nodes, derr := decodeNodes(resp)
	if derr != nil {
		return nil, false, derr
	}
	if len(nodes) == 0 {
		return nil, false, nil
	}
	return nodes[0], true, nil
}

// composeEdgeSummary reads the node's raw edges through the bounded pivot drain
// and hydrates their peers in ONE bulk ids[] read, shaping the result into the
// []nodeEdgeInfo the render_node.go renderers consume. This replaces the
// server's since-removed per-peer N+1 edge-info build: the peer
// hydrate is a single call whatever the peer count, and the edge read is paged
// rather than per-peer. Returns nil (the renderer omits the Edges section) when
// the node has no edges.
//
// A node whose edge count exceeds the drain's per-page ceiling cannot be served
// completely, and paging.DrainPivotEdges errors by name rather than returning a short
// summary — the by-id render must not present a silent sample as the whole.
//
// THE EDGE READ IS COMPLETE OR THE CALL FAILS, so it contributes no truncation
// verdict. pivotEdgePage threads the server's truncated flag out of every page,
// but paging.DrainPivotEdges never ACCEPTS a saturated page: it halves the pivot
// set, then re-reads a single pivot as a from_id band tiling, splits a saturating
// band at its median interior id, and only a pivot no band can divide returns an
// error naming the pivot and the ceiling. Its sibling DrainBandedEdges states the
// invariant outright — "A short union is never returned." Capturing an
// intermediate page's flag and reporting it as "this result may be incomplete"
// would be a FALSE statement about a provably complete union.
//
// THE PEER HYDRATE IS A DIFFERENT READ AND IT IS NOT COMPLETE-OR-LOUD. It is one
// unbounded QueryPlan{Ids} over the drained union's peer set, and the server
// clamps an id set above 10,000 on the request alone — see bulkHydratePeers. Its
// verdict is the bool this function returns, and it is the ONLY truncation this
// arm can suffer. An earlier revision of this comment certified the whole
// composition complete-or-loud on the strength of the edge read alone; that was
// true of the edges and false of the summary.
func composeEdgeSummary(ctx context.Context, exec ExecuteFn, id string, includeTombstones bool, target *knowledgev1.GraphSelector) ([]nodeEdgeInfo, bool, error) {
	if id == "" {
		// An edges plan with no pivot means "every edge of the graph" — never what
		// an empty by-id summary wants.
		return nil, false, nil
	}
	// (2) RETURN_MODE_EDGES: raw []knowledgev1.Edge for the pivot, both
	// directions (Forward unset → BothEdges in collectEdgesForReturnMode), read
	// in bounded pages.
	rawEdges, err := paging.DrainPivotEdges([]string{id}, paging.EdgePivotPageSize, CorrelationsEdgeScanCap,
		func(idPage []string, fromIDGte, fromIDLt string) ([]knowledgev1.Edge, bool, error) {
			return pivotEdgePage(ctx, exec, idPage, paging.EdgeFromBandOrNil(fromIDGte, fromIDLt), includeTombstones, target)
		})
	if err != nil {
		return nil, false, err
	}
	if len(rawEdges) == 0 {
		return nil, false, nil
	}

	// Shape []knowledgev1.Edge → []nodeEdgeInfo (peer-name unresolved yet). For an
	// edge whose FromID is the queried node, the peer is ToID (outgoing); else
	// the peer is FromID (incoming) — mirroring CollectNodeEdges semantics.
	infos := make([]nodeEdgeInfo, len(rawEdges))
	peerSet := make(map[string]struct{}, len(rawEdges))
	for i := range rawEdges {
		e := &rawEdges[i]
		peerID, direction := e.ToId, "outgoing"
		if e.FromId != id {
			peerID, direction = e.FromId, "incoming"
		}
		infos[i] = nodeEdgeInfo{
			PeerID:       peerID,
			Relationship: e.Type,
			Direction:    direction,
		}
		peerSet[peerID] = struct{}{}
	}

	// (3) ONE bulk ids[] peer hydrate → resolve PeerName + PeerType for every
	// peer in a single Execute (the no-N+1 guarantee).
	peerIDs := make([]string, 0, len(peerSet))
	for pid := range peerSet {
		peerIDs = append(peerIDs, pid)
	}
	peers, peersTruncated, err := bulkHydratePeers(ctx, exec, peerIDs, target)
	if err != nil {
		return nil, false, err
	}
	for i := range infos {
		if peer, ok := peers[infos[i].PeerID]; ok {
			infos[i].PeerName = peer.SymbolName
			infos[i].PeerType = peer.Type
		}
		// Fallback peer name carries forward the server's rendering: a
		// truncated id when the peer has no SymbolName / was not resolved.
		if infos[i].PeerName == "" {
			pid := infos[i].PeerID
			infos[i].PeerName = pid[:min(12, len(pid))]
		}
	}
	return infos, peersTruncated, nil
}

// pivotEdgePage issues ONE bounded RETURN_MODE_EDGES read over a page of pivot
// ids for the drains above. The plan Limit and the drain's edgeCap are the same
// number twice on purpose: the Limit is what the server enforces, the cap is
// what the drain uses to notice it was enforced. One without the other yields a
// drain that never detects truncation, or one that splits on a threshold nobody
// applies.
//
// THE BAND ARRIVES ALREADY CONSTRUCTED, as a parameter, because this function is
// where the three forwarding closures' plan is actually built. That makes it the
// one place an intermediate name is correct: its callers construct the band with
// paging.EdgeFromBandOrNil in argument position, and it assigns what it was given.
// A nil band is the ordinary unbanded page and MUST stay nil — the server refuses
// a non-nil band alongside two or more pivots, and an ordinary page carries up to
// paging.EdgePivotPageSize of them.
//
// The second return is the response's truncated flag, threaded verbatim from
// resp.GetTruncated() and never derived from len(): the server can drop rows
// between its scan and the count it returns, so a saturated read can come back
// short and a length test alone would be blind to it.
func pivotEdgePage(
	ctx context.Context,
	exec ExecuteFn,
	idPage []string,
	band *knowledgev1.EdgeFromBand,
	includeTombstones bool,
	target *knowledgev1.GraphSelector,
) ([]knowledgev1.Edge, bool, error) {
	plan := &knowledgev1.QueryPlan{
		Ids:          idPage,
		ReturnMode:   knowledgev1.ReturnMode_RETURN_MODE_EDGES,
		Limit:        int32(CorrelationsEdgeScanCap),
		EdgeFromBand: band,
	}
	applyTombstones(plan, includeTombstones)
	resp, err := exec(ctx, &knowledgev1.ExecuteRequest{
		Plan:   &knowledgev1.ExecuteRequest_Query{Query: plan},
		Target: target,
	})
	if err != nil {
		return nil, false, err
	}
	edges, derr := decodeEdgesRaw(resp)
	return edges, resp.GetTruncated(), derr
}

// bulkHydratePeers issues ONE Execute (QueryPlan{Ids: peerIDs} → []*knowledgev1.Node)
// and returns a peerID→Node map. An empty id set is a no-op (no Execute, empty
// map). This is the single bulk read that replaces the per-peer N+1.
//
// THE SECOND RETURN IS THE SERVER'S TRUNCATION VERDICT, and it is REAL on this
// read rather than a formality. The plan carries NO Limit, and the id set is
// unbounded by construction: it is the peer set of a drained edge union, and the
// drain's band split exists precisely to serve a pivot whose edge count exceeds
// the 50,000-row edge cap. executeTruncation flags
// `len(p.GetIds()) > maxExecuteNodeRows` (10,000) on the REQUEST alone, so a
// hot node's peer set is clamped and the server returns at most 10,000 rows.
// Every unreturned peer then renders under the truncated-id fallback name,
// indistinguishable from a peer that genuinely has no SymbolName — which is the
// silent narrowing this whole read path exists to stop.
func bulkHydratePeers(ctx context.Context, exec ExecuteFn, peerIDs []string, target *knowledgev1.GraphSelector) (map[string]*knowledgev1.Node, bool, error) {
	if len(peerIDs) == 0 {
		return map[string]*knowledgev1.Node{}, false, nil
	}
	resp, err := exec(ctx, &knowledgev1.ExecuteRequest{
		Plan:   &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{Ids: peerIDs}},
		Target: target,
	})
	if err != nil {
		return nil, false, err
	}
	nodes, derr := decodeNodes(resp)
	if derr != nil {
		return nil, false, derr
	}
	out := make(map[string]*knowledgev1.Node, len(nodes))
	for _, n := range nodes {
		out[n.Id] = n
	}
	return out, resp.GetTruncated(), nil
}

// linkageTarget is the GraphSelector for the linkage graph — the second DB the
// cross-link composition queries, mirroring the legacy ResolveLinkageGraph.
var linkageTarget = &knowledgev1.GraphSelector{Graph: "linkage"}

// composeCrossLinks reproduces the server-side cross-link collection (proxy
// lookup then per-proxy cross-link gather, both since removed) using
// GENERIC Execute primitives against the linkage graph:
//
//   - FindLinkageProxies: (1) a by-id read against linkage — if the node IS a
//     proxy (deterministic-ID O(1) path), it is the match; (2) else a foreign_id
//     OP_EQ metadata-predicate BROWSE for proxy nodes, DRAINED in keyset pages —
//     this REPLACES the server's Match(NodeProxy).Limit(0) full-scan + in-memory
//     filter with a predicate-pushed browse (a strict improvement).
//   - CollectProxyCrossLinks: per proxy, a paged RETURN_MODE_EDGES read for the
//     proxy's edges, then ONE bulk peer hydrate (no per-peer N+1), building
//     []crossLink with proxyInfoWire(peer) for the peer's graph label.
//
// An absent linkage graph yields no rows (NOT an error) — mirroring the legacy
// collectCrossLinks degrade-to-empty (a node with no cross-graph proxies simply
// has none). The by-id lookup asks for one node, the foreign_id browse and the
// per-proxy edge reads drain bounded pages, and the peer hydrate is one bulk
// ids[] read rather than a per-peer round trip. The proxy count is low single
// digits in practice.
func composeCrossLinks(ctx context.Context, exec ExecuteFn, nodeID string) ([]crossLink, bool, error) {
	proxies, err := findLinkageProxies(ctx, exec, nodeID)
	if err != nil {
		return nil, false, nil //nolint:nilerr // absent/empty linkage graph = no cross-links, not an error (mirrors legacy collectCrossLinks)
	}
	var links []crossLink
	truncated := false
	for _, proxy := range proxies {
		pls, pTruncated, perr := collectProxyCrossLinks(ctx, exec, proxy)
		if perr != nil {
			return nil, false, perr
		}
		// OR across proxies: any clamped peer hydrate drops cross-link rows, and a
		// later whole proxy does not restore them.
		truncated = truncated || pTruncated
		links = append(links, pls...)
	}
	return links, truncated, nil
}

// findLinkageProxies resolves the linkage proxies referencing nodeID via the two
// generic reads described on composeCrossLinks. Returns an empty slice (no
// error) when the linkage graph is absent or carries no matching proxy.
func findLinkageProxies(ctx context.Context, exec ExecuteFn, nodeID string) ([]*knowledgev1.Node, error) {
	// (1) Deterministic-ID O(1) path: the node ID may itself be a proxy ID.
	byIDResp, err := exec(ctx, &knowledgev1.ExecuteRequest{
		Plan:   &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{ById: nodeID}},
		Target: linkageTarget,
	})
	if err == nil {
		nodes, derr := decodeNodes(byIDResp)
		if derr == nil && len(nodes) > 0 && kgtypes.NodeType(nodes[0].Type) == kgtypes.NodeProxy {
			return []*knowledgev1.Node{nodes[0]}, nil
		}
	} else if !isNotFound(err) {
		// A non-NotFound error (e.g. the linkage graph does not exist) → no
		// cross-links to surface (degrade to empty, like the legacy resolver).
		return nil, nil //nolint:nilerr // absent linkage graph = no proxies
	}

	// (2) foreign_id OP_EQ browse for proxy nodes — replaces the server full-scan.
	// DRAINED in keyset pages: every proxy referencing the node is a cross-link
	// section the render must carry, and the proxy count is bounded only by how
	// many foreign graphs hold a proxy for that node (one per indexed repo or
	// account), so a single bounded read could cut the section short.
	// A DECODE failure is a real error and stays one; only a failed READ degrades
	// to empty. The drain collapses both into one return value, so the decode
	// error is carried out separately rather than silently becoming "no proxies".
	var decodeErr error
	proxies, err := paging.DrainKeysetPages(func(afterID string) ([]*knowledgev1.Node, error) {
		cursor := afterID
		browseResp, rerr := exec(ctx, &knowledgev1.ExecuteRequest{
			Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
				Selection: &knowledgev1.Selection{
					NodeType: string(kgtypes.NodeProxy),
					MetadataPredicates: []*knowledgev1.MetadataPredicate{
						{Key: "foreign_id", Op: knowledgev1.MetadataPredicate_OP_EQ, Value: nodeID},
					},
				},
				Limit: int32(paging.BrowsePageSize),
				// SET on every page including the first, where the value is empty:
				// presence is what selects the keyset browse.
				AfterId:   &cursor,
				SkipTotal: true,
			}},
			Target: linkageTarget,
		})
		if rerr != nil {
			return nil, rerr
		}
		page, derr := decodeNodes(browseResp)
		if derr != nil {
			decodeErr = derr
		}
		return page, derr
	}, paging.BrowsePageSize)
	if decodeErr != nil {
		return nil, decodeErr
	}
	if err != nil {
		return nil, nil //nolint:nilerr // absent linkage graph = no proxies, not an error
	}
	return proxies, nil
}

// collectProxyCrossLinks reproduces the server CollectProxyCrossLinks for one
// proxy: the proxy's edges (both directions) read through the bounded pivot
// drain + ONE bulk peer hydrate, building []crossLink with proxyInfoWire for
// each peer.
//
// The second return is the peer hydrate's truncation verdict, and it matters
// MORE here than on the edge summary: an unresolved peer is not merely rendered
// without a name, it is SKIPPED (see the loop below), so a clamped hydrate drops
// cross-link rows outright.
func collectProxyCrossLinks(ctx context.Context, exec ExecuteFn, proxy *knowledgev1.Node) ([]crossLink, bool, error) {
	if proxy.GetId() == "" {
		// An edges plan with no pivot means "every edge of the graph" — never what
		// a per-proxy cross-link read wants.
		return nil, false, nil
	}
	rawEdges, err := paging.DrainPivotEdges([]string{proxy.Id}, paging.EdgePivotPageSize, CorrelationsEdgeScanCap,
		func(idPage []string, fromIDGte, fromIDLt string) ([]knowledgev1.Edge, bool, error) {
			return pivotEdgePage(ctx, exec, idPage, paging.EdgeFromBandOrNil(fromIDGte, fromIDLt), false, linkageTarget)
		})
	if err != nil {
		return nil, false, err
	}
	if len(rawEdges) == 0 {
		return nil, false, nil
	}

	// Shape edges → (peerID, edgeType, direction); collect peer IDs.
	type edgeRow struct {
		peerID    string
		edgeType  kgtypes.EdgeType
		direction string
	}
	rows := make([]edgeRow, len(rawEdges))
	peerSet := make(map[string]struct{}, len(rawEdges))
	for i := range rawEdges {
		e := &rawEdges[i]
		peerID, direction := e.ToId, "outgoing"
		if e.FromId != proxy.Id {
			peerID, direction = e.FromId, "incoming"
		}
		rows[i] = edgeRow{peerID: peerID, edgeType: kgtypes.EdgeType(e.Type), direction: direction}
		peerSet[peerID] = struct{}{}
	}

	peerIDs := make([]string, 0, len(peerSet))
	for pid := range peerSet {
		peerIDs = append(peerIDs, pid)
	}
	peers, peersTruncated, err := bulkHydratePeers(ctx, exec, peerIDs, linkageTarget)
	if err != nil {
		return nil, false, err
	}

	links := make([]crossLink, 0, len(rows))
	for _, r := range rows {
		peer, ok := peers[r.peerID]
		if !ok {
			// Mirror the legacy CollectProxyCrossLinks: an edge whose peer does
			// not resolve is SKIPPED (it only appends when len(peerQR.NodeList)>0).
			continue
		}
		links = append(links, crossLink{
			EdgeType:  r.edgeType,
			Direction: r.direction,
			Peer:      peer,
			PeerInfo:  kgwire.ProxyInfo(peer),
		})
	}
	return links, peersTruncated, nil
}
