// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"sync/atomic"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// countingEdgeCaller serves ONE RETURN_MODE_EDGES response (the seeded edges) and
// records how many edge reads were issued, so a test can assert buildLeafProvenance
// makes exactly one bulk read regardless of leaf count.
type countingEdgeCaller struct {
	edges     []*knowledgev1.Edge
	edgeReads atomic.Int64
}

func (c *countingEdgeCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	if q != nil && q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		c.edgeReads.Add(1)
		return &knowledgev1.ExecuteResponse{Edges: c.edges}, nil
	}
	return &knowledgev1.ExecuteResponse{}, nil
}

// vec builds a 32-byte (256-bit) vector from a 0..255 fill seed pattern: byte i is
// (seed XOR i). Distinct seeds yield distinct, deterministic vectors; the same seed
// yields an identical vector (BitSimilarity 1.0 against itself).
func vec(seed byte) []byte {
	v := make([]byte, vectorBytes)
	for i := range v {
		v[i] = seed ^ byte(i)
	}
	return v
}

// vecBitsFrom returns a 32-byte vector equal to base but with `flip` of its LOW
// bits inverted, so BitSimilarity(base, result) == 1 - flip/256 — a knob to hit a
// target similarity precisely. flip in [0,256].
func vecBitsFrom(base []byte, flip int) []byte {
	out := make([]byte, vectorBytes)
	copy(out, base)
	for i := range flip {
		out[i/8] ^= 1 << uint(i%8)
	}
	return out
}

// singletonState returns communityOf+commSize where every listed id is its own
// singleton community.
func singletonState(ids ...string) (map[string]string, map[string]int) {
	co := map[string]string{}
	cs := map[string]int{}
	for _, id := range ids {
		co[id] = id
		cs[id] = 1
	}
	return co, cs
}

// withCluster adds a non-singleton community `comm` containing members to the maps.
func withCluster(co map[string]string, cs map[string]int, comm string, members ...string) {
	for _, m := range members {
		co[m] = comm
	}
	cs[comm] = len(members)
}

// realProv builds a leaf→neighbor→provenance map marking the given (leaf,neighbor)
// pairs as a real-link edge.
func realProv(pairs ...[2]string) map[string]map[string]string {
	out := map[string]map[string]string{}
	for _, p := range pairs {
		if out[p[0]] == nil {
			out[p[0]] = map[string]string{}
		}
		out[p[0]][p[1]] = "real-link"
	}
	return out
}

// TestBuildLeafProvenance_OneReadAndClassify (FAILS-WHEN-ABSENT) asserts
// buildLeafProvenance issues exactly ONE RETURN_MODE_EDGES read regardless of leaf
// count and classifies tree-link / topic-densify / topic-similarity / bare
// relates-to edges as tree-link / densify / topic-similarity / real-link.
func TestBuildLeafProvenance_OneReadAndClassify(t *testing.T) {
	leafIDs := []string{"L1", "L2", "L3", "L4"}
	caller := &countingEdgeCaller{edges: []*knowledgev1.Edge{
		{FromId: "L1", ToId: "T", Type: "relates-to", Method: treeLinkMethod},
		{FromId: "L2", ToId: "T", Type: "relates-to", Method: densifyMethod},
		{FromId: "L3", ToId: "T", Type: "relates-to", Method: topicSimilarityMethod},
		{FromId: "L4", ToId: "T", Type: "relates-to", Method: ""}, // bare → real-link
	}}
	prov := buildLeafProvenance(context.Background(), caller, leafIDs)

	if got := caller.edgeReads.Load(); got != 1 {
		t.Fatalf("buildLeafProvenance issued %d edge reads, want exactly 1", got)
	}
	cases := map[string]string{"L1": "tree-link", "L2": "densify", "L3": "topic-similarity", "L4": "real-link"}
	for leaf, want := range cases {
		if got := prov[leaf]["T"]; got != want {
			t.Fatalf("provenance[%s][T] = %q, want %q", leaf, got, want)
		}
	}
}

