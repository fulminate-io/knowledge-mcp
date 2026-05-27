// SPDX-License-Identifier: Apache-2.0

package engine

import (
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// compile_mutate_batch.go holds the update_batch → MUTATION_KIND_UPDATE_ITEMS
// lowering. Split out of compile_mutate.go (which is near the 500-line cap) into
// this same-package sibling (precedent: pkg/store's composite_db_write_batch.go).
// The compileMutate switch case + the mutateArgs/batchItem types live in
// compile_mutate.go; only the bulky lowering + mapper live here.

// compileMutateUpdateBatch lowers a mutate(update_batch) into a
// MUTATION_KIND_UPDATE_ITEMS plan: each batchItem becomes a distinct proto
// UpdateItem (the heterogeneous per-item arm), all riding ONE Execute → one txn →
// one commit on the server (the pipeline write-back's per-batch RPC discipline).
// Empty items → (nil,false) so the degenerate empty case falls through to legacy.
// The Target carries graph/repo/account/name/language so a code/cloud-graph
// write-back routes to the right per-graph backing (the engine resolves it).
func compileMutateUpdateBatch(a mutateArgs) (*knowledgev1.ExecuteRequest, bool) {
	if len(a.Items) == 0 {
		return nil, false // empty / id-less update_batch → legacy.
	}
	items := make([]*knowledgev1.UpdateItem, len(a.Items))
	for i, it := range a.Items {
		items[i] = batchItemToUpdateItem(it)
	}
	plan := &knowledgev1.MutationPlan{
		Kind:        knowledgev1.MutationPlan_MUTATION_KIND_UPDATE_ITEMS,
		UpdateItems: items,
	}
	// Build the request inline (rather than via mutationRequest, which targets
	// the knowledge graph with empty repo/account/name) so the batch routes to
	// the right per-graph backing.
	return &knowledgev1.ExecuteRequest{
		Plan:   &knowledgev1.ExecuteRequest_Mutation{Mutation: plan},
		Target: buildTarget(a.Graph, a.Repo, a.Account, a.Name, a.Language, ""),
	}, true
}

// batchItemToUpdateItem maps a compile-local batchItem onto a proto UpdateItem,
// preserving the *string set/unset distinction onto the proto `optional` fields
// (a nil pointer stays nil → "untouched"; a set pointer rides verbatim, including
// a deliberate empty-string clear). binary_vector bytes + metadata map ride as-is.
func batchItemToUpdateItem(it batchItem) *knowledgev1.UpdateItem {
	return &knowledgev1.UpdateItem{
		Id:           it.ID,
		Summary:      it.Summary,
		Keywords:     it.Keywords,
		BinaryVector: it.BinaryVector,
		Metadata:     it.Metadata,
		Status:       it.Status,
	}
}

// bulkUpdateItem is the bulk_update_metadata per-item wire shape: a target id +
// the per-key metadata merge map. It is a metadata-only subset of batchItem (no
// summary/keywords/status/vector) — bulk_update_metadata is exactly "merge this
// metadata onto these N nodes", nothing else.
type bulkUpdateItem struct {
	ID       string            `json:"id"`
	Metadata map[string]string `json:"metadata"`
}

// compileMutateBulkMetadata lowers a mutate(bulk_update_metadata) into the SAME
// MUTATION_KIND_UPDATE_ITEMS arm update_batch rides: each {id, metadata} item
// becomes a metadata-only proto UpdateItem (summary/keywords/status/vector left
// nil/empty → untouched, per the UpdateItem "nil = untouched" contract). All N
// items apply inside ONE Execute → one txn → one commit, and the backend-tag
// reject the legacy handler enforced is preserved by the engine's validateUpdate-
// Items decode. A degenerate shape (no updates, or any item missing an id or
// metadata) falls through to legacy, mirroring compileMutateUpdateBatch's empty
// guard.
func compileMutateBulkMetadata(a mutateArgs) (*knowledgev1.ExecuteRequest, bool) {
	if len(a.Updates) == 0 {
		return nil, false // empty bulk_update_metadata → legacy.
	}
	items := make([]*knowledgev1.UpdateItem, len(a.Updates))
	for i, u := range a.Updates {
		if u.ID == "" || len(u.Metadata) == 0 {
			return nil, false // degenerate item (no id / no metadata) → legacy.
		}
		items[i] = &knowledgev1.UpdateItem{Id: u.ID, Metadata: u.Metadata}
	}
	plan := &knowledgev1.MutationPlan{
		Kind:        knowledgev1.MutationPlan_MUTATION_KIND_UPDATE_ITEMS,
		UpdateItems: items,
	}
	// Inline request (not via mutationRequest) so a cross-graph bulk update routes
	// to the right per-graph backing — though the bulk_update_metadata callers
	// (clusters.go / propagation.go) are knowledge-graph, so empty target →
	// knowledge.
	return &knowledgev1.ExecuteRequest{
		Plan:   &knowledgev1.ExecuteRequest_Mutation{Mutation: plan},
		Target: buildTarget(a.Graph, a.Repo, a.Account, a.Name, a.Language, ""),
	}, true
}
