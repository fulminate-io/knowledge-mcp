// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// updateRecordingCaller records MUTATION_KIND_UPDATE calls (the drift re-summary
// doc update) by target id, capturing both the set fields (name/description) and the
// set metadata (cluster_id / medoid_id / topic_centroid / member_clusters) so a test
// can assert the refreshed stored centroid anchor.
type updateRecordingCaller struct {
	updates  map[string]map[string]string // doc id → set_fields (name/description)
	metadata map[string]map[string]string // doc id → set_metadata (provenance keys)
}

func (c *updateRecordingCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	m := req.GetMutation()
	if m != nil && m.GetKind() == knowledgev1.MutationPlan_MUTATION_KIND_UPDATE {
		if c.updates == nil {
			c.updates = map[string]map[string]string{}
			c.metadata = map[string]map[string]string{}
		}
		for _, id := range m.GetSelection().GetIds() {
			c.updates[id] = m.GetSetFields()
			c.metadata[id] = m.GetSetMetadata()
		}
	}
	return &knowledgev1.ExecuteResponse{}, nil
}

// driftDoc builds a topic `document` node carrying the durable medoid anchor AND the
// stored topic_centroid (the centroid-as-of-last-summary the drift metric compares
// the live centroid against). storedCentroid is the binary centroid; it is hex-encoded
// into the metadata exactly as the create / drift-refresh write paths persist it.
func driftDoc(id, medoid, summaryText string, storedCentroid []byte) *knowledgev1.Node {
	n := &knowledgev1.Node{Id: id, Type: string(kgtypes.NodeDocument), Description: summaryText}
	kgtypes.SetValue(n, metaMedoidID, medoid)
	kgtypes.SetValue(n, metaTopicCentroid, encodeCentroid(storedCentroid))
	return n
}

// allBitsSet returns a 32-byte vector with every bit set (256 bits).
func allBitsSet() []byte {
	v := make([]byte, vectorBytes)
	for i := range v {
		v[i] = 0xFF
	}
	return v
}

// TestDecodeCentroid round-trips encodeCentroid and pins the degraded directions:
// empty and non-hex inputs decode to nil (→ BitSimilarity 0 → drift 1.0 → one
// self-healing refresh).
func TestDecodeCentroid(t *testing.T) {
	v := bitVec(0, 1, 2, 3)
	if got := decodeCentroid(encodeCentroid(v)); string(got) != string(v) {
		t.Fatalf("decodeCentroid(encodeCentroid(v)) = %x, want %x", got, v)
	}
	if got := decodeCentroid(""); got != nil {
		t.Fatalf(`decodeCentroid("") = %x, want nil`, got)
	}
	if got := decodeCentroid("zz"); got != nil {
		t.Fatalf(`decodeCentroid("zz") = %x, want nil (non-hex)`, got)
	}
}