// TestBuildLeafProvenance_SessionByElimination (FAILS-WHEN-ABSENT) asserts a leaf
// reachable to a cluster ONLY via a neighbor with NO backing edge is tagged
// session-sibling by elimination in the attach pass — it gates at 0.80 and tallies
// byProvenance["session-sibling"] on attach. (The provenance map omits the
// neighbor; the attach pass supplies the elimination tag.)
func TestBuildLeafProvenance_SessionByElimination(t *testing.T) {
	// Edge read returns NOTHING for the leaf→sibling pair (session sibling has no edge).
	caller := &countingEdgeCaller{edges: nil}
	prov := buildLeafProvenance(context.Background(), caller, []string{"leaf"})
	if _, present := prov["leaf"]["m1"]; present {
		t.Fatalf("session-sibling neighbor unexpectedly present in provenance map: %v", prov)
	}

	// Now drive the attach pass: leaf reaches cluster C only via m1 (no edge),
	// sim 0.85 >= 0.80 session gate → attaches and tallies session-sibling.
	co, cs := singletonState("leaf")
	withCluster(co, cs, "C", "m1", "m2")
	centroid := vec(90)
	leafVec := vecBitsFrom(centroid, 38) // sim ≈ 0.851.
	vi := map[string][]byte{"leaf": leafVec, "m1": centroid, "m2": centroid}
	adj2 := map[string][]string{"leaf": {"m1"}, "m1": {"leaf", "m2"}, "m2": {"m1"}}
	stats := attachLeaves(co, cs, adj2, vi, prov)
	if stats.byProvenance[provenanceSessionSibling] != 1 {
		t.Fatalf("byProvenance=%v, want 1 session-sibling attach by elimination", stats.byProvenance)
	}
}

// TestLeafAttach_GateConstsTwoTier locks the two-tier gate constant values.
func TestLeafAttach_GateConstsTwoTier(t *testing.T) {
	if leafAttachGateLinked != 0.60 {
		t.Fatalf("leafAttachGateLinked = %v, want 0.60", leafAttachGateLinked)
	}
	if leafAttachGateSession != 0.80 {
		t.Fatalf("leafAttachGateSession = %v, want 0.80", leafAttachGateSession)
	}
}

// TestLeafAttach_OnlySingletonsAreCandidates: a node in a 2+ member community is
// never a candidate; only communityOf[id]==id && commSize[id]==1 nodes are.
func TestLeafAttach_OnlySingletonsAreCandidates(t *testing.T) {
	co, cs := singletonState("leaf")
	withCluster(co, cs, "C", "m1", "m2") // m1,m2 are members, not candidates.
	base := vec(10)
	vi := map[string][]byte{"leaf": base, "m1": base, "m2": base}
	adj := map[string][]string{"leaf": {"m1"}, "m1": {"leaf", "m2"}, "m2": {"m1"}}

	stats := attachLeaves(co, cs, adj, vi, realProv([2]string{"leaf", "m1"}))

	if stats.candidates != 1 {
		t.Fatalf("candidates = %d, want 1 (only the singleton leaf)", stats.candidates)
	}
	// m1/m2 must keep their community C regardless.
	if co["m1"] != "C" || co["m2"] != "C" {
		t.Fatalf("cluster members reassigned: m1=%q m2=%q want C", co["m1"], co["m2"])
	}
}

// TestLeafAttach_LinkedGateRealEdge: a singleton reached via a REAL edge to a
// cluster at sim 0.7 attaches (>=0.60 linked gate).
func TestLeafAttach_LinkedGateRealEdge(t *testing.T) {
	co, cs := singletonState("leaf")
	withCluster(co, cs, "C", "m1", "m2")
	centroid := vec(20)
	// member vectors set so the cluster bit-majority centroid == `centroid`.
	leafVec := vecBitsFrom(centroid, 77) // sim = 1 - 77/256 ≈ 0.699.
	vi := map[string][]byte{"leaf": leafVec, "m1": centroid, "m2": centroid}
	adj := map[string][]string{"leaf": {"m1"}, "m1": {"leaf", "m2"}, "m2": {"m1"}}

	stats := attachLeaves(co, cs, adj, vi, realProv([2]string{"leaf", "m1"}))

	if co["leaf"] != "C" {
		t.Fatalf("leaf community = %q, want C (real edge, sim≈0.70 >= 0.60)", co["leaf"])
	}
	if stats.attached != 1 || stats.byProvenance["real-link"] != 1 {
		t.Fatalf("attached=%d byProvenance=%v, want 1 attach via real-link", stats.attached, stats.byProvenance)
	}
	if cs["C"] != 3 || cs["leaf"] != 0 {
		t.Fatalf("commSize C=%d leaf=%d, want C=3 leaf=0", cs["C"], cs["leaf"])
	}
}

