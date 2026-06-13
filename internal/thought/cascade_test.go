// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"reflect"
	"slices"
	"testing"
)

// setBits returns a 32-byte vector with the given bit ranges/indices set.
func setRange(v []byte, from, to int) {
	for i := from; i <= to; i++ {
		v[i/8] |= 1 << uint(i%8)
	}
}

func newVec(ranges ...[2]int) []byte {
	v := make([]byte, vectorBytes)
	for _, r := range ranges {
		setRange(v, r[0], r[1])
	}
	return v
}

// TestRunMergeCascade_ABCFixpoint is the crux test: A and B merge first, and the
// recomputed A∪B centroid newly clears the threshold against C (a cascade) even
// though neither A nor B individually cleared C. A far D is untouched.
//
// Construction (per the audit's asymmetric-member-count mechanism): B has four
// members that SPLIT 2-2 on a swing-bit block, so B's own centroid clears those
// swing bits to 0 (a tie). A's three members all SET the swing bits (matching C),
// so the A∪B majority flips the swing block toward C — moving the union centroid
// close enough to C to clear the merge threshold.
func TestRunMergeCascade_ABCFixpoint(t *testing.T) {
	// Bit layout over 256 bits (merge = 0.80 ↔ <=51 differing bits):
	//   match = [0,149]   : A, B, C all set → shared agreement (150 bits).
	//   extra = [160,199] : A and B BOTH set, C lacks (40 bits) → makes A,B
	//                       individually FAR from C (sim 0.785 < 0.80 merge).
	//   gA    = [220,234] : A sets, B lacks (15 bits)  ┐ A and B DISAGREE here; an
	//   gB    = [235,249] : B sets, A lacks (15 bits)  ┘ even-member union CLEARS
	//                       both blocks (tie→0). After clearing, the union differs
	//                       from C only by `extra` (40 bits → sim 0.844 >= 0.80) —
	//                       the CASCADE: the union clears C though neither parent did.
	match := [2]int{0, 149}
	extra := [2]int{160, 199}
	gA := [2]int{220, 234}
	gB := [2]int{235, 249}

	// A: 3 members, all = match ∪ extra ∪ gA.
	aMember := newVec(match, extra, gA)
	aMembers := []string{"a1", "a2", "a3"}

	// B: 3 members, all = match ∪ extra ∪ gB.
	bMember := newVec(match, extra, gB)
	bMembers := []string{"b1", "b2", "b3"}

	// C: 3 members, all = match only. Each of A,B differs from C by extra(40) +
	// its own g-block(15) = 55 bits → sim 0.785 < 0.80, so A,C and B,C do NOT
	// merge directly; only the cleared union (40 bits, sim 0.844) clears C.
	cMember := newVec(match)
	cMembers := []string{"c1", "c2", "c3"}

	// D: 3 members, a disjoint far block (no overlap with match/extra/g) — never merges.
	dMember := newVec([2]int{250, 255})
	dMembers := []string{"d1", "d2", "d3"}

	vectorIndex := map[string][]byte{
		"a1": aMember, "a2": aMember, "a3": aMember,
		"b1": bMember, "b2": bMember, "b3": bMember,
		"c1": cMember, "c2": cMember, "c3": cMember,
		"d1": dMember, "d2": dMember, "d3": dMember,
	}
	createdAt := map[string]int64{
		"a1": 10, "a2": 11, "a3": 12,
		"b1": 20, "b2": 21, "b3": 23,
		"c1": 30, "c2": 31, "c3": 32,
		"d1": 40, "d2": 41, "d3": 42,
	}

	mkTopic := func(label string, members []string) Topic {
		vecs := make([][]byte, len(members))
		for i, id := range members {
			vecs[i] = vectorIndex[id]
		}
		centroid := BitMajorityCentroid(vecs)
		return Topic{
			PrimaryClusterID: label,
			MemberClusters:   []string{label},
			MemberThoughtIDs: members,
			Centroid:         centroid,
			MedoidID:         members[0],
			CreatedAt:        createdAt[members[0]],
		}
	}

	A := mkTopic("A", aMembers)
	B := mkTopic("B", bMembers)
	C := mkTopic("C", cMembers)
	D := mkTopic("D", dMembers)

	const merge = 0.80

	// --- Precondition asserts: prove the fixture actually exhibits the cascade. ---
	simAB := BitSimilarity(A.Centroid, B.Centroid)
	simAC := BitSimilarity(A.Centroid, C.Centroid)
	simBC := BitSimilarity(B.Centroid, C.Centroid)
	abCentroid := BitMajorityCentroid([][]byte{aMember, aMember, aMember, bMember, bMember, bMember})
	simABxC := BitSimilarity(abCentroid, C.Centroid)
	t.Logf("sim(A,B)=%.4f sim(A,C)=%.4f sim(B,C)=%.4f sim(A∪B,C)=%.4f (merge=%.2f)", simAB, simAC, simBC, simABxC, merge)

	if simAB < merge {
		t.Fatalf("fixture: sim(A,B)=%.4f must be >= merge %.2f (A,B merge first)", simAB, merge)
	}
	if simAC >= merge {
		t.Fatalf("fixture: sim(A,C)=%.4f must be < merge %.2f", simAC, merge)
	}
	if simBC >= merge {
		t.Fatalf("fixture: sim(B,C)=%.4f must be < merge %.2f", simBC, merge)
	}
	if simABxC < merge {
		t.Fatalf("fixture: sim(A∪B,C)=%.4f must be >= merge %.2f (the cascade)", simABxC, merge)
	}

	// --- Run the cascade. ---
	merged, chains := RunMergeCascade([]Topic{A, B, C, D}, vectorIndex, createdAt, merge)

	// Final: one merged topic spanning {A,B,C} + D untouched → 2 topics.
	if len(merged) != 2 {
		t.Fatalf("merged topics = %d, want 2 (ABC + D)", len(merged))
	}
	var abc, dd *Topic
	for i := range merged {
		if slices.Contains(merged[i].MemberClusters, "A") {
			abc = &merged[i]
		}
		if reflect.DeepEqual(merged[i].MemberClusters, []string{"D"}) {
			dd = &merged[i]
		}
	}
	if abc == nil {
		t.Fatalf("no merged topic spanning A; got %+v", topicLabels(merged))
	}
	if dd == nil {
		t.Fatalf("D was disturbed; got %+v", topicLabels(merged))
	}
	wantMembers := []string{"A", "B", "C"}
	if !reflect.DeepEqual(abc.MemberClusters, wantMembers) {
		t.Fatalf("ABC member_clusters = %v, want %v", abc.MemberClusters, wantMembers)
	}

	// Chain: [A+B → AB, AB+C → ABC] (two merges).
	if len(chains) != 2 {
		t.Fatalf("merge chains = %d, want 2 (A+B, then AB+C)", len(chains))
	}

	// Survivor medoid is bit-closest to the final centroid.
	wantMedoid := medoidForUnion(abc.MemberThoughtIDs, vectorIndex, createdAt, abc.Centroid)
	if abc.MedoidID != wantMedoid {
		t.Fatalf("survivor medoid = %q, want bit-closest %q", abc.MedoidID, wantMedoid)
	}

	// The final centroid is BitMajorityCentroid over the FULL union member set (NOT
	// an average of the two source centroids) — re-derive it and compare.
	fullVecs := make([][]byte, 0, len(abc.MemberThoughtIDs))
	for _, id := range abc.MemberThoughtIDs {
		fullVecs = append(fullVecs, vectorIndex[id])
	}
	if !equalBytes(abc.Centroid, BitMajorityCentroid(fullVecs)) {
		t.Fatalf("ABC centroid is not the bit-majority over the full union members")
	}

	// --- Reproducibility: a second run yields an identical chain. ---
	_, chains2 := RunMergeCascade([]Topic{A, B, C, D}, vectorIndex, createdAt, merge)
	if !reflect.DeepEqual(chains, chains2) {
		t.Fatalf("merge chain not reproducible:\n run1=%+v\n run2=%+v", chains, chains2)
	}
}

