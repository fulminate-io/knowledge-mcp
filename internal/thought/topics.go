// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// Topic-document metadata keys. A topic is a durable `document` node whose
// Description holds the LLM topic summary (AutoEmbedded). Identity is anchored to
// the medoid thought (metaMedoidID) so the doc survives min-member cluster_id
// label churn underneath it.
const (
	metaMedoidID       = "medoid_id"       // durable identity anchor: the topic's medoid thought ID
	metaClusterID      = "cluster_id"      // current PRIMARY (min-member) live cluster label
	metaMemberClusters = "member_clusters" // delimited list of the topic's one-or-more cluster labels
	metaTopicCentroid  = "topic_centroid"  // hex-encoded bit-majority centroid at last summary
)

// memberClustersSep delimits the member_clusters label list. Cluster labels are
// min-member node IDs (hex / UUID), which never contain a comma, so a comma is a
// safe separator.
const memberClustersSep = ","

// encodeMemberClusters / decodeMemberClusters round-trip the member-cluster label
// list through the single delimited metadata string.
func encodeMemberClusters(labels []string) string {
	return strings.Join(labels, memberClustersSep)
}

func decodeMemberClusters(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, memberClustersSep)
}

// encodeCentroid hex-encodes a binary centroid for the topic_centroid metadata
// string (written as provenance at summary time).
func encodeCentroid(v []byte) string { return hex.EncodeToString(v) }

// decodeCentroid is the inverse of encodeCentroid: it hex-decodes a stored
// topic_centroid metadata string back to the binary centroid. On empty input or a
// decode error it returns nil — a nil/short operand makes BitSimilarity return 0,
// which yields drift 1.0 and one self-healing refresh that rewrites a valid anchor.
func decodeCentroid(s string) []byte {
	if s == "" {
		return nil
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil
	}
	return b
}

// isTopicDoc reports whether a `document` node was created by the topic layer —
// the SOLE gate every topic-doc browse and the reconciler use to decide a doc is
// theirs to touch. Identity is by the PRESENCE of a topic marker, NEVER the
// absence of one: a doc carrying a non-empty medoid_id (the durable anchor) OR a
// topic_centroid (written at summary time) is a topic doc. A regular `document`
// node (a retro guide, a hand-written doc) carries NEITHER, so it is INVISIBLE to
// all topic machinery — never reconciled, re-keyed, re-summarized, or tombstoned.
// This is the data-loss guard: selecting candidates by absence-of-marker once
// tombstoned every non-topic document in the graph.
func isTopicDoc(doc *knowledgev1.Node) bool {
	return kgtypes.Value(doc, metaMedoidID) != "" || kgtypes.Value(doc, metaTopicCentroid) != ""
}

// filterTopicDocs returns only the topic-layer-created docs from a raw `document`
// browse (those passing isTopicDoc). Every consumer that reads the document-type
// browse — reconcile, create-idempotency, drift — runs against this filtered set
// so a regular document never reaches topic machinery.
func filterTopicDocs(docs []*knowledgev1.Node) []*knowledgev1.Node {
	out := docs[:0:0] // fresh backing array; never alias the caller's slice
	for _, d := range docs {
		if isTopicDoc(d) {
			out = append(out, d)
		}
	}
	return out
}

// medoidToDocID maps each topic doc's medoid anchor (medoid_id metadata) to the
// doc's own node Id, over a topic-filtered doc set. It is the medoid→docID twin of
// medoidSetFromDocs (which maps medoid→presence-bool to gate create idempotency);
// the value type differs (docID, not a bool) because this map resolves a medoid to
// the topic doc whose pipeline-embedded vector backs the topic's summary vector.
// Docs with an empty medoid_id are skipped (a topic doc always carries one).
func medoidToDocID(docs []*knowledgev1.Node) map[string]string {
	out := make(map[string]string, len(docs))
	for _, d := range docs {
		if m := kgtypes.Value(d, metaMedoidID); m != "" {
			out[m] = d.GetId()
		}
	}
	return out
}

// populateSummaryVectors assigns each topic's SummaryVector from its topic doc's
// already-drained pipeline embedding, via an in-memory medoid→docID→vectorIndex
// join. It is PURE: no Caller, no context, no round trips — it reads only the maps
// the lever already drained (vectorIndex) and read (docs), so it adds zero wire
// calls. A topic gets its vector only when the looked-up entry is exactly
// vectorBytes (32) long, mirroring groupEmbedding's length guard; a missing
// medoid, missing doc, missing vector, or wrong-length vector leaves SummaryVector
// nil, so the topic falls back to its centroid in groupEmbedding.
//
// FRESHNESS LAG (one-pass): the vector available here is the embedding of the
// topic doc's PREVIOUS summary text — the doc pipeline drains and embeds summaries
// asynchronously, and that drain runs BEFORE any re-summary this pass writes. So a
// continuing topic carries its prior-summary vector (a near-current semantic
// signal), and a BRAND-NEW topic doc (created this pass, not yet embedded) carries
// no vector at all → centroid fallback. The vector is never the just-written
// summary's embedding.
func populateSummaryVectors(topics []Topic, docs []*knowledgev1.Node, vectorIndex map[string][]byte) {
	docIDByMedoid := medoidToDocID(docs)
	for i := range topics {
		if topics[i].MedoidID == "" {
			continue
		}
		docID, ok := docIDByMedoid[topics[i].MedoidID]
		if !ok {
			continue
		}
		if v := vectorIndex[docID]; len(v) == vectorBytes {
			topics[i].SummaryVector = v
		}
	}
}

