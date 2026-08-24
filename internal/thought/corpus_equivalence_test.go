// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"math"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// corpus_equivalence_test.go is the LOAD-BEARING correctness gate for the client
// corpus cache: it proves the reflect output is IDENTICAL
// whether the thought-node set is served from the resident cache (CorpusSnapshot,
// fed through the real MergeDelta machinery) or drained from the wire — over the
// WIDENED surface (cluster assignments + propagated valence/magnitude + adjacency +
// topic-label overrides + blind-spot report). The ONLY variable between the two
// runs is the thought-node SOURCE; everything else (edges, charges, sessions, topic
// docs, clock) is the same fake wire, so a merge/dedup/tombstone bug in the cache
// surfaces as a divergence.
//
// This is an EQUIVALENCE/characterization test, NOT a defect reproduction: it is
// green once the cache is correct. Its value is going RED the moment the merge,
// dedup, tombstone handling, or a mis-rewired consumer diverges the cache's node set
// from the drain — which guards the Step 4.3 rewire. Its production-RED counterpart
// is the per-tick reconciliation probe (refreshCorpusCache → Reconcile), which fires
// slog.Warn + a forced resync on any LIVE divergence that escapes this harness.
//
// CANONICALIZATION (T3-3b): map-iteration order over the cache's live set is random,
// so a raw comparison would flake TWO ways. (1) Slice order: cluster member-id lists
// and adjacency neighbor lists are sorted, the cluster list is size/ID-sorted by the
// reader, and the blind-spot facets are deterministically ordered by the classifier.
// (2) Float summation order: DeGroot sums valence/magnitude in the node set's
// iteration order and float addition is non-associative, so the two runs differ in
// the last ULP — every float is therefore rounded to canonEpsilon before compare.
// Together the sort + round are what make the equality a REAL signal rather than a
// flaky map-order comparison; a genuine merge divergence changes membership /
// adjacency / counts (exact, unrounded) and shifts values far above the grid, so it
// still bites. Charges carry UpdatedAt=0 so the read-time recency scalar is a
// now-independent 0.5, and both runs share ONE fixed clock for the recency facets.

// equivThoughtSpec is one thought row in the equivalence corpus.
type equivThoughtSpec struct {
	id        string
	clusterID string // persisted cluster_id (drives DetectPersistedClusters partition)
	sessionID string // enclosing thought_session (EdgeKGContains), "" = none
}

// equivChargeSpec is one charge attached to a thought (UpdatedAt=0 → recency-neutral).
type equivChargeSpec struct {
	id       string
	thoughtT string
	polarity string
	weight   string
}

// equivCorpusSpec is the whole seed: thoughts + charges + adjacency edges + topic
// docs. Seeds BOTH a fake wire (for the drain run) and the resident cache (for the
// cached run) from the SAME data, so any divergence is the cache's fault.
type equivCorpusSpec struct {
	thoughts []equivThoughtSpec
	charges  []equivChargeSpec
	edges    [][2]string       // undirected adjacency edges (EdgeRelatesTo)
	sessions map[string]string // sessionID → SymbolName label
	docs     []equivDocSpec    // topic docs (NodeDocument)
}

// equivDocSpec is one persisted topic doc overriding a cluster's display label.
type equivDocSpec struct {
	id        string
	clusterID string // cluster_id anchor
	summary   string // Description → the overridden label
}

// defaultEquivSpec is a small structured corpus: two clusters (cA={a1,a2,a3},
// cB={b1,b2,b3}), triangle adjacency within each, a session over cluster A, mixed
// charges (so valence/magnitude and the blind-spot facets are non-trivial), and one
// topic doc overriding cluster cA's label.
func defaultEquivSpec() equivCorpusSpec {
	return equivCorpusSpec{
		thoughts: []equivThoughtSpec{
			{"a1", "cA", "sessA"}, {"a2", "cA", "sessA"}, {"a3", "cA", "sessA"},
			{"b1", "cB", ""}, {"b2", "cB", ""}, {"b3", "cB", ""},
		},
		charges: []equivChargeSpec{
			{"chg-a1", "a1", "positive", "5"},
			{"chg-a2a", "a2", "positive", "3"}, {"chg-a2b", "a2", "negative", "1"},
			{"chg-b1", "b1", "negative", "4"},
			{"chg-b2", "b2", "positive", "2"},
		},
		edges: [][2]string{
			{"a1", "a2"}, {"a2", "a3"}, {"a1", "a3"},
			{"b1", "b2"}, {"b2", "b3"}, {"b1", "b3"},
		},
		sessions: map[string]string{"sessA": "session-alpha"},
		docs:     []equivDocSpec{{"doc-cA", "cA", "Topic Alpha"}},
	}
}