// TestRunMergeCascade_NoMerge: when no pair clears the threshold the input is
// returned unchanged with zero merges.
func TestRunMergeCascade_NoMerge(t *testing.T) {
	v1 := newVec([2]int{0, 49})
	v2 := newVec([2]int{100, 149})
	v3 := newVec([2]int{200, 249})
	idx := map[string][]byte{"x": v1, "y": v2, "z": v3}
	ca := map[string]int64{"x": 1, "y": 2, "z": 3}
	topics := []Topic{
		{PrimaryClusterID: "X", MemberClusters: []string{"X"}, MemberThoughtIDs: []string{"x"}, Centroid: v1, MedoidID: "x"},
		{PrimaryClusterID: "Y", MemberClusters: []string{"Y"}, MemberThoughtIDs: []string{"y"}, Centroid: v2, MedoidID: "y"},
		{PrimaryClusterID: "Z", MemberClusters: []string{"Z"}, MemberThoughtIDs: []string{"z"}, Centroid: v3, MedoidID: "z"},
	}
	merged, chains := RunMergeCascade(topics, idx, ca, 0.90)
	if len(merged) != 3 {
		t.Fatalf("no-merge: merged = %d, want 3 unchanged", len(merged))
	}
	if len(chains) != 0 {
		t.Fatalf("no-merge: chains = %d, want 0", len(chains))
	}
}

// TestRunMergeCascade_AllAbove: a 3-topic fixture all pairwise above threshold
// collapses to one topic and halts (count strictly decreases each iteration).
func TestRunMergeCascade_AllAbove(t *testing.T) {
	v := newVec([2]int{0, 99}) // identical → sim 1.0 pairwise
	idx := map[string][]byte{"p": v, "q": v, "r": v}
	ca := map[string]int64{"p": 1, "q": 2, "r": 3}
	topics := []Topic{
		{PrimaryClusterID: "P", MemberClusters: []string{"P"}, MemberThoughtIDs: []string{"p"}, Centroid: v, MedoidID: "p"},
		{PrimaryClusterID: "Q", MemberClusters: []string{"Q"}, MemberThoughtIDs: []string{"q"}, Centroid: v, MedoidID: "q"},
		{PrimaryClusterID: "R", MemberClusters: []string{"R"}, MemberThoughtIDs: []string{"r"}, Centroid: v, MedoidID: "r"},
	}
	merged, chains := RunMergeCascade(topics, idx, ca, 0.90)
	if len(merged) != 1 {
		t.Fatalf("all-above: merged = %d, want 1 collapsed topic", len(merged))
	}
	if len(chains) != 2 {
		t.Fatalf("all-above: chains = %d, want 2 (3→2→1)", len(chains))
	}
	if !reflect.DeepEqual(merged[0].MemberClusters, []string{"P", "Q", "R"}) {
		t.Fatalf("all-above: member_clusters = %v, want [P Q R]", merged[0].MemberClusters)
	}
}

func topicLabels(ts []Topic) [][]string {
	out := make([][]string, len(ts))
	for i, t := range ts {
		out[i] = t.MemberClusters
	}
	return out
}