// Topic is the durable, mergeable unit spanning one or more Leiden clusters — the
// lever's working representation produced by ComputeClusterCentroids + the merge
// cascade. PrimaryClusterID is the current min-member live label; MemberClusters
// lists every source cluster label the topic spans (length 1 for an un-merged
// topic). Centroid is the bit-majority over the FULL member set; MedoidID is the
// member thought bit-closest to that centroid (the durable identity anchor).
// CreatedAt (the medoid thought's creation time) breaks survivor ties → oldest.
type Topic struct {
	PrimaryClusterID string
	MemberClusters   []string
	MemberThoughtIDs []string // every member thought across the topic's clusters
	Centroid         []byte
	MedoidID         string
	CreatedAt        int64
	Size             int    // total member-thought count across the topic's clusters
	SummaryContent   string // prompt content (member symbol-names/summaries) for the summarizer

	// SummaryVector is the topic doc's pipeline-embedded summary vector, populated
	// at lever time by populateSummaryVectors from the already-drained vectorIndex
	// (keyed medoid→doc). It is the PREFERRED group-embedding operand for similarity;
	// when empty the group embedding falls back to Centroid. ONE-PASS FRESHNESS LAG:
	// the vector is the PREVIOUS summary's embedding (the doc pipeline drains/embeds
	// asynchronously, before any re-summary this pass), or absent for a brand-new
	// topic doc not yet embedded → centroid fallback. It is never the just-written
	// summary's embedding.
	SummaryVector []byte
}

// topicUnion is the reconciliation view of one surviving topic the merge cascade
// produced, keyed (in the unions map) by the survivor's medoid ID — the durable
// anchor. PrimaryClusterID + MemberClusters are the survivor's re-keyed label and
// the union label set; MergedAwayMedoids are the medoids of the topics absorbed
// into this survivor (their docs are tombstoned).
type topicUnion struct {
	PrimaryClusterID  string
	MemberClusters    []string
	MergedAwayMedoids []string
}

// reconcileReport tallies the reconciliation actions for the lever's loud report.
// The tombstoned list carries id+name PAIRS (not a bare count): the report — and
// the daemon log — must name exactly which docs were removed, so a wrongful
// deletion is visible at a glance rather than hidden behind a count. The delete is
// SOFT (see writeTombstones) — tombstoned rows are recoverable — but the loud
// listing stays: a wrongful tombstone still hides the doc from every read.
type reconcileReport struct {
	Rekeyed    int             // continuing topics re-keyed to a shifted live label
	Merged     int             // cascade survivors re-keyed + member_clusters rewritten
	Tombstoned int             // merged-away losers + true orphans removed
	rekeyedIDs []string        // doc IDs re-keyed (re-key or merge), for report detail
	tombstoned []TombstonedDoc // docs tombstoned (id+name), for the loud report
}

