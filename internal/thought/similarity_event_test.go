// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"maps"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// eventStoreCaller models the wire surface the event helpers drive, with REPLACE
// upsert semantics matching the real backend: an upsert stores the node WHOLESALE
// keyed by id — the new metadata map REPLACES the prior entry, never a key merge.
// This is the regression-guard behavior the round-trip test relies on: a partial
// completion payload would erase the similarity_pass marker, so the round-trip read
// must fail if completion does not re-supply the full map.
type eventStoreCaller struct {
	store   map[string]*knowledgev1.Node // id → node (REPLACE: whole-node, last-write-wins)
	upserts []*knowledgev1.NodeBody      // every captured upsert body, in order
	extra   []*knowledgev1.Node          // non-similarity event nodes returned by the browse
}

func newEventStoreCaller() *eventStoreCaller {
	return &eventStoreCaller{store: map[string]*knowledgev1.Node{}}
}

func (c *eventStoreCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	if m := req.GetMutation(); m != nil {
		if m.GetKind() == knowledgev1.MutationPlan_MUTATION_KIND_UPSERT {
			for _, b := range m.GetNodeBodies() {
				c.upserts = append(c.upserts, b)
				// REPLACE: store the node wholesale keyed by id; a fresh metadata map
				// REPLACES any prior entry (NO per-key merge).
				meta := map[string]string{}
				maps.Copy(meta, b.GetMetadata())
				c.store[b.GetId()] = &knowledgev1.Node{
					Id:          b.GetId(),
					Type:        b.GetType(),
					SymbolName:  b.GetName(),
					Summary:     b.GetSummary(),
					Content:     b.GetContent(),
					Description: b.GetDescription(),
					Metadata:    meta,
				}
			}
		}
		return &knowledgev1.ExecuteResponse{}, nil
	}
	q := req.GetQuery()
	if q == nil {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	// by-id hydrate (readSimilarityEventByID via fetchNode).
	if len(q.GetIds()) > 0 {
		var out []*knowledgev1.Node
		for _, id := range q.GetIds() {
			if n, ok := c.store[id]; ok {
				out = append(out, n)
			}
		}
		return &knowledgev1.ExecuteResponse{Nodes: out}, nil
	}
	// Offset beyond the first page → exhausted (the browse drain stops on a short page).
	if q.GetOffset() > 0 {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	// type=event browse → every stored node plus any injected non-similarity events.
	if q.GetSelection().GetNodeType() == string(kgtypes.NodeEvent) {
		var out []*knowledgev1.Node
		for _, n := range c.store {
			out = append(out, n)
		}
		out = append(out, c.extra...)
		return &knowledgev1.ExecuteResponse{Nodes: out}, nil
	}
	return &knowledgev1.ExecuteResponse{}, nil
}

// TestNewSimilarityEventID_Unique: distinct 32-char hex ids across calls.
func TestNewSimilarityEventID_Unique(t *testing.T) {
	seen := map[string]bool{}
	for range 1000 {
		id := newSimilarityEventID()
		assert.Len(t, id, 32, "id must be 32 hex chars (16 bytes)")
		assert.False(t, seen[id], "ids must be distinct across calls")
		seen[id] = true
	}
}

// TestCreateRunningSimilarityEvent: the running upsert carries type=event, the
// similarity_pass marker, status=running, an RFC3339 started_at, and the passed id.
func TestCreateRunningSimilarityEvent(t *testing.T) {
	gc := newEventStoreCaller()
	id := newSimilarityEventID()
	started := time.Now()

	require.NoError(t, createRunningSimilarityEvent(context.Background(), gc, id, started, 0.90, 0.97))

	require.Len(t, gc.upserts, 1)
	b := gc.upserts[0]
	assert.Equal(t, id, b.GetId(), "the upsert carries the passed id")
	assert.Equal(t, string(kgtypes.NodeEvent), b.GetType(), "type=event")
	meta := b.GetMetadata()
	assert.NotEmpty(t, meta[metaSimPassMarker], "the similarity_pass marker is set")
	assert.Equal(t, SimStatusRunning, meta[MetaSimStatus], "status=running")
	_, perr := time.Parse(time.RFC3339, meta[MetaSimStartedAt])
	assert.NoError(t, perr, "started_at is RFC3339-parseable")
}

// TestCompleteSimilarityEvent_FullMetadataPayload: the completion upsert re-supplies
// the FULL metadata map (per-payload guard) — same id, marker still set,
// status=completed, started_at preserved, thresholds, duration_ms, completed_at, and
// content/description == the rendered report.
func TestCompleteSimilarityEvent_FullMetadataPayload(t *testing.T) {
	gc := newEventStoreCaller()
	id := newSimilarityEventID()
	started := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	completed := started.Add(90 * time.Second)
	report := "=== SIMILARITY REPORT ===\nlinks: 3"

	require.NoError(t, completeSimilarityEvent(context.Background(), gc, id, started,
		0.90, 0.97, SimStatusCompleted, completed, 90000, report,
		map[string]string{MetaSimLinks: "3", MetaSimMerges: "1", MetaSimTopics: "7"}))

	require.Len(t, gc.upserts, 1)
	b := gc.upserts[0]
	assert.Equal(t, id, b.GetId(), "same id")
	meta := b.GetMetadata()
	assert.NotEmpty(t, meta[metaSimPassMarker], "marker STILL set on completion (full map)")
	assert.Equal(t, SimStatusCompleted, meta[MetaSimStatus])
	assert.Equal(t, started.Format(time.RFC3339), meta[MetaSimStartedAt], "started_at preserved")
	assert.Equal(t, completed.Format(time.RFC3339), meta[metaSimCompletedAt])
	assert.Equal(t, "90000", meta[MetaSimDurationMs])
	assert.NotEmpty(t, meta[metaSimLinkThreshold], "link threshold re-supplied")
	assert.NotEmpty(t, meta[metaSimMergeThreshold], "merge threshold re-supplied")
	assert.Equal(t, "3", meta[MetaSimLinks], "headline counts carried")
	assert.Equal(t, report, b.GetContent(), "content == rendered report")
	assert.Equal(t, report, b.GetDescription(), "description == rendered report")
}

// TestSimilarityEvent_ReplaceRoundTrip (FAILS-WHEN-ABSENT, T2-1 regression guard):
// under REPLACE upsert semantics, create-running then complete (same id), then
// readLatestSimilarityEvent returns ok=true, status=completed, started_at == the
// create-time value. A partial completion payload would erase the marker → ok=false,
// the failure this catches.
func TestSimilarityEvent_ReplaceRoundTrip(t *testing.T) {
	gc := newEventStoreCaller()
	id := newSimilarityEventID()
	started := time.Date(2026, 6, 10, 9, 30, 0, 0, time.UTC)
	completed := started.Add(2 * time.Minute)

	require.NoError(t, createRunningSimilarityEvent(context.Background(), gc, id, started, 0.90, 0.97))
	require.NoError(t, completeSimilarityEvent(context.Background(), gc, id, started,
		0.90, 0.97, SimStatusCompleted, completed, 120000, "report body", nil))

	n, ok := readLatestSimilarityEvent(context.Background(), gc)
	require.True(t, ok, "the completed event must still be matched (marker re-supplied under REPLACE)")
	assert.Equal(t, SimStatusCompleted, kgtypes.Value(n, MetaSimStatus))
	assert.Equal(t, started.Format(time.RFC3339), kgtypes.Value(n, MetaSimStartedAt),
		"started_at survives the REPLACE (re-supplied by completion)")
}

// TestReadLatestSimilarityEvent_NewestWins: with three event nodes (two similarity,
// one unrelated), the helper returns the similarity one with the latest started_at
// and ignores the unrelated node.
func TestReadLatestSimilarityEvent_NewestWins(t *testing.T) {
	gc := newEventStoreCaller()
	older := time.Date(2026, 6, 10, 8, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 6, 10, 10, 0, 0, 0, time.UTC)
	idOld := newSimilarityEventID()
	idNew := newSimilarityEventID()
	require.NoError(t, createRunningSimilarityEvent(context.Background(), gc, idOld, older, 0.90, 0.97))
	require.NoError(t, createRunningSimilarityEvent(context.Background(), gc, idNew, newer, 0.90, 0.97))

	// An unrelated event node with NO similarity_pass marker — must be ignored.
	unrelated := &knowledgev1.Node{Id: "unrelated", Type: string(kgtypes.NodeEvent), SymbolName: "deploy"}
	kgtypes.SetValue(unrelated, "something_else", "x")
	gc.extra = append(gc.extra, unrelated)

	n, ok := readLatestSimilarityEvent(context.Background(), gc)
	require.True(t, ok)
	assert.Equal(t, idNew, n.GetId(), "the newest similarity event by started_at wins")
}

// TestReadLatestSimilarityEvent_EmptyState: ok=false when no similarity_pass event
// exists (the empty-state path the fetch op renders as a clear message).
func TestReadLatestSimilarityEvent_EmptyState(t *testing.T) {
	gc := newEventStoreCaller()
	// One unrelated event node, no similarity marker.
	unrelated := &knowledgev1.Node{Id: "u1", Type: string(kgtypes.NodeEvent), SymbolName: "deploy"}
	gc.extra = append(gc.extra, unrelated)

	_, ok := readLatestSimilarityEvent(context.Background(), gc)
	assert.False(t, ok, "no similarity_pass event → ok=false")
}