// reflectEquivFake is a stateful Caller serving the whole reflect wire surface the
// equivalence pass reads: the type=thought / type=document browses, the ids[]
// hydrate (thoughts + charges + docs), the RETURN_MODE_EDGES reads (adjacency /
// EdgeChargedBy / EdgeKGContains, filtered by the requested selection + node set),
// and the reflect-inert metadata writeback (applied + ignored). Deliberately built
// so the ONLY thing the cache changes is the thought-NODE source.
type reflectEquivFake struct {
	thoughts   map[string]*knowledgev1.Node
	thoughtIDs []string // stable browse order (drain path determinism)
	charges    map[string]*knowledgev1.Node
	docs       []*knowledgev1.Node
	sessions   map[string]*knowledgev1.Node
	// typed edges keyed by type; each carries {from,to}.
	edgesByType map[kgtypes.EdgeType][][2]string
}

func newReflectEquivFake(spec equivCorpusSpec) *reflectEquivFake {
	f := &reflectEquivFake{
		thoughts:    map[string]*knowledgev1.Node{},
		charges:     map[string]*knowledgev1.Node{},
		sessions:    map[string]*knowledgev1.Node{},
		edgesByType: map[kgtypes.EdgeType][][2]string{},
	}
	for i, ts := range spec.thoughts {
		n := &knowledgev1.Node{Id: ts.id, Type: string(kgtypes.NodeThought), UpdatedAt: int64(1000 + i)}
		kgtypes.SetValue(n, metaClusterID, ts.clusterID)
		if ts.sessionID != "" {
			kgtypes.SetValue(n, "session", ts.sessionID)
			// EdgeKGContains is session(From)→thought(To).
			f.edgesByType[kgtypes.EdgeKGContains] = append(f.edgesByType[kgtypes.EdgeKGContains], [2]string{ts.sessionID, ts.id})
		}
		f.thoughts[ts.id] = n
		f.thoughtIDs = append(f.thoughtIDs, ts.id)
	}
	for _, cs := range spec.charges {
		c := &knowledgev1.Node{Id: cs.id, Type: string(kgtypes.NodeCharge)} // UpdatedAt=0 → recency-neutral.
		kgtypes.SetValue(c, "polarity", cs.polarity)
		kgtypes.SetValue(c, "weight", cs.weight)
		f.charges[cs.id] = c
		// EdgeChargedBy is thought(From)→charge(To).
		f.edgesByType[kgtypes.EdgeChargedBy] = append(f.edgesByType[kgtypes.EdgeChargedBy], [2]string{cs.thoughtT, cs.id})
	}
	for _, e := range spec.edges {
		f.edgesByType[kgtypes.EdgeRelatesTo] = append(f.edgesByType[kgtypes.EdgeRelatesTo], e)
	}
	for sid, label := range spec.sessions {
		f.sessions[sid] = &knowledgev1.Node{Id: sid, Type: string(kgtypes.NodeThoughtSession), SymbolName: label}
	}
	for _, ds := range spec.docs {
		d := &knowledgev1.Node{Id: ds.id, Type: string(kgtypes.NodeDocument), Description: ds.summary}
		kgtypes.SetValue(d, metaClusterID, ds.clusterID)
		f.docs = append(f.docs, d)
	}
	return f
}

// thoughtNodesSlice returns the corpus thought nodes in stable order — the drain
// browse payload AND the seed for the cached run's MergeDelta.
func (f *reflectEquivFake) thoughtNodesSlice() []*knowledgev1.Node {
	out := make([]*knowledgev1.Node, 0, len(f.thoughtIDs))
	for _, id := range f.thoughtIDs {
		out = append(out, cloneNode(f.thoughts[id]))
	}
	return out
}