// reconcileTopicDocs reconciles the persisted topic `document` nodes against the
// current Leiden partition AND the merge cascade's topic unions — the FIRST stage
// of the lever's topic lifecycle (before create/drift). Identity is anchored to
// the medoid thought (metaMedoidID), never the churning min-member cluster_id, so
// a topic doc survives label churn.
//
// CANDIDATE SELECTION IS BY MARKER PRESENCE, NEVER ABSENCE. Any doc in existingDocs
// that is NOT a topic doc (isTopicDoc false — no medoid_id and no topic_centroid)
// is SKIPPED whole: never reconciled, re-keyed, or tombstoned. A regular `document`
// node is invisible here. (existingDocs is expected to be pre-filtered to topic
// docs by the caller; the per-doc guard is defense-in-depth so a wrongful delete
// is impossible even if an unfiltered set is passed.) This is the data-loss guard:
// the original code tombstoned every doc with an empty medoid_id, which wiped all
// regular documents in the graph.
//
// For each TOPIC doc (passes isTopicDoc, carries metaMedoidID):
//   - a medoid mapping to no live community → TRUE ORPHAN → tombstone.
//   - medoid is a merged-away medoid in some union → cascade LOSER → tombstone.
//   - medoid is a union survivor → re-key cluster_id to the union's primary label
//     and rewrite member_clusters to the union label set (Merged++).
//   - otherwise (continuing topic) → re-key cluster_id only when the live primary
//     label shifted from the stored one (Rekeyed++).
//
// All re-keys / member-cluster rewrites ride ONE reflect-inert bulk_update_metadata
// (so the lever's own writeback never self-triggers the hourly dirty pass); all
// tombstones ride ONE mutate(delete, ids) batch. NO TopicSummarizer call happens
// here — reconciliation never summarizes.
func reconcileTopicDocs(
	ctx context.Context,
	gc Caller,
	existingDocs []*knowledgev1.Node,
	communityOf map[string]string,
	unions map[string]topicUnion,
) (reconcileReport, error) {
	var rep reconcileReport

	// Build the merged-away → survivor reverse index so a loser doc is recognized
	// by its own medoid.
	mergedAway := make(map[string]bool)
	for _, u := range unions {
		for _, m := range u.MergedAwayMedoids {
			mergedAway[m] = true
		}
	}

	// desired carries the bulk_update_metadata rows (re-keys + member-cluster
	// rewrites); tombstones collects the delete-target docs (id+name for the report).
	var desired []map[string]any
	var tombstones []TombstonedDoc

	for _, doc := range existingDocs {
		// Marker-presence gate: a doc the topic layer did NOT create is invisible —
		// never deletable, re-keyable, or counted. The only deletable docs below are
		// ones carrying the topic marker (medoid_id / topic_centroid).
		if !isTopicDoc(doc) {
			continue
		}

		medoid := kgtypes.Value(doc, metaMedoidID)
		liveCID, alive := communityOf[medoid]

		// True orphan: a topic doc whose anchor maps to no live community.
		if !alive {
			tombstones = append(tombstones, TombstonedDoc{ID: doc.Id, Name: doc.GetSymbolName()})
			continue
		}

		// Cascade loser: this doc's medoid was absorbed into another survivor.
		if mergedAway[medoid] {
			tombstones = append(tombstones, TombstonedDoc{ID: doc.Id, Name: doc.GetSymbolName()})
			continue
		}

		// Cascade survivor: re-key + rewrite member_clusters to the union set.
		if u, ok := unions[medoid]; ok {
			desired = append(desired, map[string]any{
				"id": doc.Id,
				"metadata": map[string]string{
					metaClusterID:      u.PrimaryClusterID,
					metaMemberClusters: encodeMemberClusters(u.MemberClusters),
				},
			})
			rep.Merged++
			rep.rekeyedIDs = append(rep.rekeyedIDs, doc.Id)
			continue
		}

		// Continuing topic: re-key only when the live primary label shifted.
		if storedCID := kgtypes.Value(doc, metaClusterID); storedCID != liveCID {
			desired = append(desired, map[string]any{
				"id": doc.Id,
				"metadata": map[string]string{
					metaClusterID: liveCID,
				},
			})
			rep.Rekeyed++
			rep.rekeyedIDs = append(rep.rekeyedIDs, doc.Id)
		}
	}

	// ONE reflect-inert bulk_update_metadata for all re-keys + member-cluster rewrites.
	if err := writeReconcileUpdates(ctx, gc, desired); err != nil {
		return rep, err
	}
	// ONE delete batch for all tombstones. This is a SOFT delete (the wire
	// `delete` op tombstones by default; writeTombstones never opts into
	// hard:true), so the rows survive recoverable under tombstoned_at. The report
	// below still lists id+name for every removed doc — never a bare count — and
	// only topic-marked docs ever reach here.
	if len(tombstones) > 0 {
		sort.Slice(tombstones, func(i, j int) bool { // deterministic batch for reproducible reports
			return tombstones[i].ID < tombstones[j].ID
		})
		ids := make([]string, len(tombstones))
		for i, t := range tombstones {
			ids[i] = t.ID
		}
		if err := writeTombstones(ctx, gc, ids); err != nil {
			return rep, err
		}
		rep.Tombstoned = len(tombstones)
		rep.tombstoned = tombstones
	}

	return rep, nil
}

// writeReconcileUpdates issues the single reflect-inert bulk_update_metadata for
// all topic-doc re-keys + member-cluster rewrites. A no-op for an empty set.
func writeReconcileUpdates(ctx context.Context, gc Caller, desired []map[string]any) error {
	if len(desired) == 0 {
		return nil
	}
	args, err := json.Marshal(map[string]any{
		"operation": "bulk_update_metadata",
		"updates":   desired,
	})
	if err != nil {
		return fmt.Errorf("thought: reconcileTopicDocs: marshal updates: %w", err)
	}
	if err := executeReflectInertMutate(ctx, gc, args); err != nil {
		return fmt.Errorf("thought: reconcileTopicDocs: re-key write: %w", err)
	}
	return nil
}

// writeTombstones issues the single batched delete for all tombstoned doc IDs.
// This is a SOFT delete: the wire `delete` op tombstones by default (the engine
// DELETE arm passes store.WithHard only on an explicit hard:true, which this
// caller never sets), so the rows survive under tombstoned_at, hidden from reads
// but recoverable. Callers still list the removed ids+names in the loud report —
// a wrongful tombstone is invisible-data until noticed.
func writeTombstones(ctx context.Context, gc Caller, ids []string) error {
	args, err := json.Marshal(map[string]any{
		"operation": "delete",
		"ids":       ids,
	})
	if err != nil {
		return fmt.Errorf("thought: reconcileTopicDocs: marshal tombstones: %w", err)
	}
	if _, err := executeViaEngine(ctx, gc, "delete", args); err != nil {
		return fmt.Errorf("thought: reconcileTopicDocs: tombstone write: %w", err)
	}
	return nil
}
