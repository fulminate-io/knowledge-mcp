// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"google.golang.org/protobuf/proto"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// mockQuery / mockStats / mockRow / mockSegment / mockFormat mirror the engine's
// own test mock format (searchengine/mockformat_test.go) — re-declared here
// because test files are not importable across packages. A trivial in-memory
// SegmentFormat: indexes Document.Fields[FieldContent], scores by term frequency.

type mockQuery struct{ term string }

type mockStats struct{ totalDocs int }

type mockRow struct {
	ID      searchengine.ExternalID `json:"id"`
	Content string                  `json:"content"`
}

type mockSegment struct{ rows []mockRow }

type mockFormat struct{}

func (mockFormat) Name() string { return "mock" }

func (mockFormat) Build(docs []searchengine.Document) (searchengine.Segment[mockQuery, mockStats], error) {
	rows := make([]mockRow, 0, len(docs))
	for _, d := range docs {
		rows = append(rows, mockRow{ID: d.ID, Content: d.Fields[searchengine.FieldContent]})
	}
	return &mockSegment{rows: rows}, nil
}

func (mockFormat) Decode(blob []byte) (searchengine.Segment[mockQuery, mockStats], error) {
	var rows []mockRow
	if err := json.Unmarshal(blob, &rows); err != nil {
		return nil, err
	}
	return &mockSegment{rows: rows}, nil
}

func (mockFormat) Merge(segs []searchengine.Segment[mockQuery, mockStats], accept []func(searchengine.ExternalID) bool) (searchengine.Segment[mockQuery, mockStats], error) {
	var merged []mockRow
	for i, s := range segs {
		ms := s.(*mockSegment)
		keep := accept[i]
		for _, r := range ms.rows {
			if keep == nil || keep(r.ID) {
				merged = append(merged, r)
			}
		}
	}
	return &mockSegment{rows: merged}, nil
}

func (mockFormat) AggregateStats(segs []searchengine.Segment[mockQuery, mockStats]) mockStats {
	total := 0
	for _, s := range segs {
		total += len(s.(*mockSegment).rows)
	}
	return mockStats{totalDocs: total}
}

func (m *mockSegment) Search(q mockQuery, _ mockStats, k int, accept func(searchengine.ExternalID) bool) []searchengine.Hit {
	var hits []searchengine.Hit
	for _, r := range m.rows {
		if accept != nil && !accept(r.ID) {
			continue
		}
		score := float64(strings.Count(r.Content, q.term))
		if score <= 0 {
			continue
		}
		hits = append(hits, searchengine.Hit{ID: r.ID, Score: score})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].ID < hits[j].ID
	})
	if k > 0 && len(hits) > k {
		hits = hits[:k]
	}
	return hits
}

func (m *mockSegment) IDs() []searchengine.ExternalID {
	ids := make([]searchengine.ExternalID, 0, len(m.rows))
	for _, r := range m.rows {
		ids = append(ids, r.ID)
	}
	return ids
}

func (m *mockSegment) Encode() ([]byte, error) { return json.Marshal(m.rows) }

// countingCaller wraps a segmentCaller and counts Fetch / Ship calls so tests
// can assert "zero Fetch on a cache hit" / "zero Ship on an empty diff".
type countingCaller struct {
	inner        segmentCaller
	fetchCalls   atomic.Int64
	shipCalls    atomic.Int64
	shipBlobs    atomic.Int64
	pruneCalls   atomic.Int64
	publishCalls atomic.Int64

	// shipReqMu guards shipReqBytes — the per-Ship-call serialized request size
	// recorded so the ship-split test can assert every ShipRequest stayed under
	// the cloud cap. One entry per Ship RPC, in call order.
	shipReqMu    sync.Mutex
	shipReqBytes []int
}

func (c *countingCaller) Ship(ctx context.Context, req *knowledgev1.ShipRequest) (*knowledgev1.ShipResponse, error) {
	c.shipCalls.Add(1)
	c.shipBlobs.Add(int64(len(req.GetBlobs())))
	c.shipReqMu.Lock()
	c.shipReqBytes = append(c.shipReqBytes, proto.Size(req))
	c.shipReqMu.Unlock()
	return c.inner.Ship(ctx, req)
}

// recordedShipBytes returns a copy of the per-Ship-call request sizes.
func (c *countingCaller) recordedShipBytes() []int {
	c.shipReqMu.Lock()
	defer c.shipReqMu.Unlock()
	out := make([]int, len(c.shipReqBytes))
	copy(out, c.shipReqBytes)
	return out
}

func (c *countingCaller) ListDelta(ctx context.Context, req *knowledgev1.ListDeltaRequest) (*knowledgev1.ListDeltaResponse, error) {
	return c.inner.ListDelta(ctx, req)
}

func (c *countingCaller) Fetch(ctx context.Context, req *knowledgev1.FetchRequest) (*knowledgev1.FetchResponse, error) {
	c.fetchCalls.Add(1)
	return c.inner.Fetch(ctx, req)
}

func (c *countingCaller) Prune(ctx context.Context, req *knowledgev1.PruneRequest) (*knowledgev1.PruneResponse, error) {
	c.pruneCalls.Add(1)
	return c.inner.Prune(ctx, req)
}

func (c *countingCaller) Publish(ctx context.Context, req *knowledgev1.PublishRequest) (*knowledgev1.PublishResponse, error) {
	c.publishCalls.Add(1)
	return c.inner.Publish(ctx, req)
}

// newMockEngine builds a SegmentedIndex over the mock format with MinSegmentDocs=1
// so every Add seals immediately (one segment per Add batch) — convenient for
// driving discrete segments in tests.
func newMockEngine() *searchengine.SegmentedIndex[mockQuery, mockStats] {
	return searchengine.New[mockQuery, mockStats](mockFormat{}, searchengine.Options{MinSegmentDocs: 1})
}

// doc builds a content document.
func doc(id, content string) searchengine.Document {
	return searchengine.Document{ID: id, Fields: map[string]string{searchengine.FieldContent: content}}
}