// TestTopicDrift anchors the metric to the STORED topic_centroid: a topic whose live
// centroid equals its stored anchor yields drift 0 and is NOT refreshed (THE
// OSCILLATION REGRESSION — this must fail if the metric reverts to comparing the live
// centroid against a re-embedding of the summary text); a topic whose live centroid
// moved past topicDriftBound from the stored anchor is re-summarized AND its update
// re-anchors the stored topic_centroid metadata to the new live centroid.
func TestTopicDrift(t *testing.T) {
	// "stable" doc: stored centroid == live centroid → drift 0 → within bound.
	// "drifty" doc: stored centroid all-ones, live centroid all-zero → BitSimilarity 0
	// → drift 1.0 (> 0.20).
	stableCentroid := bitVec(0, 1, 2, 3)
	driftyLiveCentroid := make([]byte, vectorBytes) // all zero
	driftyStoredCentroid := allBitsSet()            // far from the all-zero live centroid

	stable := driftDoc("doc-stable", "m-stable", "stable summary text", stableCentroid)
	drifty := driftDoc("doc-drifty", "m-drifty", "drifty summary text", driftyStoredCentroid)

	topicByMedoid := map[string]Topic{
		"m-stable": {PrimaryClusterID: "cidStable", MemberClusters: []string{"cidStable"}, Centroid: stableCentroid, MedoidID: "m-stable", SummaryContent: "stable content"},
		"m-drifty": {PrimaryClusterID: "cidDrifty", MemberClusters: []string{"cidDrifty"}, Centroid: driftyLiveCentroid, MedoidID: "m-drifty", SummaryContent: "drifty content"},
	}

	sum := &stubTopicSummarizer{}
	rec := &updateRecordingCaller{}

	rep, err := driftTopicDocs(context.Background(), rec, []*knowledgev1.Node{stable, drifty}, topicByMedoid, sum)
	if err != nil {
		t.Fatalf("drift error: %v", err)
	}

	if rep.Checked != 2 {
		t.Fatalf("Checked = %d, want 2", rep.Checked)
	}
	if rep.Resummaried != 1 {
		t.Fatalf("Resummaried = %d, want 1 (only the drifty doc)", rep.Resummaried)
	}
	if sum.calls != 1 {
		t.Fatalf("SummarizeTopics calls = %d, want 1 (one re-summary pass over drifted only)", sum.calls)
	}
	// THE OSCILLATION REGRESSION GUARD: an unchanged-centroid topic must NOT refresh.
	if _, ok := rec.updates["doc-stable"]; ok {
		t.Fatalf("unchanged-centroid stable doc must NOT be updated (drift 0) — oscillation regression")
	}
	// The moved-centroid doc updated.
	if _, ok := rec.updates["doc-drifty"]; !ok {
		t.Fatalf("drifty doc was not updated")
	}
	// The update carries the new summary as name + description (so it re-embeds).
	sf := rec.updates["doc-drifty"]
	if sf["description"] == "" || sf["name"] == "" {
		t.Fatalf("drift update missing name/description set fields: %v", sf)
	}
	// The update RE-ANCHORS the stored topic_centroid to the new LIVE centroid.
	gotCentroid := rec.metadata["doc-drifty"][metaTopicCentroid]
	if want := encodeCentroid(driftyLiveCentroid); gotCentroid != want {
		t.Fatalf("drift update topic_centroid = %q, want encodeCentroid(new live centroid) %q", gotCentroid, want)
	}
}

// TestTopicDrift_NoStoredCentroid asserts a topic doc with no stored topic_centroid
// is skipped (no anchor to drift-check against) — never checked, never refreshed.
func TestTopicDrift_NoStoredCentroid(t *testing.T) {
	doc := &knowledgev1.Node{Id: "d", Type: string(kgtypes.NodeDocument), Description: "text"}
	kgtypes.SetValue(doc, metaMedoidID, "m") // medoid anchor but NO topic_centroid
	topics := map[string]Topic{"m": {PrimaryClusterID: "c", Centroid: bitVec(0), MedoidID: "m"}}
	rec := &updateRecordingCaller{}

	rep, err := driftTopicDocs(context.Background(), rec, []*knowledgev1.Node{doc}, topics, &stubTopicSummarizer{})
	if err != nil {
		t.Fatalf("drift error: %v", err)
	}
	if rep.Checked != 0 || rep.Resummaried != 0 {
		t.Fatalf("doc with no stored centroid must be skipped: got %+v", rep)
	}
	if len(rec.updates) != 0 {
		t.Fatalf("no-anchor doc must write nothing")
	}
}

// TestTopicDrift_NoDeps asserts a nil summarizer (degraded client) does no drift work
// and returns a zero report.
func TestTopicDrift_NoDeps(t *testing.T) {
	doc := driftDoc("d", "m", "text", bitVec(0))
	topics := map[string]Topic{"m": {PrimaryClusterID: "c", Centroid: bitVec(0), MedoidID: "m"}}
	rec := &updateRecordingCaller{}

	rep, err := driftTopicDocs(context.Background(), rec, []*knowledgev1.Node{doc}, topics, nil)
	if err != nil || rep.Resummaried != 0 || rep.Checked != 0 {
		t.Fatalf("nil summarizer: want zero report, got %+v err=%v", rep, err)
	}
	if len(rec.updates) != 0 {
		t.Fatalf("degraded drift must write nothing")
	}
}
