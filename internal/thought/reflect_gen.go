// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// reflect_gen.go holds the client-side quiet-tick reflection plumbing: the
// one-RPC reflect-gen probe and the last-reflected-gen watermark persistence. The
// hourly PropagationLoop reads the probe + watermark at the start of each tick and
// skips the whole pass when the reflect gen has not advanced since the last
// completed pass (runBackgroundPropagation, loop.go).

const (
	// watermarkNodeID is the fixed well-known ID of the singleton node carrying
	// the last-reflected-gen watermark. A deterministic ID makes the read a single
	// O(1) by-id query and the write an idempotent upsert — no name search needed.
	watermarkNodeID = "reflection-watermark"

	// watermarkNodeName is the human-readable name of the watermark singleton.
	watermarkNodeName = "reflection-watermark"

	// watermarkGenKey is the metadata key under which the watermark gen is stored.
	watermarkGenKey = "last_reflected_gen"

	// reflectWatermarkNodeID is the singleton node carrying the max-UpdatedAt
	// reflection watermark (unix nanos) — DISTINCT from the reflect-gen watermark
	// above. The dirty-seed derivation reads it as the per-tick UpdatedAt cutoff;
	// the loop persists max(UpdatedAt) after a completed warm pass. A separate
	// singleton (not a second key on the gen node) keeps the two watermarks
	// independently writable.
	reflectWatermarkNodeID = "reflection-updatedat-watermark"

	// reflectWatermarkNodeName is the human-readable name of the max-UpdatedAt
	// watermark singleton.
	reflectWatermarkNodeName = "reflection-updatedat-watermark"

	// reflectWatermarkKey is the metadata key under which the max-UpdatedAt
	// watermark (unix nanos) is stored.
	reflectWatermarkKey = "last_reflected_updatedat"
)

// probeReflectGen issues exactly ONE PipelineScan on the reflect axis for the
// knowledge graph and returns (dirtyGen, true). On any error or when the caller
// does not satisfy reflectProbe (no PipelineScan — e.g. an Execute-only test
// fake), it returns (0, false) so the caller treats the probe as unavailable and
// RUNS the pass (never skips on probe failure). LastSeenGen is 0 so the handler
// always reports the live gen rather than short-circuiting.
func probeReflectGen(ctx context.Context, probe reflectProbe) (uint64, bool) {
	if probe == nil {
		return 0, false
	}
	// Background quiet-tick probe with no originating tool call.
	ctx = graphclient.WithOperation(ctx, graphclient.OpPropagationReflect)
	resp, err := probe.PipelineScan(ctx, &knowledgev1.PipelineScanRequest{
		GraphType:   "knowledge",
		GraphName:   "default",
		Axis:        "reflect",
		LastSeenGen: 0,
	})
	if err != nil {
		slog.Warn("thought: probeReflectGen: PipelineScan failed", "err", err)
		return 0, false
	}
	return resp.GetDirtyGen(), true
}

// readLastReflectedGen reads the persisted last-reflected-gen watermark from the
// singleton resource node. Returns 0 when the node is absent (first run) or on
// any read/parse error — a 0 watermark means "never reflected", so the pass runs.
func readLastReflectedGen(ctx context.Context, gc Caller) uint64 {
	if gc == nil {
		return 0
	}
	raw, err := json.Marshal(map[string]any{"id": watermarkNodeID})
	if err != nil {
		return 0
	}
	resp, err := executeViaEngine(ctx, gc, "query", raw)
	if err != nil {
		return 0
	}
	nodes, derr := engine.DecodeNodes(resp)
	if derr != nil || len(nodes) == 0 {
		return 0
	}
	v := kgtypes.Value(nodes[0], watermarkGenKey)
	if v == "" {
		return 0
	}
	gen, perr := strconv.ParseUint(v, 10, 64)
	if perr != nil {
		return 0
	}
	return gen
}

// writeLastReflectedGen persists gen as the last-reflected-gen watermark via an
// idempotent upsert of the singleton resource node (create-on-first-write). The
// node type is `resource` — a NON-reflection type — so this write does NOT itself
// advance the reflect gen (Phase 1's bump is type-gated to thought/charge),
// avoiding a self-trigger. This is why the watermark lives on a resource node, not
// a thought node.
func writeLastReflectedGen(ctx context.Context, gc Caller, gen uint64) error {
	if gc == nil {
		return nil
	}
	raw, err := json.Marshal(map[string]any{
		"operation": "upsert",
		"id":        watermarkNodeID,
		"type":      "resource",
		"name":      watermarkNodeName,
		"summary":   "Singleton watermark: the reflect dirty-gen of the last completed reflection pass.",
		"metadata":  map[string]string{watermarkGenKey: strconv.FormatUint(gen, 10)},
	})
	if err != nil {
		return err
	}
	if _, err := executeViaEngine(ctx, gc, "mutate", raw); err != nil {
		return err
	}
	return nil
}

// readLastReflectedWatermark reads the persisted max-UpdatedAt reflection
// watermark (unix nanos) from its singleton resource node. Returns 0 when the
// node is absent (first run) or on any read/parse error — a 0 watermark means
// "never reflected", so every thought's UpdatedAt exceeds it and the first warm
// pass seeds the whole corpus (which the cold-start full pass already covers).
func readLastReflectedWatermark(ctx context.Context, gc Caller) int64 {
	if gc == nil {
		return 0
	}
	raw, err := json.Marshal(map[string]any{"id": reflectWatermarkNodeID})
	if err != nil {
		return 0
	}
	resp, err := executeViaEngine(ctx, gc, "query", raw)
	if err != nil {
		return 0
	}
	nodes, derr := engine.DecodeNodes(resp)
	if derr != nil || len(nodes) == 0 {
		return 0
	}
	v := kgtypes.Value(nodes[0], reflectWatermarkKey)
	if v == "" {
		return 0
	}
	wm, perr := strconv.ParseInt(v, 10, 64)
	if perr != nil {
		return 0
	}
	return wm
}

// writeLastReflectedWatermark persists wm (max Node.UpdatedAt, unix nanos) as the
// reflection watermark via an idempotent upsert of the singleton resource node.
// The node type is `resource` — a NON-reflection type — so this write does NOT
// advance the reflect gen (the gen bump is type-gated to thought/charge), avoiding
// a self-trigger.
func writeLastReflectedWatermark(ctx context.Context, gc Caller, wm int64) error {
	if gc == nil {
		return nil
	}
	raw, err := json.Marshal(map[string]any{
		"operation": "upsert",
		"id":        reflectWatermarkNodeID,
		"type":      "resource",
		"name":      reflectWatermarkNodeName,
		"summary":   "Singleton watermark: the max Node.UpdatedAt (unix nanos) of the last completed reflection pass.",
		"metadata":  map[string]string{reflectWatermarkKey: strconv.FormatInt(wm, 10)},
	})
	if err != nil {
		return err
	}
	if _, err := executeViaEngine(ctx, gc, "mutate", raw); err != nil {
		return err
	}
	return nil
}
