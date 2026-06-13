// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"maps"
	"sort"
	"strconv"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// similarity_event.go persists ONE event node PER similarity-lever pass — the
// running→completed/failed status record the async lever creates at trigger time
// and updates at completion. The event id is PER-PASS (a fresh crypto/rand id each
// trigger), NOT the singleton shape writeLastFullPass uses: every pass is its own
// auditable record, and the latest-by-started_at read is what surfaces the most
// recent one.
//
// COMPLETION RE-SUPPLIES THE FULL METADATA MAP because mutate(upsert) is a
// whole-node REPLACE: the upsert overwrites the node's entire metadata map with
// the new payload, it does NOT merge per key. A completion payload carrying only
// the new keys would ERASE the similarity_pass marker + started_at + thresholds the
// running record wrote, and readLatestSimilarityEvent filters on that marker — so
// the report would vanish. The trigger-time values are threaded in as args (pure
// data flow, no read-modify-write) so completeSimilarityEvent rebuilds the complete
// map. Do NOT reintroduce a partial completion payload.
//
// Event nodes do NOT embed (NodeEvent carries the zero NodeTypeBehavior): the
// rendered report lives in content/description as the persisted body the fetch op
// returns verbatim, fetched by id/marker and NEVER vector-searched. The summary is
// a fixed string supplied only to satisfy create-time validation, not for search.

// similarity-event metadata keys (the event-node metadata map is string→string).
// The keys the tools-side estimate + fetch-op rendering read (status, started_at,
// duration_ms) AND the headline-count keys the tools-side completion callback
// builds into the headline map are EXPORTED — they cross the package seam, so a
// single source of truth keeps the writer (here) and the readers (tools) in lockstep.
const (
	metaSimPassMarker     = "similarity_pass" // presence marks an event as a similarity-pass record (the fetch-op filter key)
	metaSimCompletedAt    = "similarity_completed_at"
	metaSimLinkThreshold  = "similarity_link_threshold"
	metaSimMergeThreshold = "similarity_merge_threshold"

	MetaSimStatus     = "similarity_status" // running | completed | failed
	MetaSimStartedAt  = "similarity_started_at"
	MetaSimDurationMs = "similarity_duration_ms"

	// Headline counts mirrored onto the event for at-a-glance reads.
	MetaSimMerges = "similarity_merges"
	MetaSimLinks  = "similarity_links"
	MetaSimTopics = "similarity_topics"
)

// similarity-event status values. Exported because the tools-side estimate + fetch
// op branch on them.
const (
	SimStatusRunning   = "running"
	SimStatusCompleted = "completed"
	SimStatusFailed    = "failed"
)

// similarityEventName is the fixed human label every similarity-pass event carries.
const similarityEventName = "Topic similarity pass"

// similarityEventSummary is the fixed <=500-char summary supplied to satisfy
// create-time validation. It is NOT a search vector — event nodes never embed.
const similarityEventSummary = "Topic-similarity lever pass status record (running/completed/failed) carrying the full rendered report in its body."

// newSimilarityEventID returns a fresh 32-char hex id for one similarity-pass
// event. In-module equivalent of the server's generateNodeID (crypto/rand 16
// bytes → hex): the server helper lives in a module the client cannot import, and
// the only other in-module id generator is bundle-scoped.
func newSimilarityEventID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// createRunningSimilarityEvent writes the status=running event node at trigger
// time via an idempotent upsert (the writeLastFullPass marshal shape). It carries
// the marker, started_at, and the resolved link/merge thresholds so an in-progress
// fetch and the completion REPLACE both have them. The summary is supplied
// defensively for create validation only (events never embed → not searchable).
func createRunningSimilarityEvent(ctx context.Context, gc Caller, id string, startedAt time.Time, link, merge float64) error {
	if gc == nil {
		return nil
	}
	raw, err := json.Marshal(map[string]any{
		"operation": "upsert",
		"id":        id,
		"type":      string(kgtypes.NodeEvent),
		"name":      similarityEventName,
		"summary":   similarityEventSummary,
		"metadata": map[string]string{
			metaSimPassMarker:     "1",
			MetaSimStatus:         SimStatusRunning,
			MetaSimStartedAt:      startedAt.Format(time.RFC3339),
			metaSimLinkThreshold:  strconv.FormatFloat(link, 'f', -1, 64),
			metaSimMergeThreshold: strconv.FormatFloat(merge, 'f', -1, 64),
		},
	})
	if err != nil {
		return err
	}
	_, err = executeViaEngine(ctx, gc, "mutate", raw)
	return err
}