func (f *reflectEquivFake) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	if req.GetMutation() != nil {
		return &knowledgev1.ExecuteResponse{}, nil // reflect-inert writeback: accepted, not modeled.
	}
	q := req.GetQuery()
	if q == nil {
		return &knowledgev1.ExecuteResponse{}, nil
	}

	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		return &knowledgev1.ExecuteResponse{Edges: bandNarrow(f.edgesFor(q), q)}, nil
	}
	if q.GetById() != "" {
		return &knowledgev1.ExecuteResponse{}, nil // watermark / singleton reads: empty.
	}
	if ids := q.GetIds(); len(ids) > 0 {
		var nodes []*knowledgev1.Node
		for _, id := range ids {
			if n := f.nodeByID(id); n != nil {
				nodes = append(nodes, n)
			}
		}
		return &knowledgev1.ExecuteResponse{Nodes: nodes}, nil
	}
	if q.GetOffset() > 0 {
		return &knowledgev1.ExecuteResponse{}, nil // single-page corpus → later pages empty.
	}
	return &knowledgev1.ExecuteResponse{Nodes: f.browse(q.GetSelection().GetNodeType())}, nil
}

// edgesFor returns the edges matching a RETURN_MODE_EDGES query: the requested
// selection edge types (or all when unset) restricted to the requested node set
// (ids[] both-direction union, or the single ById endpoint).
func (f *reflectEquivFake) edgesFor(q *knowledgev1.QueryPlan) []*knowledgev1.Edge {
	want := map[kgtypes.EdgeType]bool{}
	if sel := q.GetSelection(); sel != nil && len(sel.GetEdgeTypes()) > 0 {
		for _, et := range sel.GetEdgeTypes() {
			want[kgtypes.EdgeType(et)] = true
		}
	}
	inScope := map[string]bool{}
	for _, id := range q.GetIds() {
		inScope[id] = true
	}
	if b := q.GetById(); b != "" {
		inScope[b] = true
	}
	var out []*knowledgev1.Edge
	for et, pairs := range f.edgesByType {
		if len(want) > 0 && !want[et] {
			continue
		}
		for _, p := range pairs {
			if len(inScope) > 0 && !inScope[p[0]] && !inScope[p[1]] {
				continue // node-SET union: keep an edge incident to ANY requested id.
			}
			out = append(out, &knowledgev1.Edge{Type: string(et), FromId: p[0], ToId: p[1]})
		}
	}
	// The band is NOT applied here: this function's only caller wraps its result in
	// bandNarrow, and that wrap is the uniform rule every fake return in this package
	// follows. Narrowing twice would be harmless (the predicate is idempotent) but it
	// would put the band in two places, which is how the two copies drift apart.
	return out
}

func (f *reflectEquivFake) nodeByID(id string) *knowledgev1.Node {
	if n, ok := f.thoughts[id]; ok {
		return cloneNode(n)
	}
	if c, ok := f.charges[id]; ok {
		return cloneNode(c)
	}
	if s, ok := f.sessions[id]; ok {
		return cloneNode(s)
	}
	for _, d := range f.docs {
		if d.GetId() == id {
			return cloneDoc(d)
		}
	}
	return nil
}

// cloneDoc clones a topic doc preserving Description (the topic-summary text
// ApplyTopicLabels overrides labels with) — cloneNode drops Description, which would
// silently empty the topic-label surface in this test.
func cloneDoc(d *knowledgev1.Node) *knowledgev1.Node {
	cp := cloneNode(d)
	cp.Description = d.GetDescription()
	return cp
}

func (f *reflectEquivFake) browse(nodeType string) []*knowledgev1.Node {
	switch kgtypes.NodeType(nodeType) {
	case kgtypes.NodeThought:
		return f.thoughtNodesSlice()
	case kgtypes.NodeDocument:
		out := make([]*knowledgev1.Node, 0, len(f.docs))
		for _, d := range f.docs {
			out = append(out, cloneDoc(d))
		}
		return out
	case kgtypes.NodeCharge:
		out := make([]*knowledgev1.Node, 0, len(f.charges))
		for _, c := range f.charges {
			out = append(out, cloneNode(c))
		}
		return out
	default:
		return nil
	}
}

// equivSurface is the canonicalized reflect output over the five compared units.
type equivSurface struct {
	clusters   []canonCluster
	adjacency  map[string][]string
	valence    map[string]float64
	magnitude  map[string]float64
	blindSpots BlindSpotReport
}

type canonCluster struct {
	ID           string
	Members      []string
	Label        string
	Size         int
	AvgValence   float64
	AvgMagnitude float64
}

