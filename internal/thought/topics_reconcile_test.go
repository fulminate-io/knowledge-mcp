// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"slices"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// recordingMutateCaller records every UPDATE_ITEMS row and DELETE id-set the
// reconciler issues so a test can assert re-key targets and tombstones. It serves
// empty responses (the reconciler does not read mutate results).
type recordingMutateCaller struct {
	updateItems []*knowledgev1.UpdateItem // accumulated across all bulk_update_metadata calls
	deletedIDs  []string                  // accumulated across all delete calls
	updateCalls int
	deleteCalls int
	otherCalls  int
}

func (c *recordingMutateCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	m := req.GetMutation()
	if m == nil {
		c.otherCalls++
		return &knowledgev1.ExecuteResponse{}, nil
	}
	switch m.GetKind() {
	case knowledgev1.MutationPlan_MUTATION_KIND_UPDATE_ITEMS:
		c.updateCalls++
		c.updateItems = append(c.updateItems, m.GetUpdateItems()...)
	case knowledgev1.MutationPlan_MUTATION_KIND_DELETE:
		c.deleteCalls++
		c.deletedIDs = append(c.deletedIDs, m.GetSelection().GetIds()...)
	default:
		c.otherCalls++
	}
	return &knowledgev1.ExecuteResponse{}, nil
}

// updateFor returns the recorded UpdateItem for the given doc id, or nil.
func (c *recordingMutateCaller) updateFor(id string) *knowledgev1.UpdateItem {
	for _, it := range c.updateItems {
		if it.GetId() == id {
			return it
		}
	}
	return nil
}

func (c *recordingMutateCaller) tombstoned(id string) bool {
	return slices.Contains(c.deletedIDs, id)
}

// topicDoc builds a topic `document` node with the given id + medoid + cluster_id
// metadata.
func topicDoc(id, medoid, clusterID string) *knowledgev1.Node {
	n := &knowledgev1.Node{Id: id, Type: string(kgtypes.NodeDocument)}
	kgtypes.SetValue(n, metaMedoidID, medoid)
	kgtypes.SetValue(n, metaClusterID, clusterID)
	return n
}

// TestReconcileTopicDocs_Rekey: a continuing community's min-member cluster_id
// shifts while its medoid stays — the SAME doc node is re-keyed to the new live
// cluster_id via bulk_update_metadata, with no tombstone and no create.
func TestReconcileTopicDocs_Rekey(t *testing.T) {
	// Doc anchored to medoid "m1" stored under old label "old-cid". The live
	// partition now puts m1 under "new-cid" (min-member shifted).
	doc := topicDoc("doc1", "m1", "old-cid")
	communityOf := map[string]string{"m1": "new-cid"}

	rec := &recordingMutateCaller{}
	rep, err := reconcileTopicDocs(context.Background(), rec, []*knowledgev1.Node{doc}, communityOf, nil)
	if err != nil {
		t.Fatalf("reconcile error: %v", err)
	}

	if rep.Rekeyed != 1 {
		t.Fatalf("Rekeyed = %d, want 1", rep.Rekeyed)
	}
	if rep.Tombstoned != 0 {
		t.Fatalf("Tombstoned = %d, want 0 (re-key must not tombstone)", rep.Tombstoned)
	}
	upd := rec.updateFor("doc1")
	if upd == nil {
		t.Fatalf("doc1 not re-keyed (no UpdateItem) — the SAME node must be updated")
	}
	if got := upd.GetMetadata()[metaClusterID]; got != "new-cid" {
		t.Fatalf("re-keyed cluster_id = %q, want new-cid", got)
	}
	if rec.deleteCalls != 0 {
		t.Fatalf("delete calls = %d, want 0 on a pure re-key", rec.deleteCalls)
	}
}

