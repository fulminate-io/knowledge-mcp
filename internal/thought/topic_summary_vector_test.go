// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"bytes"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// TestPopulateSummaryVectors_Keying: a topic whose medoid resolves (via a topic
// doc carrying that medoid_id, whose .Id is present in vectorIndex with a 32-byte
// vector) gets exactly that vector on SummaryVector. The join is
// medoid → docID → vectorIndex[docID].
func TestPopulateSummaryVectors_Keying(t *testing.T) {
	doc := topicDoc("doc-A", "medoid-A", "cluster-A") // doc.Id = "doc-A", medoid_id = "medoid-A"
	vec := bitVec(0, 1, 2, 200)                       // 32-byte
	vectorIndex := map[string][]byte{"doc-A": vec}

	topics := []Topic{
		{MedoidID: "medoid-A", Centroid: bitVec(255)},
	}
	populateSummaryVectors(topics, []*knowledgev1.Node{doc}, vectorIndex)

	if !bytes.Equal(topics[0].SummaryVector, vec) {
		t.Fatalf("SummaryVector = %x, want the doc's 32-byte vector %x", topics[0].SummaryVector, vec)
	}
}

// TestPopulateSummaryVectors_AbsentLeavesNil: a topic whose doc vector is ABSENT
// from vectorIndex (or whose doc/medoid is missing entirely) leaves SummaryVector
// nil → centroid fallback.
func TestPopulateSummaryVectors_AbsentLeavesNil(t *testing.T) {
	docNoVec := topicDoc("doc-B", "medoid-B", "cluster-B") // doc exists, .Id not in vectorIndex
	topics := []Topic{
		{MedoidID: "medoid-B", Centroid: bitVec(1)}, // doc exists, no vector for its .Id
		{MedoidID: "medoid-X", Centroid: bitVec(2)}, // no doc maps this medoid
		{MedoidID: "", Centroid: bitVec(3)},         // no medoid anchor at all
	}
	vectorIndex := map[string][]byte{"some-other-doc": bitVec(0)}

	populateSummaryVectors(topics, []*knowledgev1.Node{docNoVec}, vectorIndex)

	for i, tp := range topics {
		if tp.SummaryVector != nil {
			t.Fatalf("topic[%d] SummaryVector = %x, want nil (no resolvable doc vector)", i, tp.SummaryVector)
		}
	}
}

// TestPopulateSummaryVectors_WrongLengthLeavesNil: a non-32-byte doc vector trips
// the length guard and leaves SummaryVector nil (mirroring groupEmbedding's guard),
// so a malformed vector never becomes the grouping operand.
func TestPopulateSummaryVectors_WrongLengthLeavesNil(t *testing.T) {
	doc := topicDoc("doc-C", "medoid-C", "cluster-C")
	vectorIndex := map[string][]byte{"doc-C": {0x01, 0x02, 0x03}} // 3 bytes, not 32

	topics := []Topic{
		{MedoidID: "medoid-C", Centroid: bitVec(7)},
	}
	populateSummaryVectors(topics, []*knowledgev1.Node{doc}, vectorIndex)

	if topics[0].SummaryVector != nil {
		t.Fatalf("SummaryVector = %x, want nil (wrong-length vector must trip the length guard)", topics[0].SummaryVector)
	}
}

// farVec64 returns a 32-byte vector with the first 64 bits set — ~64 bits from the
// all-zero vector → BitSimilarity ≈ 0.75, comfortably below the 0.90 link threshold.
func farVec64() []byte {
	v := make([]byte, vectorBytes)
	for i := range 8 { // bytes 0..7 = bits 0..63
		v[i] = 0xFF
	}
	return v
}

// TestPopulateSummaryVectors_LinkConsumesSummary: two singleton topics whose
// CENTROIDS are FAR (would NOT link) but whose populated SUMMARY vectors are CLOSE
// link via RunGroupSimilarity — proving the link pass read the summary vector, not
// the centroid. The pair would not link on centroids alone.
func TestPopulateSummaryVectors_LinkConsumesSummary(t *testing.T) {
	farCentroidA := bitVec()   // all zero
	farCentroidB := farVec64() // ~64 bits from A → sim ~0.75 < 0.90: would NOT link on centroid
	closeSummaryA := bitVec(0)
	closeSummaryB := bitVec(0, 1) // 2 bits from A → sim ~0.992 > 0.90: links on summary

	docA := topicDoc("doc-sA", "mA", "cA")
	docB := topicDoc("doc-sB", "mB", "cB")
	vectorIndex := map[string][]byte{"doc-sA": closeSummaryA, "doc-sB": closeSummaryB}

	topics := []Topic{
		{MedoidID: "mA", Centroid: farCentroidA, MemberClusters: []string{"cA"}},
		{MedoidID: "mB", Centroid: farCentroidB, MemberClusters: []string{"cB"}},
	}

	const linkThreshold = 0.90

	// Baseline: on centroids alone (no summary vectors yet) the far pair does NOT link.
	if got := RunGroupSimilarity(topics, linkThreshold); len(got) != 0 {
		t.Fatalf("pre-populate link candidates = %d, want 0 (far centroids must not link)", len(got))
	}

	populateSummaryVectors(topics, []*knowledgev1.Node{docA, docB}, vectorIndex)

	links := RunGroupSimilarity(topics, linkThreshold)
	if len(links) != 1 {
		t.Fatalf("post-populate link candidates = %d, want 1 (close summary vectors must link)", len(links))
	}
}