// TestLeafAttach_SessionGateVetoesSameSim: the SAME 0.7 similarity reached ONLY via
// a session-sibling (no backing edge) stays a singleton (<0.80 session gate).
func TestLeafAttach_SessionGateVetoesSameSim(t *testing.T) {
	co, cs := singletonState("leaf")
	withCluster(co, cs, "C", "m1", "m2")
	centroid := vec(20)
	leafVec := vecBitsFrom(centroid, 77) // sim ≈ 0.699 < 0.80.
	vi := map[string][]byte{"leaf": leafVec, "m1": centroid, "m2": centroid}
	adj := map[string][]string{"leaf": {"m1"}, "m1": {"leaf", "m2"}, "m2": {"m1"}}

	// NO provenance entry for leaf→m1 → session-sibling by elimination.
	stats := attachLeaves(co, cs, adj, vi, map[string]map[string]string{})

	if co["leaf"] != "leaf" {
		t.Fatalf("leaf community = %q, want unchanged 'leaf' (session-only sim 0.70 < 0.80)", co["leaf"])
	}
	if stats.attached != 0 || stats.gateVetoed != 1 {
		t.Fatalf("attached=%d gateVetoed=%d, want 0 attach / 1 veto", stats.attached, stats.gateVetoed)
	}
}

// TestLeafAttach_SessionGateAttachesAbove: a session-only candidate at sim 0.85
// attaches (>=0.80) and tallies session-sibling provenance.
func TestLeafAttach_SessionGateAttachesAbove(t *testing.T) {
	co, cs := singletonState("leaf")
	withCluster(co, cs, "C", "m1", "m2")
	centroid := vec(30)
	leafVec := vecBitsFrom(centroid, 38) // sim = 1 - 38/256 ≈ 0.851.
	vi := map[string][]byte{"leaf": leafVec, "m1": centroid, "m2": centroid}
	adj := map[string][]string{"leaf": {"m1"}, "m1": {"leaf", "m2"}, "m2": {"m1"}}

	stats := attachLeaves(co, cs, adj, vi, map[string]map[string]string{})

	if co["leaf"] != "C" {
		t.Fatalf("leaf community = %q, want C (session sim≈0.85 >= 0.80)", co["leaf"])
	}
	if stats.byProvenance[provenanceSessionSibling] != 1 {
		t.Fatalf("byProvenance=%v, want 1 session-sibling attach", stats.byProvenance)
	}
}

// TestLeafAttach_LowerGateWinsBothProvenances: a leaf reaching the SAME cluster via
// both a session-sibling and a real edge gates at 0.60 and records the real
// provenance.
func TestLeafAttach_LowerGateWinsBothProvenances(t *testing.T) {
	co, cs := singletonState("leaf")
	// Cluster C has two members; leaf has a real edge to m1 and a session-only
	// neighbor m2, BOTH in C.
	withCluster(co, cs, "C", "m1", "m2")
	centroid := vec(40)
	leafVec := vecBitsFrom(centroid, 77) // sim ≈ 0.699 — passes 0.60, fails 0.80.
	vi := map[string][]byte{"leaf": leafVec, "m1": centroid, "m2": centroid}
	adj := map[string][]string{"leaf": {"m1", "m2"}, "m1": {"leaf", "m2"}, "m2": {"leaf", "m1"}}

	// Only leaf→m1 is a real edge; leaf→m2 is session-sibling by elimination.
	stats := attachLeaves(co, cs, adj, vi, realProv([2]string{"leaf", "m1"}))

	if co["leaf"] != "C" {
		t.Fatalf("leaf community = %q, want C (lower 0.60 gate wins via the real edge)", co["leaf"])
	}
	if stats.byProvenance["real-link"] != 1 {
		t.Fatalf("byProvenance=%v, want the winning provenance to be real-link", stats.byProvenance)
	}
}

