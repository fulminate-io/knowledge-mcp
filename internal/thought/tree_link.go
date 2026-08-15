// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"sort"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// tree_link.go owns the RESOLUTION CONTRACT of the lever's tree-link phase: every
// similarity pass it resolves each thought's tree-adjacent work-item artifacts UP the
// `contains` ancestry to their tree root and groups thoughts by shared tree root (the
// clique WRITE half — computeTreeLinkEdges / writeTreeLinkEdges / runTreeLinkPhase —
// lives in tree_link_write.go). The structural intent: thoughts that resolve into the
// same project/ticket/plan containment tree are ALWAYS associated, so the next
// reflection's Leiden collapses the topic count toward project-shaped topics (CPM at
// gamma=0.5 splits sparse shapes, so a full clique is the only shape that survives the
// community detector).
//
// TWO DISTINCT HOPS — keep them separate:
//   - the ATTACHMENT hop (collectThoughtArtifacts: thought → its adjacent artifact) is
//     edge-type-UNFILTERED and BIDIRECTIONAL — a thought attached to a work-item
//     artifact by ANY edge (contains, relates-to, informed-by, …) in EITHER direction
//     is a tree member (CEO ruling 2026-06-10). Only thought↔thought, session, proxy,
//     and charge neighbors are excluded.
//   - the ANCESTRY hop (buildContainsParentMap: artifact → its tree root) stays
//     `contains`-ONLY and upward — tree membership is still decided by the work-item
//     containment spine; the widening is purely in how a thought first attaches to an
//     artifact, never in how that artifact climbs to its root.
//
// The resolution is deterministic (sorted iteration + min-root tie-break), and bounded:
// the ancestry walk is a LEVEL-BY-LEVEL bulk read capped at treeRootMaxLevels with a
// cycle guard, costing a fixed handful of bulk reads regardless of corpus size. It needs
// only the containment graph (no vectors, no LLM), so it runs in every degrade band.

// treeRootEligibleTypes is the set of work-item containment types that may anchor a
// tree root: the project→ticket→plan→phase→step `contains` hierarchy
// (kgtypes/node_types.go:15-19). A tree root is emitted ONLY for one of these types;
// a thought whose artifacts resolve to no work-item root writes nothing (the ticket
// grouping rule that tree membership is the work-item containment spine alone).
var treeRootEligibleTypes = map[kgtypes.NodeType]bool{
	kgtypes.NodeProject: true,
	kgtypes.NodeTicket:  true,
	kgtypes.NodePlan:    true,
	kgtypes.NodePhase:   true,
	kgtypes.NodeStep:    true,
}

// treeLinkMethod tags every relates-to edge the tree-link phase writes, so it is
// distinguishable from authored relates-to edges AND from the densify
// ("topic-densify") and medoid ("topic-similarity") machine edges — its OWN distinct
// provenance so the three machine-edge origins are independently identifiable for
// cleanup or tension exclusion. A distinct tag (not a reuse of densifyMethod) is what
// keeps the tree-link clique edges separable from the within-topic kNN edges.
const treeLinkMethod = "tree-link"

// treeLinkEdgeConfidence is the LOW explicit Confidence stamped on every tree-link
// edge to MARK its machine origin (below the authored-edge convention: a bare
// authored mutate(link) leaves Confidence 0). Same confidence-consumer census stance
// as densifyEdgeConfidence: NO current consumer reads edge Confidence/Method for
// thought-graph reflection/trust/clustering, so discounting machine edges is NOTED AS
// AVAILABLE (the metadata is present + discountable the moment a consumer reads it)
// but is NOT WIRED NOW.
const treeLinkEdgeConfidence = 0.25

// treeRootMaxLevels bounds the level-by-level contains-ancestry walk: the
// project→ticket→plan→phase→step spine is 5 deep, so 6 levels covers it with one
// level of slack while guaranteeing the walk terminates regardless of graph shape
// (a malformed deeper chain is simply truncated, never hung).
const treeRootMaxLevels = 6

// TreeLinkTreeStat (the per-tree report row) + the clique-write half of the phase
// (computeTreeLinkEdges / writeTreeLinkEdges / runTreeLinkPhase) live in
// tree_link_write.go — this file owns the resolution contract.