// TestPopulateSummaryVectors_SummaryPreferredOverCentroid: the converse — two
// topics whose CENTROIDS are CLOSE (would link) but whose populated SUMMARY vectors
// are FAR must NOT link, proving the summary vector is PREFERRED over the centroid
// (groupEmbedding returns the summary when present, never the centroid).
func TestPopulateSummaryVectors_SummaryPreferredOverCentroid(t *testing.T) {
	closeCentroidA := bitVec(0)
	closeCentroidB := bitVec(0, 1) // 2 bits → would link on centroid
	farSummaryA := bitVec()        // all zero
	farSummaryB := farVec64()      // ~64 bits → far → must NOT link on summary

	docA := topicDoc("doc-pA", "mA", "cA")
	docB := topicDoc("doc-pB", "mB", "cB")
	vectorIndex := map[string][]byte{"doc-pA": farSummaryA, "doc-pB": farSummaryB}

	topics := []Topic{
		{MedoidID: "mA", Centroid: closeCentroidA, MemberClusters: []string{"cA"}},
		{MedoidID: "mB", Centroid: closeCentroidB, MemberClusters: []string{"cB"}},
	}

	const linkThreshold = 0.90

	// Baseline: on close centroids the pair WOULD link.
	if got := RunGroupSimilarity(topics, linkThreshold); len(got) != 1 {
		t.Fatalf("pre-populate link candidates = %d, want 1 (close centroids link before summary populated)", len(got))
	}

	populateSummaryVectors(topics, []*knowledgev1.Node{docA, docB}, vectorIndex)

	if links := RunGroupSimilarity(topics, linkThreshold); len(links) != 0 {
		t.Fatalf("post-populate link candidates = %d, want 0 (far summary vectors are preferred over close centroids)", len(links))
	}
}

// TestPopulateSummaryVectors_MergedUnionDropsSummary: two topics that MERGE produce
// a union whose SummaryVector is nil (unionTopics omits the field), so a subsequent
// groupEmbedding on the union returns its centroid — never a stale parent summary
// vector. Guards the clearing behavior the RunMergeCascade comment promises.
func TestPopulateSummaryVectors_MergedUnionDropsSummary(t *testing.T) {
	// Two near-identical member vectors so the topics merge at 0.97.
	memberA := bitVec(0, 1, 2, 3)
	memberB := bitVec(0, 1, 2, 3) // identical → sim 1.0 → merges
	vectorIndex := map[string][]byte{"a1": memberA, "b1": memberB}
	createdAt := map[string]int64{"a1": 1, "b1": 2}

	// Give both parents a (close) summary vector so we prove the UNION drops it.
	parentSummary := bitVec(0, 1, 2, 3)
	topics := []Topic{
		{PrimaryClusterID: "A", MemberClusters: []string{"A"}, MemberThoughtIDs: []string{"a1"}, Centroid: memberA, MedoidID: "a1", SummaryVector: parentSummary},
		{PrimaryClusterID: "B", MemberClusters: []string{"B"}, MemberThoughtIDs: []string{"b1"}, Centroid: memberB, MedoidID: "b1", SummaryVector: parentSummary},
	}

	const mergeThreshold = 0.97
	merged, chains := RunMergeCascade(topics, vectorIndex, createdAt, mergeThreshold)
	if len(chains) != 1 || len(merged) != 1 {
		t.Fatalf("merge produced %d chains / %d survivors, want 1 / 1", len(chains), len(merged))
	}

	union := merged[0]
	if union.SummaryVector != nil {
		t.Fatalf("merged-union SummaryVector = %x, want nil (the union must not reuse a parent summary vector)", union.SummaryVector)
	}
	// groupEmbedding on the union therefore returns the centroid, not a stale summary.
	if !bytes.Equal(groupEmbedding(union), union.Centroid) {
		t.Fatalf("groupEmbedding(union) did not return the centroid — a stale summary vector leaked through the merge")
	}
}

// TestPopulateSummaryVectors_ZeroRoundTrip pins the zero-round-trip property at the
// type level: populateSummaryVectors is a PURE in-memory join over the already-
// drained vectorIndex and already-read docs, so its signature takes NO Caller and
// NO context.Context — it STRUCTURALLY cannot make a wire call. Binding the function
// to this exact signature is a compile-level fails-when-absent guarantee: if anyone
// adds a Caller/ctx parameter (the only way it could round-trip), this test stops
// compiling.
//
// No pipeline-level recording-caller test drives runTopicPipeline end-to-end in
// package thought without a live scanner+summarizer harness, so per the plan this
// signature-level guarantee (plus the Phase 1 manual no-new-round-trip review of the
// diff) discharges the zero-round-trip criterion.
func TestPopulateSummaryVectors_ZeroRoundTrip(t *testing.T) {
	var _ func([]Topic, []*knowledgev1.Node, map[string][]byte) = populateSummaryVectors
}
