// SPDX-License-Identifier: Apache-2.0

package tools

// mutate_arm_registry_update.go holds the update / delete / answer arms of the
// param classification table. Split from the create-family sibling only to keep
// each file inside the repo's file-length gate; see mutate_arm_registry.go for
// the authoring rules every cell in all three files follows.
// justifyAnswerDerived and justifyAnswerBodyEdit explain the two distinct
// reasons the answer arm refuses a body field: the first pair is written by the
// operation itself (routing a caller value would fight that write), the second
// trio is an ordinary body edit the arm has no business doing.
const (
	justifyAnswerDerived  = "the answer operation sets status and summary itself"
	justifyAnswerBodyEdit = "it is a body edit; issue mutate(update) on the question instead"

	// justifyBulkMetadataOnly covers the top-level body scalars on
	// bulk_update_metadata. Reading only updates[]{id,metadata} is the
	// operation's advertised contract, so ignoring a stray top-level body field
	// is a stated decision — rejecting the schema-advertised key would be a
	// false rejection of a correctly-shaped call.
	justifyBulkMetadataOnly = "bulk_update_metadata reads only updates[]{id,metadata}; " +
		"per-node body edits belong on mutate(update) or mutate(update_batch)"

	// justifyTypedNoCascade covers expand_to_descendants on the per-type update
	// router. Only the rollup claim gate reads the flag (via cascadeToDescendants),
	// and the two arms are disjoint by node type — the router claims criterion,
	// rule and finding, none of which is a rollup container — so the flag can
	// neither select this arm nor change what it writes.
	justifyTypedNoCascade = "only the container-rollup claim gate reads this flag, " +
		"and this arm claims only non-container types"
)