// collectThoughtArtifacts collects, for each thought in thoughtIDs, the set of
// work-item artifacts it is attached to by ANY edge in EITHER direction — the seed for
// the upward root resolution. It does TWO bulk reads for the WHOLE thought set (no
// per-thought traverse): a bulk fetchEdgesForNodeSet over the thought IDs with NO
// edge-type filter, then ONE bulk fetchNodesByIDs over the distinct neighbor IDs to
// hydrate their types.
//
// It returns TWO maps off those same two reads: artifactsByThought (thought → sorted
// distinct kept-artifact IDs) and artifactByID (kept-artifact ID → its already-hydrated
// node). artifactByID carries ONLY the kept artifacts — every node dropped by the
// neighbor gate (thought/session/proxy/charge) is absent — so a downstream consumer can
// read an artifact's type/name/metadata WITHOUT a third bulk hydrate. The artifact-link
// clique phase consumes artifactByID to classify each shared artifact (real-type gate +
// analyzer-finding exclusion) off the in-hand data the tree-link pass already fetched.
//
// ATTACHMENT IS EDGE-TYPE-UNFILTERED AND BIDIRECTIONAL (CEO ruling: a thought linked
// to the project tree by ANY edge type falls under the always-link rule). The earlier
// boundary read only `contains` edges (a ticket--contains-->thought, child endpoint);
// historical thoughts instead attach via relates-to/informed-by FROM the thought TO
// the artifact (the links: param), so a contains-only read found zero of them. The
// attachment hop is therefore the OTHER endpoint of any edge touching a requested
// thought: an edge whose ToId is a requested thought contributes its FromId, and an
// edge whose FromId is a requested thought contributes its ToId. The artifact then
// resolves UP the `contains` spine to its root exactly as before — the ancestry walk
// (buildContainsParentMap) stays contains-only; only this ATTACHMENT hop widened.
//
// NEIGHBOR GATE (node-type-based): a neighbor hydrated as NodeThought is DROPPED —
// thought↔thought edges (e.g. relates-to between two thoughts) are the similarity /
// densify domain, NOT tree attachment. NodeThoughtSession is DROPPED — session
// containment already feeds Leiden via deriveSessionSiblings, and re-linking on it here
// would double-count the same grouping. NodeProxy / NodeCharge neighbors are dropped
// likewise (a proxy/charge is never a work-item artifact). Every OTHER neighbor type is
// kept and resolved upward: the neighbor need NOT itself be tree-eligible — a step
// neighbor resolves up to its project root, and a finding neighbor resolves up to the
// ticket that contains it. The work-item eligibility gate applies to the ROOT, in
// ResolveTreeRoots, not to the neighbor here.
func collectThoughtArtifacts(ctx context.Context, gc Caller, thoughtIDs []string) (artifactsByThought map[string][]string, artifactByID map[string]*knowledgev1.Node, err error) {
	out := map[string][]string{}
	kept := map[string]*knowledgev1.Node{}
	if gc == nil || len(thoughtIDs) == 0 {
		return out, kept, nil
	}

	thoughtSet := make(map[string]bool, len(thoughtIDs))
	for _, id := range thoughtIDs {
		thoughtSet[id] = true
	}

	// Edge-type-UNFILTERED read (nil edge types): every edge touching the thought set,
	// both directions.
	edges, eerr := fetchEdgesForNodeSet(ctx, gc, thoughtIDs, nil)
	if eerr != nil {
		return nil, nil, eerr
	}

	// Candidate neighbor (artifact) per thought, plus the distinct neighbor set for the
	// one type-hydrate read. The candidate is the endpoint that is NOT the requested
	// thought, for an edge in either direction.
	candidatesByThought := make(map[string][]string, len(thoughtIDs))
	neighborSet := map[string]bool{}
	addCandidate := func(tid, neighbor string) {
		if neighbor == tid || thoughtSet[neighbor] {
			return // self-loop or thought↔thought edge — never an attachment artifact
		}
		candidatesByThought[tid] = append(candidatesByThought[tid], neighbor)
		neighborSet[neighbor] = true
	}
	for i := range edges {
		e := &edges[i]
		if thoughtSet[e.ToId] {
			addCandidate(e.ToId, e.FromId) // artifact--*-->thought (e.g. ticket--contains-->thought)
		}
		if thoughtSet[e.FromId] {
			addCandidate(e.FromId, e.ToId) // thought--*-->artifact (e.g. thought--informed-by-->ticket)
		}
	}
	if len(neighborSet) == 0 {
		return out, kept, nil
	}

	neighborIDs := make([]string, 0, len(neighborSet))
	for id := range neighborSet {
		neighborIDs = append(neighborIDs, id)
	}
	neighborByID := fetchNodesByIDs(ctx, gc, neighborIDs)

	// Drop thought/session/proxy/charge neighbors; keep the rest (resolved upward
	// later). Build the per-thought artifact list in a deterministic (sorted) order, and
	// record each kept artifact's hydrated node in `kept` so the artifact-link phase can
	// classify it without a third bulk read.
	for tid, neighbors := range candidatesByThought {
		var keptForThought []string
		seen := map[string]bool{}
		for _, nid := range neighbors {
			if seen[nid] {
				continue
			}
			n, ok := neighborByID[nid]
			if !ok {
				continue // un-hydratable neighbor (tombstoned/deleted) — skip
			}
			switch kgtypes.NodeType(n.Type) {
			case kgtypes.NodeThought, kgtypes.NodeThoughtSession, kgtypes.NodeProxy, kgtypes.NodeCharge:
				continue // thought↔thought, session, proxies/charges are not work-item artifacts
			}
			seen[nid] = true
			keptForThought = append(keptForThought, nid)
			kept[nid] = n
		}
		if len(keptForThought) > 0 {
			sort.Strings(keptForThought)
			out[tid] = keptForThought
		}
	}
	return out, kept, nil
}

