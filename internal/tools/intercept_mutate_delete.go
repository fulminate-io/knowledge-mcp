// SPDX-License-Identifier: Apache-2.0

package tools

// intercept_mutate_delete.go holds the mutate(delete) arm: it archives every
// tracker-backed id through its adapter, then forwards one tombstone covering
// all ids. Split out of intercept_mutate.go for file length only.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/backends/dispatch"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/workingset"
)

func handleInterceptMutateDelete(
	ctx context.Context,
	deps ClientDeps,
	a mutateArgs,
) (bool, kgtools.ToolResult) {
	if a.ID == "" && len(a.IDs) == 0 {
		// Server will emit its own "delete requires id=..." error.
		return false, kgtools.ToolResult{}
	}
	ids := a.IDs
	if len(ids) == 0 {
		ids = []string{a.ID}
	}

	gc := deps.GraphCaller()
	if err := guardBatchHasNoBackendBacked(ctx, gc, a.IDs); err != nil {
		return true, errorResult(err.Error())
	}

	var archived []string
	for _, id := range ids {
		node, backendName, _, _, _, lookupErr := lookupNodeBackend(ctx, gc, id)
		if lookupErr != nil {
			return true, errorResult("mutate(delete): " + lookupErr.Error())
		}
		if backendName == "" {
			// Non-backend id — skip; the final routed forward will tombstone it.
			continue
		}
		backend := deps.BackendResolver().ByName(backendName)
		if backend == nil {
			msg := fmt.Sprintf(
				"mutate(delete): backend %q recorded on node %s but not currently configured",
				backendName, id,
			)
			if len(archived) > 0 {
				msg = fmt.Sprintf(
					"%s; Linear archive succeeded for %d node(s) (%s) before this failure. %s",
					msg, len(archived), strings.Join(archived, ","), linearArchiveRetryGuidance,
				)
			}
			return true, errorResult(msg)
		}
		if err := dispatch.Archive(ctx, node, backendName, backend, dispatch.DeleteArgs{NodeID: id}); err != nil {
			msg := fmt.Sprintf("mutate(delete): %v", err)
			if len(archived) > 0 {
				msg = fmt.Sprintf(
					"%s; Linear archive succeeded for %d prior node(s) (%s) before this failure. %s",
					msg, len(archived), strings.Join(archived, ","), linearArchiveRetryGuidance,
				)
			}
			return true, errorResult(msg)
		}
		archived = append(archived, id)
	}

	// Forward the tombstone — the knowledge graph tombstones every id regardless
	// of Linear's involvement — routed through the login-aware Execute carrier
	// seam (by-id DELETE, cloud when logged in). The engine DELETE arm selects
	// via Selection.Ids, so the forward
	// carries the normalized PLURAL ids[] (a singular caller `id` was folded
	// into ids above); the caller's graph/language are preserved. format is NOT
	// forwarded — this arm renders its own result, matching the
	// deliberately-ignored cell the accounting table declares for it. Reuses
	// params.Arguments-equivalent intent without the singular-id wire shape the
	// generic delete arm does not reduce.
	forwardedDelete, derr := json.Marshal(struct {
		Operation string   `json:"operation"`
		IDs       []string `json:"ids"`
		Graph     string   `json:"graph,omitempty"`
		Language  string   `json:"language,omitempty"`
	}{Operation: "delete", IDs: ids, Graph: a.Graph, Language: a.Language})
	if derr != nil {
		return true, errorResult("mutate(delete): marshal forward: " + derr.Error())
	}
	if _, err := executeMutate(ctx, gc, forwardedDelete); err != nil {
		if len(archived) > 0 {
			return true, errorResult(fmt.Sprintf(
				"Linear archive succeeded for %d node(s) (%s), but local delete failed: %v. %s",
				len(archived), strings.Join(archived, ","), err, linearArchiveRetryGuidance,
			))
		}
		return true, errorResult(fmt.Sprintf("mutate(delete): local delete failed: %v", err))
	}
	reEmitErr := reEmitDeletedFromSegments(ctx, deps, a.Graph, a.Language, ids)
	return true, textResult(renderDeleteAck(len(archived), len(ids), reEmitErr))
}

// renderDeleteAck reports what landed: the archived + tombstoned counts, plus the
// qualifier when the shipped-corpus re-emit failed.
//
// THE DELETE LANDED AND THE DURABILITY STEP DID NOT, so the result says both. The
// caller does NOT get an error result: the tombstone is committed server-side, and
// reporting the operation as failed would be a second false statement rather than a
// fix for the first. What it must never be is an UNQUALIFIED success — that is the
// report this arm used to produce, and it left a caller believing the removal was
// durable in the shipped corpus when it was not.
func renderDeleteAck(archived, tombstoned int, reEmitErr error) string {
	ack := fmt.Sprintf(
		"mutate(delete): archived %d node(s) in the external tracker + tombstoned %d node(s) in the knowledge graph",
		archived, tombstoned,
	)
	if reEmitErr != nil {
		ack += "\n\n" + segmentReEmitFailureNotice(reEmitErr)
	}
	return ack
}