var updateArmSpecs = map[armID]armSpec{

	// The backend-backed single-id update runs the tracker write-through then
	// forwards the local half, which carries keywords and source as top-level
	// set_fields the same way every other update arm does. The per-type params
	// stay rejected: a tracker-backed node is a work item and owns none of them.
	//
	// `status` is consumed here, but with a VALUE-CONDITIONAL rejection this
	// table cannot express and does not replace: an explicitly-supplied EMPTY
	// status (clear to blank) is rejected by the sibling guard in
	// handleInterceptMutateUpdate, because a tracker's status vocabulary has no
	// blank state. Any non-empty status routes normally.
	armUpdateBackend: {
		operation: "update",
		handler:   "Linear write-through in handleInterceptMutateUpdate",
		consumed: paramSet(
			"operation", "id", "ids", "graph", "name", "description", "summary", "content",
			"status", "metadata", "keywords", "source",
			"verified_quote", "cited_range",
		),
		rejected: paramSet(
			"supports",
			"type", "expand_to_descendants", "evidence", "question_id", "concludes",
			"scope", "enforcement", "step_id", "command", "criterion_type", "from", "to",
			"relationship", "conclusion", "findings", "language", "binary_vector",
			"confidence", "method", "edge_evidence", "last_validated", "link_graph", "branches_from",
			"links", "session", "ticket_id", "polarity", "weight", "reasoning", "charge_evidence",
			"thought_parent", "references", "items", "nodes", "edges", "updates",
		),
		deliberatelyIgnored: clientRenderedFormat(),
	},

	// The container-status rollup cascades status down the contains tree and
	// applies the accompanying body fields to the NAMED node only, via a separate
	// by-id write. The per-type params stay rejected: the node is a
	// project/ticket/plan/phase/step and none of those owns a criterion/rule/
	// finding key — the same rule the typed-update arm's per-node-type refinement
	// encodes.
	//
	// The negation proof params are REJECTED here, and that is deliberate rather
	// than an omission — do not "fix" it to match the sibling update arms. The
	// rollup is claimed only when a.Status == kgtypes.StatusCompleted
	// (intercept_mutate_dispatch.go:131), and the only update-shaped negation is
	// status:"invalidated" (negation_gate.go recognizeNegationOp), so no negation
	// call can ever select this arm. A proof param here routes nothing.
	armUpdateRollup: {
		operation: "update",
		handler:   "handleClientUpdateStatusRollup",
		consumed: paramSet(
			"operation", "id", "ids", "status", "expand_to_descendants", "graph",
			"name", "description", "summary", "content", "metadata", "keywords", "source",
		),
		rejected: paramSet(
			"supports",
			"type", "evidence", "question_id",
			"concludes", "scope", "enforcement", "step_id", "command", "criterion_type", "from", "to",
			"relationship", "conclusion", "findings", "language",
			"binary_vector", "confidence", "method", "edge_evidence", "last_validated", "link_graph",
			"branches_from", "links", "session", "ticket_id", "polarity", "weight", "reasoning",
			"charge_evidence", "thought_parent", "references", "items", "nodes", "edges", "updates",
			"verified_quote", "cited_range",
		),
		deliberatelyIgnored: clientRenderedFormat(),
	},

	// The per-type router forwards the universal scalars and lands the five
	// per-type params in metadata; a per-type param unroutable for the looked-up
	// NODE TYPE is refined by the sibling per-type rejection, which this table
	// cannot express and does not replace. `name` stays consumed here because
	// rule and finding updates route it — the criterion-only refinement (a
	// criterion's name is derived from its description, so a supplied one is
	// rejected) lives in that same sibling function.
	armUpdateTyped: {
		operation: "update",
		handler:   "handleClientMutateUpdateTyped",
		consumed: paramSet(
			"operation", "id", "ids", "graph", "language", "name", "description", "summary",
			"content", "status", "keywords", "source", "metadata", "command",
			"criterion_type", "scope", "enforcement", "evidence",
			"verified_quote", "cited_range",
		),
		rejected: paramSet(
			"supports",
			"type", "question_id", "concludes", "step_id", "from", "to", "relationship",
			"conclusion", "findings", "binary_vector", "confidence", "method", "edge_evidence",
			"last_validated", "link_graph", "branches_from", "links", "session", "ticket_id",
			"polarity", "weight", "reasoning", "charge_evidence", "thought_parent", "references",
			"items", "nodes", "edges", "updates",
		),
		deliberatelyIgnored: map[string]string{
			"format":                justifyClientRendered,
			"expand_to_descendants": justifyTypedNoCascade,
		},
	},

	// A single-id update the typed router declines lands on the engine by-id
	// UPDATE arm, which routes the universal scalars as set_fields and metadata
	// as set_metadata. expand_to_descendants is consumed: the explicit-false
	// cascade opt-out is exactly the shape that reaches this arm.
	armUpdateFallthrough: {
		operation: "update",
		handler:   "engine compileMutateByIDUpdate",
		consumed: paramSet(
			"operation", "id", "ids", "graph", "language", "name", "description", "summary",
			"content", "status", "keywords", "source", "metadata", "format", "expand_to_descendants",
			"verified_quote", "cited_range",
		),
		rejected: paramSet(
			"supports",
			"type", "evidence", "question_id", "concludes", "scope", "enforcement", "step_id",
			"command", "criterion_type", "from", "to", "relationship", "conclusion", "findings",
			"binary_vector", "confidence", "method", "edge_evidence", "last_validated", "link_graph",
			"branches_from", "links", "session", "ticket_id", "polarity", "weight", "reasoning",
			"charge_evidence", "thought_parent", "references", "items", "nodes", "edges", "updates",
		),
		deliberatelyIgnored: map[string]string{},
	},

	// A multi-id update batch. source and the five per-type params ARE rejected:
	// this arm never applies them, so rejected is the honest class, and it is the
	// class whose contract downstream assertions check — that the call errors
	// naming the param with zero writes, which holds regardless of WHICH gate
	// produced the error. Declaring them merely ignored would assert the
	// opposite (call succeeds, value appears nowhere) and be false, since the
	// batch-shape contract gate errors on all six.
	//
	// That is also why this arm's accounting runs AFTER the contract gate rather
	// than before it: both reject the same six, and the contract gate's message
	// carries the actionable split-into-per-id-updates remedy this generic gate
	// cannot express. Its per-id lookups are reads, so the reject still leaves
	// every node byte-identical.
	armUpdateBatchIDs: {
		operation: "update",
		handler:   "guardBatchUpdateShape then engine compileMutateByIDUpdate",
		consumed: paramSet(
			"operation", "ids", "graph", "language", "name", "description", "summary", "content",
			"status", "keywords", "metadata", "format",
		),
		rejected: paramSet(
			"supports",
			"type", "id", "expand_to_descendants", "source", "evidence", "question_id", "concludes",
			"scope", "enforcement", "step_id", "command", "criterion_type", "from", "to",
			"relationship", "conclusion", "findings", "binary_vector", "confidence", "method",
			"edge_evidence", "last_validated", "link_graph", "branches_from", "links", "session",
			"ticket_id", "polarity", "weight", "reasoning", "charge_evidence", "thought_parent",
			"references", "items", "nodes", "edges", "updates",
			"verified_quote", "cited_range",
		),
		deliberatelyIgnored: map[string]string{},
	},

	// update_batch carries its whole payload in items[]; the top-level body
	// fields belong to the single-update shape. `name` is one of them — each item
	// names its target by id and no item body carries a name — and it is not
	// routing either: a foreign-graph update_batch never reaches this arm (the
	// non-knowledge guard claims it under armNonKnowledgeFallthrough), so the arm
	// only runs on the knowledge family, whose resolver reads no selector name.
	// (The PIPELINE write-back does route a graph-instance name on this shape, but
	// it calls engine.Compile directly from pipeline/rpc.go and never passes
	// through this dispatch, so it is not governed by this cell.)
	armUpdateBatchItems: {
		operation: "update_batch",
		handler:   "engine compileMutateUpdateBatch",
		consumed:  paramSet("operation", "items", "graph", "language", "format"),
		rejected: paramSet(
			"supports",
			"name",
			"type", "id", "ids", "description", "summary", "content", "status",
			"expand_to_descendants", "source", "evidence", "question_id", "concludes", "scope",
			"enforcement", "step_id", "command", "criterion_type", "from", "to", "relationship",
			"conclusion", "findings", "metadata", "keywords", "binary_vector", "confidence", "method",
			"edge_evidence", "last_validated", "link_graph", "branches_from", "links", "session",
			"ticket_id", "polarity", "weight", "reasoning", "charge_evidence", "thought_parent",
			"references", "nodes", "edges", "updates",
			"verified_quote", "cited_range",
		),
		deliberatelyIgnored: map[string]string{},
	},

	// bulk_update_metadata carries its whole payload in updates[]. The top-level
	// body scalars are ignored rather than rejected: reading only updates[] IS
	// this operation's advertised contract, not an oversight — and `name` joins
	// them. It was declared consumed-as-routing while the compile arm copied it
	// into the Execute Target's graph-instance selector, but a foreign-graph bulk
	// update never reaches this arm (the non-knowledge guard claims it under
	// armNonKnowledgeFallthrough), so the arm only runs on the knowledge family,
	// whose resolver reads no selector name. The value could only ever be a node
	// name, and this operation writes no node body.
	armBulkUpdateMetadata: {
		operation: "bulk_update_metadata",
		handler:   "engine compileMutateBulkMetadata",
		consumed:  paramSet("operation", "updates", "graph", "language", "format"),
		rejected: paramSet(
			"supports",
			"type", "id", "ids", "expand_to_descendants", "evidence", "question_id", "concludes",
			"scope", "enforcement", "step_id", "command", "criterion_type", "from", "to",
			"relationship", "conclusion", "findings", "binary_vector", "confidence", "method",
			"edge_evidence", "last_validated", "link_graph", "branches_from", "links", "session",
			"ticket_id", "polarity", "weight", "reasoning", "charge_evidence", "thought_parent",
			"references", "items", "nodes", "edges",
			"verified_quote", "cited_range",
		),
		deliberatelyIgnored: map[string]string{
			"name":        justifyBulkMetadataOnly,
			"description": justifyBulkMetadataOnly,
			"summary":     justifyBulkMetadataOnly,
			"content":     justifyBulkMetadataOnly,
			"status":      justifyBulkMetadataOnly,
			"keywords":    justifyBulkMetadataOnly,
			"source":      justifyBulkMetadataOnly,
			"metadata":    justifyBulkMetadataOnly,
		},
	},

	// Delete archives any tracker-backed ids then forwards a tombstone carrying
	// only operation/ids/graph/language.
	armDelete: {
		operation: "delete",
		handler:   "handleInterceptMutateDelete",
		consumed:  paramSet("operation", "id", "ids", "graph", "language"),
		rejected: paramSet(
			"supports",
			"type", "name", "description", "summary", "content", "status", "expand_to_descendants",
			"source", "evidence", "question_id", "concludes", "scope", "enforcement", "step_id",
			"command", "criterion_type", "from", "to", "relationship", "conclusion", "findings",
			"metadata", "keywords", "binary_vector", "confidence", "method", "edge_evidence",
			"last_validated", "link_graph", "branches_from", "links", "session", "ticket_id",
			"polarity", "weight", "reasoning", "charge_evidence", "thought_parent", "references",
			"items", "nodes", "edges", "updates",
			"verified_quote", "cited_range",
		),
		deliberatelyIgnored: clientRenderedFormat(),
	},

	// Answer resolves the question by id (question_id is the documented alias),
	// stamps the conclusion, and comma-splits findings into answers edges. Caller
	// metadata is merged alongside the derived conclusion key. The body fields
	// stay rejected for two different reasons, so each carries its own
	// explanation: status and summary are written by the operation itself, and
	// name/description/content are plain body edits with a home on mutate(update).
	armAnswer: {
		operation: "answer",
		handler:   "handleClientMutateAnswer",
		consumed: paramSet(
			"operation", "id", "graph", "question_id", "conclusion", "findings", "metadata",
		),
		rejected: paramSet(
			"supports",
			"type", "ids", "name", "description", "summary", "content", "status",
			"expand_to_descendants", "source", "evidence", "concludes", "scope", "enforcement",
			"step_id", "command", "criterion_type", "from", "to", "relationship", "language",
			"keywords", "binary_vector", "confidence", "method", "edge_evidence",
			"last_validated", "link_graph", "branches_from", "links", "session", "ticket_id",
			"polarity", "weight", "reasoning", "charge_evidence", "thought_parent", "references",
			"items", "nodes", "edges", "updates",
			"verified_quote", "cited_range",
		),
		rejectionReasons: map[string]string{
			"status":      justifyAnswerDerived,
			"summary":     justifyAnswerDerived,
			"name":        justifyAnswerBodyEdit,
			"description": justifyAnswerBodyEdit,
			"content":     justifyAnswerBodyEdit,
		},
		deliberatelyIgnored: clientRenderedFormat(),
	},
}