// treeResolution is the FULL output of resolveArtifactsAndRoots — every map the
// tree-link resolution computes in one pass, surfaced so a sibling clique phase
// (artifact-link) consumes the SAME data without re-resolving or re-reading:
//
//   - artifactsByThought: thought → sorted distinct kept-artifact IDs (the attachment hop);
//   - artifactByID:        kept-artifact ID → its already-hydrated node (type/name/metadata);
//   - resolvedArtifactRoot: artifact ID → the work-item-spine root it climbs to (any type);
//   - rootNodeByID:        resolved-root ID → its already-hydrated node (eligibility/name);
//   - rootByThought:       thought → its work-item tree root (eligible roots only; the
//     tree-link grouping key);
//   - rootNames:           eligible-root ID → display name for the report.
//
// resolvedArtifactRoot carries EVERY artifact's spine root regardless of root
// eligibility, so the artifact-link phase can ask "does this artifact resolve to a
// work-item root?" (→ tree-link's domain, exclude it) off rootNodeByID without a
// second resolution.
type treeResolution struct {
	artifactsByThought   map[string][]string
	artifactByID         map[string]*knowledgev1.Node
	resolvedArtifactRoot map[string]string
	rootNodeByID         map[string]*knowledgev1.Node
	rootByThought        map[string]string
	rootNames            map[string]string
}

// ResolveTreeRoots resolves each thought in thoughtIDs to the work-item tree root of
// its tree-adjacent artifacts and returns rootByThought (thought → root id, omitting
// thoughts with no eligible root) plus rootNames (root id → display name for the
// report). A thought attaches to an artifact by ANY edge in EITHER direction (the
// widened attachment hop in collectThoughtArtifacts); that artifact then climbs the
// `contains` spine to its root. So a thought that merely relates-to a finding which a
// ticket contains resolves to the ticket's tree and is a member — the relates-to is the
// attachment, the ticket→finding contains edge is the spine.
//
// EXPORTED — shared contract: the vector orphan-absorption dry-run phase (same package)
// reuses this exact root resolution to classify absorption candidates against the same
// work-item trees the write phase groups by. Do NOT unexport it, and keep the
// (thoughtIDs)→(rootByThought, rootNames) shape stable, without first updating that
// consumer. The body now DELEGATES whole to resolveArtifactsAndRoots and returns only
// the two public maps from the bundle — the resolution logic is unchanged; the
// extraction merely lets the artifact-link sibling phase consume the rest of the bundle
// (artifactByID, resolvedArtifactRoot, rootNodeByID) off ONE resolution.
func ResolveTreeRoots(ctx context.Context, gc Caller, thoughtIDs []string) (rootByThought map[string]string, rootNames map[string]string, err error) {
	res, rerr := resolveArtifactsAndRoots(ctx, gc, thoughtIDs)
	if rerr != nil {
		return nil, nil, rerr
	}
	return res.rootByThought, res.rootNames, nil
}