// completeSimilarityEvent REPLACES the running record (same id) with the terminal
// status. Because upsert is a whole-node REPLACE it re-supplies the FULL metadata
// map — the marker, started_at, and thresholds are threaded in as args (pure data
// flow, no read-modify-write) so the record stays matchable by
// readLatestSimilarityEvent. content/description carry the FULL rendered report
// text as the persisted body the fetch op returns verbatim (events never embed —
// this is persisted, not searchable). headline carries the at-a-glance count keys.
func completeSimilarityEvent(
	ctx context.Context,
	gc Caller,
	id string,
	startedAt time.Time,
	link, merge float64,
	status string,
	completedAt time.Time,
	durationMs int64,
	renderedReport string,
	headline map[string]string,
) error {
	if gc == nil {
		return nil
	}
	metadata := map[string]string{
		metaSimPassMarker:     "1",
		MetaSimStatus:         status,
		MetaSimStartedAt:      startedAt.Format(time.RFC3339),
		metaSimCompletedAt:    completedAt.Format(time.RFC3339),
		MetaSimDurationMs:     strconv.FormatInt(durationMs, 10),
		metaSimLinkThreshold:  strconv.FormatFloat(link, 'f', -1, 64),
		metaSimMergeThreshold: strconv.FormatFloat(merge, 'f', -1, 64),
	}
	maps.Copy(metadata, headline)
	raw, err := json.Marshal(map[string]any{
		"operation":   "upsert",
		"id":          id,
		"type":        string(kgtypes.NodeEvent),
		"name":        similarityEventName,
		"summary":     similarityEventSummary,
		"content":     renderedReport,
		"description": renderedReport,
		"metadata":    metadata,
	})
	if err != nil {
		return err
	}
	_, err = executeViaEngine(ctx, gc, "mutate", raw)
	return err
}

// readLatestSimilarityEvent returns the newest similarity-pass event by started_at,
// ignoring every non-similarity event node. It type-browses `event`, filters to
// nodes carrying the similarity_pass marker, and sorts started_at DESC client-side
// (the recent-browse client-side-sort precedent; similarity-pass cardinality is one
// per manual lever invocation). ok=false when none exists (the empty-state path the
// fetch op renders as a clear message). A completed event is still matched because
// completeSimilarityEvent re-supplies the marker + started_at.
func readLatestSimilarityEvent(ctx context.Context, gc Caller) (*knowledgev1.Node, bool) {
	if gc == nil {
		return nil, false
	}
	raw, err := json.Marshal(queryArgs{Type: string(kgtypes.NodeEvent), Limit: browsePageSize})
	if err != nil {
		return nil, false
	}
	resp, err := executeViaEngine(ctx, gc, "query", raw)
	if err != nil {
		return nil, false
	}
	nodes, derr := engine.DecodeNodes(resp)
	if derr != nil {
		return nil, false
	}
	var matched []*knowledgev1.Node
	for _, n := range nodes {
		if kgtypes.Value(n, metaSimPassMarker) != "" {
			matched = append(matched, n)
		}
	}
	if len(matched) == 0 {
		return nil, false
	}
	sort.SliceStable(matched, func(i, j int) bool {
		return kgtypes.Value(matched[i], MetaSimStartedAt) > kgtypes.Value(matched[j], MetaSimStartedAt)
	})
	return matched[0], true
}