// segmentReEmitFailureNotice is the SINGLE wording of the qualifier both delete
// paths append when the shipped-corpus re-emit failed. It names what did not
// land and what the caller can expect, and it is one declaration so the two arms
// cannot describe the same condition differently.
//
// It does NOT promise recovery. Which recovery applies depends on the path — the
// tombstone path can re-learn the id from a later server-side scan, the hard
// prune path cannot — and reEmitDeletedFromSegments' own doc states both with
// their preconditions rather than asserting a repair here that may not run.
func segmentReEmitFailureNotice(err error) string {
	return fmt.Sprintf(
		"WARNING: the local shipped segment corpus was NOT updated for this removal: %v. "+
			"The rows are gone from the graph, but those documents stay resident in this "+
			"client's shipped blobs — and keep taking top-k slots in local search — until a "+
			"later pass or a rebuild removes them.", err)
}

// reEmitDeletedFromSegments carries a completed delete into this client's SHIPPED
// segment corpus, so the removal survives in the blobs rather than only as a
// cleared bit in memory. Without it the dead vector keeps competing for top-k
// slots — the user's result set silently gets SHORTER, since the read path drops
// the ranked-but-tombstoned id after it has already taken a slot — and every ship,
// cache file and load of that partition keeps carrying the document.
//
// NON-FATAL, BUT REPORTED. The delete has already been applied when this runs, so
// a re-emit failure must never turn a successful delete into a reported failure —
// it is logged and the call continues. It is NOT swallowed: the error is RETURNED,
// and both callers append a qualifier to their result text naming what did not
// land. Until they did, the failure had no path out of this function at all: it
// returned nothing and the acks were fixed strings, so a caller was told the
// removal was durable in the shipped corpus whether or not it was.
//
// WHAT ACTUALLY RECOVERS A DROPPED RE-EMIT, on the tombstone path, is the
// segment-delta consumer and NOT anything touching the partition. MergeSegmentDelta
// (tombstone_delta_consumer.go) learns the id from a SERVER-side tombstone scan on a
// later reconcile pass, and landDeltaTombstones then stamps it, merges it into the
// persisted tombstone record, and only then re-deletes it from the buckets. That
// record write is what later partition emissions filter on — and this path performs
// NONE of those three steps, unlike manage(prune), which seeds the record before it
// calls here (intercept_manage_prune.go). So until that pass runs, nothing touching
// the partition would drop the id, because locally nothing knows it is dead.
//
// ITS PRECONDITION IS WORKING-SET MEMBERSHIP. The reconcile visits only the graphs
// segmentBearingGraphs yields (bootstrap/client_segment_reconcile.go): a member an
// interaction earned, and — for a code graph — one whose checkout this machine
// holds. A graph outside that set is never visited, so its dropped re-emit is never
// re-learned. On the hard-delete (prune) path the server row is gone outright, so
// there is nothing left to re-learn from and recovery there is a rebuild.
//
// instanceKey is the caller's single per-graph INSTANCE KEY: mutate(delete)
// passes its `language`, manage(prune) passes its `name`.
func reEmitDeletedFromSegments(ctx context.Context, deps ClientDeps, graph, instanceKey string, ids []string) error {
	deleter := deps.SegmentDeleter()
	if deleter == nil || len(ids) == 0 {
		return nil
	}
	gt, name := deleteSegmentTarget(graph, instanceKey)
	// searchengine.ExternalID is an alias for string, so the tombstoned ids cross the
	// seam as they are.
	if err := deleter.DeleteFromBuckets(ctx, gt, name, ids); err != nil {
		slog.Warn("segment delete re-emit failed; the removal is applied but not yet durable in the shipped corpus",
			"graph_type", gt, "name", name, "ids", len(ids), "error", err)
		return err
	}
	return nil
}

// deleteSegmentTarget resolves the (graph type, instance name) the segment engine
// keys this removal's corpus on, from a graph and the caller's single per-graph
// instanceKey (mutate(delete) passes `language`, manage(prune) passes `name`). An
// absent graph is the knowledge graph under its default instance, a practice graph
// is keyed by its language, and anything else falls back to the graph's own name.
// It mirrors pivotEngineKey's default branch, which resolves the same key from the
// richer query argument set.
//
// Against manage's own selector lowering the two resolve identically EXCEPT on the
// knowledge arm: manage can address a non-default knowledge instance server-side,
// while this returns the default unconditionally. That is harmless because the
// knowledge graph keys ALL client-hosted segments under knowledgeDefaultName —
// there is no second knowledge corpus for a key to select.
func deleteSegmentTarget(graph, instanceKey string) (kgtypes.GraphType, string) {
	gt := kgtypes.GraphType(graph)
	if graph == "" {
		gt = kgtypes.GraphKnowledge
	}
	if gt == kgtypes.GraphKnowledge {
		return gt, knowledgeDefaultName
	}
	if instanceKey != "" {
		return gt, instanceKey
	}
	// A SINGLE-INSTANCE FAMILY RESOLVES TO ITS CANONICAL INSTANCE rather than
	// falling through to the graph-type-name fallback below. Without this the
	// checks graph keyed its segment tombstones under the literal "checks" — a
	// THIRD spelling, neither the empty name a caller sends nor the "default" the
	// collector seals under — so deleting a check removed the node while leaving
	// its segment entry searchable.
	if canonical := workingset.CanonicalInstanceName(gt, ""); canonical != "" {
		return gt, canonical
	}
	// The fallback for a family that DOES carry an instance field but was given
	// none: the graph-type name, unchanged.
	return gt, string(gt)
}