// resolveArtifactsAndRoots is the internal resolution core: it collects each thought's
// kept artifacts (collectThoughtArtifacts — TWO bulk reads, surfacing artifactByID too),
// then walks each artifact UP the `contains` ancestry to its tree root via a BOUNDED
// LEVEL-BY-LEVEL bulk read: starting from the distinct artifact set, it repeats
// (≤ treeRootMaxLevels) a fetchEdgesForNodeSet over the current level filtered to
// EdgeKGContains — each such read itself drained in bounded pivot pages —
// recording parentOf[child]=parent for every contains edge whose child
// is a current-level node, and stops the moment a level adds no new parent. The
// resolution is then pure in-memory:
//
//   - a node with no contains-parent is its OWN root (missing-parent = own root);
//   - a contains CYCLE (a→b→a) terminates via a visited-set guard, choosing the
//     deterministic minimum-ID node in the cycle as the root for reproducibility;
//   - when a thought's multiple artifacts resolve to DIFFERENT roots, the
//     deterministic MINIMUM root id wins (a documented tie-break mirroring the
//     densify lexicographic-determinism discipline);
//   - a resolved root whose hydrated type is NOT in treeRootEligibleTypes is DROPPED
//     from rootByThought (the thought writes nothing) — only the project→…→step spine
//     anchors a tree.
//
// It returns the WHOLE treeResolution bundle (see the type doc) so both the tree-link
// phase (rootByThought/rootNames) and the artifact-link phase (artifactByID +
// resolvedArtifactRoot + rootNodeByID, to exclude work-item-rooted artifacts) run off a
// SINGLE resolution. Cost: TWO bulk reads in collect + ≤ treeRootMaxLevels bulk edge
// reads + ONE bulk root hydrate for the WHOLE corpus — bounded regardless of N. The
// cycle guard + level cap guarantee no hang.
func resolveArtifactsAndRoots(ctx context.Context, gc Caller, thoughtIDs []string) (treeResolution, error) {
	res := treeResolution{
		artifactsByThought:   map[string][]string{},
		artifactByID:         map[string]*knowledgev1.Node{},
		resolvedArtifactRoot: map[string]string{},
		rootNodeByID:         map[string]*knowledgev1.Node{},
		rootByThought:        map[string]string{},
		rootNames:            map[string]string{},
	}
	if gc == nil || len(thoughtIDs) == 0 {
		return res, nil
	}

	artifactsByThought, artifactByID, aerr := collectThoughtArtifacts(ctx, gc, thoughtIDs)
	if aerr != nil {
		return treeResolution{}, aerr
	}
	res.artifactsByThought = artifactsByThought
	res.artifactByID = artifactByID
	if len(artifactsByThought) == 0 {
		return res, nil
	}

	// Distinct seed artifact set.
	artifactSet := map[string]bool{}
	for _, arts := range artifactsByThought {
		for _, a := range arts {
			artifactSet[a] = true
		}
	}

	parentOf, perr := buildContainsParentMap(ctx, gc, artifactSet)
	if perr != nil {
		return treeResolution{}, perr
	}

	// Resolve each distinct artifact to its root (memoized), collecting the distinct
	// root set for one bulk type/name hydrate.
	rootCache := map[string]string{}
	rootIDSet := map[string]bool{}
	for a := range artifactSet {
		r := resolveRoot(a, parentOf, rootCache)
		res.resolvedArtifactRoot[a] = r
		rootIDSet[r] = true
	}
	rootIDs := make([]string, 0, len(rootIDSet))
	for r := range rootIDSet {
		rootIDs = append(rootIDs, r)
	}
	res.rootNodeByID = fetchNodesByIDs(ctx, gc, rootIDs)

	// Per-thought root: pick the deterministic minimum eligible root across artifacts.
	for tid, arts := range artifactsByThought {
		best := bestEligibleRoot(arts, res.resolvedArtifactRoot, res.rootNodeByID)
		if best != "" {
			res.rootByThought[tid] = best
			if n, ok := res.rootNodeByID[best]; ok {
				res.rootNames[best] = n.GetSymbolName()
			}
		}
	}
	return res, nil
}