// TestReconcileTopicDocs_Orphan: a TOPIC doc (carries medoid_id) whose medoid maps
// to no live community is tombstoned. A doc with an EMPTY medoid_id is NOT a topic
// doc — it is invisible to reconciliation and must NOT be tombstoned (the data-loss
// guard: selecting orphans by absence-of-anchor once wiped every regular document).
func TestReconcileTopicDocs_Orphan(t *testing.T) {
	orphan := topicDoc("doc-orphan", "dead-medoid", "old-cid")
	noAnchor := topicDoc("doc-noanchor", "", "old-cid") // empty medoid → NOT a topic doc → invisible
	// communityOf has no entry for dead-medoid → orphan; noAnchor is a non-topic doc.
	communityOf := map[string]string{"someone-else": "cid-x"}

	rec := &recordingMutateCaller{}
	rep, err := reconcileTopicDocs(context.Background(), rec, []*knowledgev1.Node{orphan, noAnchor}, communityOf, nil)
	if err != nil {
		t.Fatalf("reconcile error: %v", err)
	}

	if rep.Tombstoned != 1 {
		t.Fatalf("Tombstoned = %d, want 1 (only the topic-doc orphan; the empty-medoid doc is invisible)", rep.Tombstoned)
	}
	if !rec.tombstoned("doc-orphan") {
		t.Fatalf("orphan topic doc not tombstoned")
	}
	if rec.tombstoned("doc-noanchor") {
		t.Fatalf("empty-medoid (non-topic) doc must NOT be tombstoned — it is invisible to reconciliation")
	}
	if rep.Rekeyed != 0 || rep.Merged != 0 {
		t.Fatalf("orphan path must not re-key/merge (Rekeyed=%d Merged=%d)", rep.Rekeyed, rep.Merged)
	}
}

// regularDoc builds a NON-topic `document` node — a hand-written doc (a retro
// guide, etc.) carrying NEITHER the medoid_id anchor NOR a topic_centroid. The
// topic layer must treat it as invisible.
func regularDoc(id, name string) *knowledgev1.Node {
	return &knowledgev1.Node{Id: id, Type: string(kgtypes.NodeDocument), SymbolName: name}
}

// TestReconcileTopicDocs_RegularDocsUntouched is the data-loss guard test. A fixture
// mixing topic docs (with medoid_id) AND regular documents (without) is run through
// EVERY reconcile branch — re-key, merge-survivor, orphan-tombstone. The regular
// documents must be NEVER touched: not deleted, not re-keyed, not modified. Against
// the pre-fix code (which tombstoned any doc with an empty medoid_id) every regular
// doc here would be tombstoned, so this test fails before the fix and passes after.
func TestReconcileTopicDocs_RegularDocsUntouched(t *testing.T) {
	// Topic docs exercising all three reconcile branches:
	rekey := topicDoc("topic-rekey", "m-rekey", "old-cid")   // min-member shift → re-key
	survivor := topicDoc("topic-survivor", "m-surv", "cidA") // cascade survivor
	loser := topicDoc("topic-loser", "m-lose", "cidB")       // cascade loser → tombstone
	orphan := topicDoc("topic-orphan", "m-dead", "cidZ")     // no live community → tombstone

	// Regular documents that MUST be invisible — one named like a real retro guide.
	reg1 := regularDoc("regular-retro", "Session retro guide")
	reg2 := regularDoc("regular-readme", "Topic layer design notes")
	// A regular doc that also carries a cluster_id (a non-topic doc can carry
	// arbitrary metadata) but NO medoid_id / topic_centroid → still invisible.
	reg3 := regularDoc("regular-with-cid", "Stray doc")
	kgtypes.SetValue(reg3, metaClusterID, "cidA")

	communityOf := map[string]string{
		"m-rekey": "new-cid", // shifted label
		"m-surv":  "cidA",
		"m-lose":  "cidA",
		// m-dead absent → orphan
	}
	unions := map[string]topicUnion{
		"m-surv": {
			PrimaryClusterID:  "cidA",
			MemberClusters:    []string{"cidA", "cidB"},
			MergedAwayMedoids: []string{"m-lose"},
		},
	}

	docs := []*knowledgev1.Node{rekey, survivor, loser, orphan, reg1, reg2, reg3}

	rec := &recordingMutateCaller{}
	rep, err := reconcileTopicDocs(context.Background(), rec, docs, communityOf, unions)
	if err != nil {
		t.Fatalf("reconcile error: %v", err)
	}

	// Every regular doc is untouched: not tombstoned, not re-keyed.
	for _, id := range []string{"regular-retro", "regular-readme", "regular-with-cid"} {
		if rec.tombstoned(id) {
			t.Fatalf("regular doc %q was tombstoned — regular documents must be invisible to reconciliation", id)
		}
		if rec.updateFor(id) != nil {
			t.Fatalf("regular doc %q was re-keyed/modified — regular documents must be invisible to reconciliation", id)
		}
	}

	// The topic docs are reconciled exactly as before — the guard does not change
	// topic-doc handling.
	if rep.Rekeyed != 1 {
		t.Fatalf("Rekeyed = %d, want 1 (topic-rekey)", rep.Rekeyed)
	}
	if rep.Merged != 1 {
		t.Fatalf("Merged = %d, want 1 (topic-survivor)", rep.Merged)
	}
	if rep.Tombstoned != 2 {
		t.Fatalf("Tombstoned = %d, want 2 (topic-loser + topic-orphan only)", rep.Tombstoned)
	}
	if !rec.tombstoned("topic-loser") || !rec.tombstoned("topic-orphan") {
		t.Fatalf("expected topic-loser AND topic-orphan tombstoned; got %v", rec.deletedIDs)
	}
	if rec.tombstoned("topic-survivor") || rec.tombstoned("topic-rekey") {
		t.Fatalf("survivor/re-key topic docs must NOT be tombstoned")
	}

	// The report's tombstoned list carries id+name PAIRS for the loud audit, not a
	// bare count — and lists ONLY the topic docs.
	if len(rep.tombstoned) != 2 {
		t.Fatalf("rep.tombstoned id+name list len = %d, want 2", len(rep.tombstoned))
	}
	for _, td := range rep.tombstoned {
		if td.ID != "topic-loser" && td.ID != "topic-orphan" {
			t.Fatalf("loud report names a non-topic doc %q as tombstoned", td.ID)
		}
	}
}