// TestLeafAttach_TieBreakLowestClusterID: equal similarity to two clusters → lowest
// cluster id; unequal → higher-sim cluster regardless of id order.
func TestLeafAttach_TieBreakLowestClusterID(t *testing.T) {
	// EQUAL sim case: both clusters share the SAME centroid, so sim is identical.
	co, cs := singletonState("leaf")
	withCluster(co, cs, "Cb", "b1", "b2")
	withCluster(co, cs, "Ca", "a1", "a2")
	centroid := vec(50)
	leafVec := vecBitsFrom(centroid, 30) // sim ≈ 0.883 — passes 0.60.
	vi := map[string][]byte{
		"leaf": leafVec,
		"a1":   centroid, "a2": centroid,
		"b1": centroid, "b2": centroid,
	}
	adj := map[string][]string{
		"leaf": {"a1", "b1"},
		"a1":   {"leaf", "a2"}, "a2": {"a1"},
		"b1": {"leaf", "b2"}, "b2": {"b1"},
	}
	attachLeaves(co, cs, adj, vi, realProv([2]string{"leaf", "a1"}, [2]string{"leaf", "b1"}))
	if co["leaf"] != "Ca" {
		t.Fatalf("equal-sim tie: leaf community = %q, want lowest id Ca", co["leaf"])
	}

	// UNEQUAL sim case: Cb's centroid is closer to the leaf than Ca's, despite Ca
	// sorting first. Higher sim must win regardless of id order.
	co2, cs2 := singletonState("leaf")
	withCluster(co2, cs2, "Ca", "a1", "a2")
	withCluster(co2, cs2, "Cb", "b1", "b2")
	near := vecBitsFrom(leafVec, 5) // very close to leaf → high sim.
	far := vecBitsFrom(leafVec, 60) // farther → lower sim (still >0.60).
	vi2 := map[string][]byte{
		"leaf": leafVec,
		"a1":   far, "a2": far,
		"b1": near, "b2": near,
	}
	attachLeaves(co2, cs2, adj, vi2, realProv([2]string{"leaf", "a1"}, [2]string{"leaf", "b1"}))
	if co2["leaf"] != "Cb" {
		t.Fatalf("unequal-sim: leaf community = %q, want higher-sim Cb regardless of id order", co2["leaf"])
	}
}

// TestLeafAttach_VectorlessSkipped: a candidate with no vector (or a non-32-byte
// vector) is never attached, increments vectorlessSkipped, and communityOf is
// unchanged.
func TestLeafAttach_VectorlessSkipped(t *testing.T) {
	co, cs := singletonState("noVec", "shortVec")
	withCluster(co, cs, "C", "m1", "m2")
	centroid := vec(60)
	vi := map[string][]byte{
		"m1": centroid, "m2": centroid,
		"shortVec": {0x01, 0x02}, // non-32-byte.
		// noVec absent entirely.
	}
	adj := map[string][]string{
		"noVec":    {"m1"},
		"shortVec": {"m1"},
		"m1":       {"noVec", "shortVec", "m2"}, "m2": {"m1"},
	}
	stats := attachLeaves(co, cs, adj, vi, realProv([2]string{"noVec", "m1"}, [2]string{"shortVec", "m1"}))

	if co["noVec"] != "noVec" || co["shortVec"] != "shortVec" {
		t.Fatalf("vector-less leaves attached: noVec=%q shortVec=%q", co["noVec"], co["shortVec"])
	}
	if stats.vectorlessSkipped != 2 || stats.attached != 0 {
		t.Fatalf("vectorlessSkipped=%d attached=%d, want 2/0", stats.vectorlessSkipped, stats.attached)
	}
}