// readLatestCompletedSimilarityEvent returns the newest COMPLETED similarity-pass
// event by started_at — the duration source the trigger estimate calibrates against.
// It mirrors readLatestSimilarityEvent (same type-browse + marker filter + client-side
// started_at DESC sort) but adds one predicate: a node must carry status==completed to
// be considered. This is why it is a SEPARATE reader rather than a status param on the
// shared one — the estimate needs the latest COMPLETED event (so it skips the running
// record the trigger just wrote), while the fetch op needs the latest ANY-status event
// to render running/completed/failed state. ok=false when no completed event exists.
func readLatestCompletedSimilarityEvent(ctx context.Context, gc Caller) (*knowledgev1.Node, bool) {
	if gc == nil {
		return nil, false
	}
	raw, err := json.Marshal(queryArgs{Type: string(kgtypes.NodeEvent), Limit: browsePageSize})
	if err != nil {
		return nil, false
	}
	resp, err := executeViaEngine(ctx, gc, "query", raw)
	if err != nil {
		return nil, false
	}
	nodes, derr := engine.DecodeNodes(resp)
	if derr != nil {
		return nil, false
	}
	var matched []*knowledgev1.Node
	for _, n := range nodes {
		if kgtypes.Value(n, metaSimPassMarker) != "" && kgtypes.Value(n, MetaSimStatus) == SimStatusCompleted {
			matched = append(matched, n)
		}
	}
	if len(matched) == 0 {
		return nil, false
	}
	sort.SliceStable(matched, func(i, j int) bool {
		return kgtypes.Value(matched[i], MetaSimStartedAt) > kgtypes.Value(matched[j], MetaSimStartedAt)
	})
	return matched[0], true
}

// readSimilarityEventByID fetches one similarity-pass event by id (the optional-id
// fetch path), mirroring readLastFullPass's O(1) by-id query. ok=false when the
// node is absent or is not a similarity-pass record.
func readSimilarityEventByID(ctx context.Context, gc Caller, id string) (*knowledgev1.Node, bool) {
	n, ok := fetchNode(ctx, gc, id)
	if !ok || kgtypes.Value(n, metaSimPassMarker) == "" {
		return nil, false
	}
	return n, true
}

// BeginSimilarityEvent is the exported event-create seam the tools-side async
// trigger drives: it mints a fresh per-pass id, captures the trigger time, and
// writes the status=running event. Returns the id + startedAt so the completion
// callback can thread them back into FinishSimilarityEvent (upsert is a whole-node
// REPLACE — completion must re-supply started_at + thresholds, not read them back).
func (p *PropagationLoop) BeginSimilarityEvent(ctx context.Context, link, merge float64) (string, time.Time, error) {
	id := newSimilarityEventID()
	startedAt := time.Now()
	if err := createRunningSimilarityEvent(ctx, p.gc, id, startedAt, link, merge); err != nil {
		return id, startedAt, err
	}
	return id, startedAt, nil
}

// FinishSimilarityEvent is the exported completion seam: it REPLACES the running
// record with the terminal status, re-supplying the FULL metadata map (the
// started_at + thresholds threaded back from BeginSimilarityEvent) plus the rendered
// report body and headline counts. completedAt is stamped here at completion time.
func (p *PropagationLoop) FinishSimilarityEvent(
	ctx context.Context,
	id string,
	startedAt time.Time,
	link, merge float64,
	status string,
	durationMs int64,
	rendered string,
	headline map[string]string,
) error {
	return completeSimilarityEvent(ctx, p.gc, id, startedAt, link, merge, status, time.Now(), durationMs, rendered, headline)
}

// LatestSimilarityEvent is the exported latest-by-started_at read backing the
// similarity_report fetch op (no id supplied).
func (p *PropagationLoop) LatestSimilarityEvent(ctx context.Context) (*knowledgev1.Node, bool) {
	return readLatestSimilarityEvent(ctx, p.gc)
}

// LatestCompletedSimilarityEvent is the exported latest-COMPLETED-by-started_at read
// backing the trigger estimate: it skips the running record the trigger just wrote and
// returns the most recent finished pass (whose duration_ms calibrates the estimate).
func (p *PropagationLoop) LatestCompletedSimilarityEvent(ctx context.Context) (*knowledgev1.Node, bool) {
	return readLatestCompletedSimilarityEvent(ctx, p.gc)
}

// SimilarityEventByID is the exported by-id read backing the optional-id fetch path.
func (p *PropagationLoop) SimilarityEventByID(ctx context.Context, id string) (*knowledgev1.Node, bool) {
	return readSimilarityEventByID(ctx, p.gc, id)
}