// buildContainsParentMap walks UP the `contains` ancestry from the seed artifact set in
// a BOUNDED LEVEL-BY-LEVEL bulk read (≤ treeRootMaxLevels fetchEdgesForNodeSet calls,
// each drained in bounded pivot pages),
// recording parentOf[child]=parent for every contains edge whose child is a current-
// level node. It stops the moment a level adds no new parent. The level cap guarantees
// termination regardless of graph shape.
func buildContainsParentMap(ctx context.Context, gc Caller, artifactSet map[string]bool) (map[string]string, error) {
	parentOf := map[string]string{}
	currentLevel := make([]string, 0, len(artifactSet))
	for a := range artifactSet {
		currentLevel = append(currentLevel, a)
	}
	levelSet := make(map[string]bool, len(artifactSet))
	for _, id := range currentLevel {
		levelSet[id] = true
	}
	for level := 0; level < treeRootMaxLevels && len(currentLevel) > 0; level++ {
		edges, eerr := fetchEdgesForNodeSet(ctx, gc, currentLevel, []kgtypes.EdgeType{kgtypes.EdgeKGContains})
		if eerr != nil {
			return nil, eerr
		}
		nextLevel := nextContainsLevel(edges, levelSet, parentOf)
		if len(nextLevel) == 0 {
			break // no new parents — ancestry exhausted
		}
		sort.Strings(nextLevel)
		currentLevel = nextLevel
		levelSet = make(map[string]bool, len(nextLevel))
		for _, id := range nextLevel {
			levelSet[id] = true
		}
	}
	return parentOf, nil
}

// nextContainsLevel records the first-seen parent of every current-level node from the
// contains edges and returns the distinct new parents to walk next.
func nextContainsLevel(edges []knowledgev1.Edge, levelSet map[string]bool, parentOf map[string]string) []string {
	var nextLevel []string
	for i := range edges {
		e := &edges[i]
		if kgtypes.EdgeType(e.Type) != kgtypes.EdgeKGContains || !levelSet[e.ToId] {
			continue // only contains edges whose child is a current-level node
		}
		if _, known := parentOf[e.ToId]; known {
			continue // first-seen parent wins (deterministic via the sorted level)
		}
		parentOf[e.ToId] = e.FromId
		if !levelSet[e.FromId] {
			nextLevel = append(nextLevel, e.FromId)
		}
	}
	return nextLevel
}

// resolveRoot walks parentOf up from start to its tree root, memoizing into rootCache.
// MISSING-PARENT = own root; a contains CYCLE terminates via the visited guard, choosing
// the deterministic minimum-ID node seen as the root for reproducibility.
func resolveRoot(start string, parentOf map[string]string, rootCache map[string]string) string {
	if r, ok := rootCache[start]; ok {
		return r
	}
	visited := map[string]bool{}
	minSeen := start
	cur := start
	for !visited[cur] {
		visited[cur] = true
		if cur < minSeen {
			minSeen = cur
		}
		parent, ok := parentOf[cur]
		if !ok {
			minSeen = cur // missing parent = own root
			break
		}
		cur = parent
	}
	rootCache[start] = minSeen
	return minSeen
}

// bestEligibleRoot returns the deterministic minimum work-item-eligible root across a
// thought's artifacts, or "" when no artifact resolves to a work-item anchor.
func bestEligibleRoot(arts []string, resolvedArtifactRoot map[string]string, rootNodeByID map[string]*knowledgev1.Node) string {
	best := ""
	for _, a := range arts {
		r := resolvedArtifactRoot[a]
		n, ok := rootNodeByID[r]
		if !ok || !treeRootEligibleTypes[kgtypes.NodeType(n.Type)] {
			continue // root not a work-item anchor — this artifact contributes nothing
		}
		if best == "" || r < best {
			best = r
		}
	}
	return best
}