// computeEquivSurface runs the five criterion surfaces against ONE fake wire, with
// the thought-node set sourced through `src` (usually the loop `p`: p.corpus
// populated → resident cache; p.corpus nil → wire drain; or a newPassReads(p) memo
// over it). Every non-node input (edges, charges, sessions, docs) comes from the same
// fake, so the surface differs ONLY if the source diverges the node set.
func computeEquivSurface(t *testing.T, gc *reflectEquivFake, p *PropagationLoop, src CorpusSource) equivSurface {
	t.Helper()
	ctx := context.Background()

	clusters, err := DetectPersistedClusters(ctx, gc, src)
	require.NoError(t, err)

	nodeIDs, adj, err := fetchAdjacency(ctx, gc, "all", nil, src)
	require.NoError(t, err)

	res, err := RunPropagationScoped(ctx, gc, nil, nil, nil, src)
	require.NoError(t, err)

	// Topic-label overrides: mutate cluster labels from the persisted topic docs.
	ApplyTopicLabels(ctx, gc, clusters, nil)

	// Blind-spot report over a sorted node set (the loop passes adjacency's nodeIDs;
	// sorting removes the cache-random ORDER as a variable — a merge bug still changes
	// the SET, which sorting preserves).
	sortedIDs := append([]string(nil), nodeIDs...)
	sort.Strings(sortedIDs)
	blindSpots := p.computeBlindSpots(ctx, sortedIDs, clusters, src)

	return canonicalizeSurface(clusters, adj, res, blindSpots)
}

// canonicalizeSurface normalizes both runs into a deterministic form: cluster
// members + list sorted, adjacency neighbor lists sorted, and EVERY float rounded to
// canonEpsilon. The float rounding is load-bearing: DeGroot sums valence/magnitude in
// the map-iteration order of the node set (cache-random vs drain-deterministic), and
// float addition is non-associative, so the two runs differ in the last ULP (~1e-16).
// Rounding to 1e-9 collapses that ULP noise while leaving any GENUINE divergence — a
// merge bug changes membership/adjacency/counts (exact, unrounded) and shifts values
// by far more than 1e-9 — fully visible. Maps and the deterministically-ordered
// blind-spot report then compare order-independently under require.Equal.
func canonicalizeSurface(clusters []ThoughtCluster, adj map[string][]string, res PropagationResult, bs BlindSpotReport) equivSurface {
	cc := make([]canonCluster, 0, len(clusters))
	for _, c := range clusters {
		members := append([]string(nil), c.ThoughtIDs...)
		sort.Strings(members)
		cc = append(cc, canonCluster{
			ID: c.ID, Members: members, Label: c.Label, Size: c.Size,
			AvgValence: canonRound(c.AvgValence), AvgMagnitude: canonRound(c.AvgMagnitude),
		})
	}
	sort.Slice(cc, func(i, j int) bool { return cc[i].ID < cc[j].ID })

	canonAdj := make(map[string][]string, len(adj))
	for k, neigh := range adj {
		n := append([]string(nil), neigh...)
		sort.Strings(n)
		canonAdj[k] = n
	}
	return equivSurface{
		clusters:   cc,
		adjacency:  canonAdj,
		valence:    canonRoundMap(res.ValenceChanges),
		magnitude:  canonRoundMap(res.MagnitudeChanges),
		blindSpots: canonRoundBlindSpots(bs),
	}
}

// canonEpsilon is the rounding grid for float canonicalization: 1e-9 sits far above
// the ~1e-16 float-summation ULP noise and far below any value shift a real merge
// bug produces.
const canonEpsilon = 1e9

func canonRound(x float64) float64 { return math.Round(x*canonEpsilon) / canonEpsilon }

func canonRoundMap(m map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(m))
	for k, v := range m {
		out[k] = canonRound(v)
	}
	return out
}

// canonRoundBlindSpots rebuilds the report with every per-item float rounded, so the
// blind-spot influence/magnitude/consistency values compare free of ULP noise.
func canonRoundBlindSpots(r BlindSpotReport) BlindSpotReport {
	out := BlindSpotReport{TotalThoughts: r.TotalThoughts, Computed: r.Computed}
	for _, f := range r.Facets {
		cf := BlindSpotFacet{Key: f.Key, Title: f.Title, Groups: f.Groups}
		for _, it := range f.Items {
			it.Influence = canonRound(it.Influence)
			it.Magnitude = canonRound(it.Magnitude)
			it.Consistency = canonRound(it.Consistency)
			cf.Items = append(cf.Items, it)
		}
		out.Facets = append(out.Facets, cf)
	}
	return out
}

// equivLoop builds a struct-literal PropagationLoop wired to gc with a FIXED clock
// (blind-spot recency determinism). cache non-nil → warm resident source; nil →
// drain. baseCtx/clock defaults are covered by baseContext()/clockNow() nil-guards.
func equivLoop(gc Caller, cache *corpusCache) *PropagationLoop {
	fixed := time.Unix(1_700_000_000, 0)
	return &PropagationLoop{gc: gc, corpus: cache, clock: func() time.Time { return fixed }}
}