// TestLeafAttach_NonRecursive: in chain A(clustered)-B(singleton)-C(singleton), ONE
// attachLeaves pass attaches B (its edge to the clustered A passes the gate) but
// leaves C a singleton — C's only non-singleton neighbor would be B, which is still
// a singleton at snapshot time. Proves a single layer per pass (settled design 5).
func TestLeafAttach_NonRecursive(t *testing.T) {
	// A is a 2-member cluster CA (so it is a real non-singleton target). B and C are
	// singletons. Edges: B-A1 (B can attach to CA), C-B (C's only neighbor is B).
	co, cs := singletonState("B", "C")
	withCluster(co, cs, "CA", "A1", "A2")
	centroid := vec(100)
	near := vecBitsFrom(centroid, 10) // sim ≈ 0.96 — passes either gate.
	vi := map[string][]byte{
		"B": near, "C": near,
		"A1": centroid, "A2": centroid,
	}
	adj := map[string][]string{
		"B":  {"A1", "C"},
		"C":  {"B"},
		"A1": {"A2", "B"}, "A2": {"A1"},
	}
	stats := attachLeaves(co, cs, adj, vi, realProv(
		[2]string{"B", "A1"}, [2]string{"B", "C"}, [2]string{"C", "B"},
	))

	if co["B"] != "CA" {
		t.Fatalf("B community = %q, want CA (B attaches this pass)", co["B"])
	}
	if co["C"] != "C" {
		t.Fatalf("C community = %q, want unchanged 'C' — C must NOT attach this pass (B was a singleton at snapshot)", co["C"])
	}
	if stats.attached != 1 {
		t.Fatalf("attached = %d, want exactly 1 (single layer per pass)", stats.attached)
	}
}

// TestLeafAttach_PairUnaffected: a 2-member community's members are never candidates
// and retain their community after attachLeaves (pairs hold under CPM, settled census).
func TestLeafAttach_PairUnaffected(t *testing.T) {
	co, cs := singletonState() // no singletons.
	withCluster(co, cs, "P", "p1", "p2")
	centroid := vec(110)
	vi := map[string][]byte{"p1": centroid, "p2": centroid}
	adj := map[string][]string{"p1": {"p2"}, "p2": {"p1"}}

	stats := attachLeaves(co, cs, adj, vi, map[string]map[string]string{})

	if stats.candidates != 0 {
		t.Fatalf("candidates = %d, want 0 — a 2-member community has no singleton leaves", stats.candidates)
	}
	if co["p1"] != "P" || co["p2"] != "P" {
		t.Fatalf("pair members reassigned: p1=%q p2=%q, want P", co["p1"], co["p2"])
	}
}

// TestLeafAttach_NilCentroidClusterAttachesNoLeaf: a target cluster with zero
// member vectors has a nil centroid and attaches no leaf (BitSimilarity vs nil ==
// 0 < either gate). Asserts ComputeClusterCentroids reuse self-vetoes cleanly.
func TestLeafAttach_NilCentroidClusterAttachesNoLeaf(t *testing.T) {
	co, cs := singletonState("leaf")
	withCluster(co, cs, "C", "m1", "m2")
	// Only the leaf has a vector; cluster members have NONE → nil centroid.
	vi := map[string][]byte{"leaf": vec(70)}
	adj := map[string][]string{"leaf": {"m1"}, "m1": {"leaf", "m2"}, "m2": {"m1"}}

	stats := attachLeaves(co, cs, adj, vi, realProv([2]string{"leaf", "m1"}))

	if co["leaf"] != "leaf" {
		t.Fatalf("leaf attached to a nil-centroid cluster: community = %q", co["leaf"])
	}
	if stats.attached != 0 || stats.gateVetoed != 1 {
		t.Fatalf("attached=%d gateVetoed=%d, want 0/1 (nil centroid self-vetoes)", stats.attached, stats.gateVetoed)
	}
}