// TestReconcileTopicDocs_CascadeSurvivor: two topics merge via the cascade into
// one survivor. After reconcile exactly ONE doc survives — re-keyed to the live
// label with member_clusters = the union of both source labels — and the merged-
// away doc is tombstoned.
func TestReconcileTopicDocs_CascadeSurvivor(t *testing.T) {
	// Two docs: survivor anchored to medoid "ms" (cluster "cidA"), loser anchored
	// to medoid "ml" (cluster "cidB"). The cascade chose ms as the survivor and
	// absorbed ml; the merged topic's live primary label is "cidA" and it spans
	// both source labels.
	survivor := topicDoc("doc-survivor", "ms", "cidA")
	loser := topicDoc("doc-loser", "ml", "cidB")
	communityOf := map[string]string{"ms": "cidA", "ml": "cidA"}
	unions := map[string]topicUnion{
		"ms": {
			PrimaryClusterID:  "cidA",
			MemberClusters:    []string{"cidA", "cidB"},
			MergedAwayMedoids: []string{"ml"},
		},
	}

	rec := &recordingMutateCaller{}
	rep, err := reconcileTopicDocs(context.Background(), rec, []*knowledgev1.Node{survivor, loser}, communityOf, unions)
	if err != nil {
		t.Fatalf("reconcile error: %v", err)
	}

	if rep.Merged != 1 {
		t.Fatalf("Merged = %d, want 1 survivor", rep.Merged)
	}
	if rep.Tombstoned != 1 {
		t.Fatalf("Tombstoned = %d, want 1 (the merged-away loser)", rep.Tombstoned)
	}
	if !rec.tombstoned("doc-loser") {
		t.Fatalf("the merged-away loser doc was not tombstoned")
	}
	if rec.tombstoned("doc-survivor") {
		t.Fatalf("the survivor doc must NOT be tombstoned")
	}
	upd := rec.updateFor("doc-survivor")
	if upd == nil {
		t.Fatalf("survivor doc was not re-keyed/rewritten")
	}
	if got := upd.GetMetadata()[metaClusterID]; got != "cidA" {
		t.Fatalf("survivor re-keyed cluster_id = %q, want cidA", got)
	}
	gotMembers := decodeMemberClusters(upd.GetMetadata()[metaMemberClusters])
	if len(gotMembers) != 2 || gotMembers[0] != "cidA" || gotMembers[1] != "cidB" {
		t.Fatalf("survivor member_clusters = %v, want [cidA cidB] (the union)", gotMembers)
	}
}