// cacheFromNodes builds a resident corpusCache holding exactly the given nodes,
// merged through the REAL MergeDelta path (one delta page reproducing the corpus
// state) — so a merge/dedup/tombstone bug in that path shows up in the cached run.
func cacheFromNodes(nodes []*knowledgev1.Node) *corpusCache {
	c := newCorpusCache()
	c.MergeDelta(&knowledgev1.CorpusDeltaResponse{Items: nodes})
	return c
}

// TestCorpusCacheEquivalence_CachedEqualsDrain (criterion e, WIDENED + CANONICALIZED)
// is the load-bearing merge-correctness gate: the reflect output — cluster
// assignments + propagated valence/magnitude + adjacency + topic-label overrides +
// blind-spot report — is IDENTICAL whether the thought-node set is drained from the
// wire or served from the resident cache fed through MergeDelta.
func TestCorpusCacheEquivalence_CachedEqualsDrain(t *testing.T) {
	spec := defaultEquivSpec()

	// DRAIN run: p.corpus nil → CorpusSnapshot warm=false → every consumer drains.
	gcFull := newReflectEquivFake(spec)
	oFull := surfaceFromLoop(t, gcFull, equivLoop(gcFull, nil))

	// CACHED run: same corpus, node set served from the resident cache (fed via the
	// real MergeDelta) — a distinct fake so the drain run's writeback can't leak in.
	gcCached := newReflectEquivFake(spec)
	cache := cacheFromNodes(gcCached.thoughtNodesSlice())
	oCached := surfaceFromLoop(t, gcCached, equivLoop(gcCached, cache))

	// The gate is only meaningful if the surface is non-trivial: real clusters, real
	// propagated values, and a non-empty blind-spot report (so a T2-C-class
	// empty-labels/empty-blind-spots regression would go RED, not silently pass).
	require.Len(t, oFull.clusters, 2, "two persisted clusters in the corpus")
	require.NotEmpty(t, oFull.valence, "propagation produced valence rows")
	require.True(t, hasBlindSpotItems(oFull.blindSpots), "blind-spot report is non-empty (guards the empty-report regression)")
	require.Equal(t, "Topic Alpha", labelOfCluster(oFull.clusters, "cA"), "topic doc overrode cluster cA's label")

	require.Equal(t, oFull, oCached,
		"reflect output must be byte-identical from the resident cache vs a wire drain over the widened canonicalized surface")
}

// TestCorpusCacheEquivalence_PoisonedMergeGoesRed PROVES the equivalence gate can
// FAIL: a poisoned cache feed (one thought dropped from the MergeDelta page, as a
// lost-node merge bug would) diverges the cached surface from the drain. If this
// divergence did NOT show up, the equivalence gate above would be trivially green
// and worthless. This is how the gate is demonstrated RED-capable without leaving a
// failing test behind.
func TestCorpusCacheEquivalence_PoisonedMergeGoesRed(t *testing.T) {
	spec := defaultEquivSpec()

	gcFull := newReflectEquivFake(spec)
	oFull := surfaceFromLoop(t, gcFull, equivLoop(gcFull, nil))

	// POISON: drop the last thought from the cache feed — a lost-node merge fault.
	gcCached := newReflectEquivFake(spec)
	poisoned := gcCached.thoughtNodesSlice()
	poisoned = poisoned[:len(poisoned)-1] // "a3" never lands in the cache.
	oCached := surfaceFromLoop(t, gcCached, equivLoop(gcCached, cacheFromNodes(poisoned)))

	// The dropped node changes cluster cA's membership + the blind-spot set, so the
	// surfaces MUST differ — this is the RED the real gate above catches.
	assert.NotEqual(t, oFull, oCached,
		"a lost-node cache-merge fault MUST diverge the surface — proves the equivalence gate is not trivially green")
	assert.NotEqual(t, oFull.clusters, oCached.clusters,
		"the dropped thought changes cluster cA's membership")
}

func hasBlindSpotItems(r BlindSpotReport) bool {
	for _, f := range r.Facets {
		if len(f.Items) > 0 || len(f.Groups) > 0 {
			return true
		}
	}
	return false
}

func labelOfCluster(cc []canonCluster, id string) string {
	for _, c := range cc {
		if c.ID == id {
			return c.Label
		}
	}
	return ""
}
