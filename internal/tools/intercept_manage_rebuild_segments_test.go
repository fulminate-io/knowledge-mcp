// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// --- fakes -------------------------------------------------------------------

// fakeRebuildScanner returns scripted segment_rebuild pages keyed by the after_id
// cursor. It records every requested after_id so the test can assert the cursor
// advanced to each page's last node_id and terminated on the empty page.
type fakeRebuildScanner struct {
	mu       sync.Mutex
	pages    [][]*knowledgev1.PipelineScanItem // returned in order
	calls    int
	cursors  []string
	pageIter int
}

func (f *fakeRebuildScanner) PipelineScan(_ context.Context, req *knowledgev1.PipelineScanRequest) (*knowledgev1.PipelineScanResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.cursors = append(f.cursors, req.GetAfterId())
	if f.pageIter >= len(f.pages) {
		return &knowledgev1.PipelineScanResponse{Items: nil}, nil
	}
	page := f.pages[f.pageIter]
	f.pageIter++
	return &knowledgev1.PipelineScanResponse{Items: page}, nil
}

func (f *fakeRebuildScanner) Execute(context.Context, *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	return &knowledgev1.ExecuteResponse{}, nil
}

// fakeRebuildShipper records every Add/Flush/Invalidate call. It is the T1 proof
// surface: it must record ZERO per-chunk ships (only Add-only entrypoints fire),
// exactly ONE FlushDeterministic, and that flush must happen AFTER every Add.
type fakeRebuildShipper struct {
	mu sync.Mutex

	addDetCalls   atomic.Int64
	addFieldCalls atomic.Int64
	flushCalls    atomic.Int64
	invalidate    [][]searchengine.SegmentID

	hnswDocTotal int
	bm25DocTotal int

	// addsBeforeFlush records the Add count observed at the moment Flush fires —
	// proving Flush ran AFTER all Adds.
	addsAtFlush int64

	pruned  []searchengine.SegmentID
	addErr  error // when set, AddDeterministic returns it (drives the abort path)
	flushed bool
}

func (s *fakeRebuildShipper) AddDeterministic(_ context.Context, _ kgtypes.GraphType, _ string, docs []searchengine.Document) error {
	if s.addErr != nil {
		return s.addErr
	}
	s.addDetCalls.Add(1)
	s.mu.Lock()
	s.hnswDocTotal += len(docs)
	s.mu.Unlock()
	return nil
}

func (s *fakeRebuildShipper) AddFields(_ context.Context, _ kgtypes.GraphType, _ string, docs []searchengine.Document) error {
	s.addFieldCalls.Add(1)
	s.mu.Lock()
	s.bm25DocTotal += len(docs)
	s.mu.Unlock()
	return nil
}

func (s *fakeRebuildShipper) FlushDeterministic(_ context.Context, _ kgtypes.GraphType, _ string) ([]searchengine.SegmentID, error) {
	s.flushCalls.Add(1)
	s.mu.Lock()
	s.flushed = true
	s.addsAtFlush = s.addDetCalls.Load()
	s.mu.Unlock()
	return s.pruned, nil
}

func (s *fakeRebuildShipper) InvalidateLocal(_ kgtypes.GraphType, _ string, ids []searchengine.SegmentID) {
	s.mu.Lock()
	s.invalidate = append(s.invalidate, ids)
	s.mu.Unlock()
}

// --- helpers -----------------------------------------------------------------

// makeScanPage builds a page of n items with ascending ids prefixed by `prefix`
// so the cursor ordering is deterministic. Each item carries a 32-byte vector and
// a one-field BM25 map.
func makeScanPage(prefix string, start, n int) []*knowledgev1.PipelineScanItem {
	page := make([]*knowledgev1.PipelineScanItem, 0, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("%s%08d", prefix, start+i)
		vec := make([]byte, 32)
		vec[0] = byte(i)
		page = append(page, &knowledgev1.PipelineScanItem{
			NodeId:       id,
			GraphName:    "myrepo",
			BinaryVector: vec,
			Bm25Fields:   &knowledgev1.Bm25Fields{SymbolName: id},
		})
	}
	return page
}

// --- tests -------------------------------------------------------------------

// TestRebuildSegments_PagesChunksShipsOnce is the happy-path T1 proof: the cursor
// advances per page and terminates on the empty page; each full
// DefaultMinSegmentDocs chunk fires Add-ONLY AddDeterministic + AddFields (ZERO
// per-chunk ship); exactly ONE FlushDeterministic fires AFTER all Adds and returns
// the fake's pruned ids; InvalidateLocal fires once with exactly those ids.
func TestRebuildSegments_PagesChunksShipsOnce(t *testing.T) {
	min := searchengine.DefaultMinSegmentDocs
	// Two full pages of exactly `min` items each → exactly 2 full chunks, no tail.
	page1 := makeScanPage("a", 0, min)
	page2 := makeScanPage("b", 0, min)
	scanner := &fakeRebuildScanner{pages: [][]*knowledgev1.PipelineScanItem{page1, page2}}

	pruned := []searchengine.SegmentID{"seg-superseded-1", "seg-superseded-2"}
	shipper := &fakeRebuildShipper{pruned: pruned}

	deps := rebuildClientDeps{scanner: scanner, shipper: shipper}
	res := handleClientRebuildSegments(context.Background(), deps, manageArgs{
		Operation: "rebuild_segments", Graph: "code", Name: "myrepo",
	})
	require.False(t, res.IsError, "happy path must not error: %v", res.Content)

	// Cursor: page1 → after_id="" ; page2 → after_id=last(page1) ; empty page →
	// after_id=last(page2). 3 scans total (2 full + 1 terminating empty).
	require.Equal(t, 3, scanner.calls, "two full pages then one empty terminator")
	require.Equal(t, []string{"", page1[len(page1)-1].GetNodeId(), page2[len(page2)-1].GetNodeId()}, scanner.cursors,
		"after_id advances to each page's last node_id and the loop terminates on the empty page")

	// Build-concurrent / ship-once: 2 full chunks → 2 AddDeterministic + 2 AddFields,
	// ZERO per-chunk ship (the fake has no ship method — Add-only by construction),
	// exactly ONE FlushDeterministic.
	require.Equal(t, int64(2), shipper.addDetCalls.Load(), "one AddDeterministic per full chunk")
	require.Equal(t, int64(2), shipper.addFieldCalls.Load(), "one AddFields per full chunk")
	require.Equal(t, int64(1), shipper.flushCalls.Load(), "exactly ONE FlushDeterministic — ship happens once, never per-chunk")
	require.Equal(t, int64(2), shipper.addsAtFlush, "FlushDeterministic ran AFTER all chunk Adds")

	// InvalidateLocal fired once with exactly the pruned ids FlushDeterministic returned.
	require.Len(t, shipper.invalidate, 1)
	require.Equal(t, pruned, shipper.invalidate[0], "InvalidateLocal evicts exactly the FlushDeterministic-returned superseded ids")

	// Every scanned doc reached the engine.
	require.Equal(t, 2*min, shipper.hnswDocTotal)
	require.Equal(t, 2*min, shipper.bm25DocTotal)
}

// TestRebuildSegments_TailSealedByFlush proves a sub-threshold trailing remainder
// is NOT sent via a concurrent full-chunk Add but IS Added (buffered) so
// FlushDeterministic seals it. One page of min+5 items → 1 full chunk + a 5-item tail.
func TestRebuildSegments_TailSealedByFlush(t *testing.T) {
	min := searchengine.DefaultMinSegmentDocs
	page := makeScanPage("a", 0, min+5)
	scanner := &fakeRebuildScanner{pages: [][]*knowledgev1.PipelineScanItem{page}}
	shipper := &fakeRebuildShipper{}

	deps := rebuildClientDeps{scanner: scanner, shipper: shipper}
	res := handleClientRebuildSegments(context.Background(), deps, manageArgs{Graph: "code", Name: "myrepo"})
	require.False(t, res.IsError)

	// 1 full chunk + 1 tail Add = 2 AddDeterministic calls; all docs reach the engine.
	require.Equal(t, int64(2), shipper.addDetCalls.Load(), "one full chunk + one tail Add")
	require.Equal(t, int64(1), shipper.flushCalls.Load())
	require.Equal(t, min+5, shipper.hnswDocTotal, "every scanned vector reaches the engine (full chunk + tail)")
}

// TestRebuildSegments_AddErrorAbortsBeforeFlush is the T3 fail-closed proof: a
// concurrent Add error aborts the rebuild BEFORE FlushDeterministic — ZERO
// FlushDeterministic and ZERO InvalidateLocal, so no partial set is shipped.
func TestRebuildSegments_AddErrorAbortsBeforeFlush(t *testing.T) {
	min := searchengine.DefaultMinSegmentDocs
	page := makeScanPage("a", 0, min)
	scanner := &fakeRebuildScanner{pages: [][]*knowledgev1.PipelineScanItem{page}}
	shipper := &fakeRebuildShipper{addErr: errors.New("engine.Add boom")}

	deps := rebuildClientDeps{scanner: scanner, shipper: shipper}
	res := handleClientRebuildSegments(context.Background(), deps, manageArgs{Graph: "code", Name: "myrepo"})

	require.True(t, res.IsError, "an Add error must surface as a tool error")
	require.Equal(t, int64(0), shipper.flushCalls.Load(), "ABORT before flush — ZERO FlushDeterministic on an Add error")
	require.Empty(t, shipper.invalidate, "no InvalidateLocal — no partial ship")
}

// TestRebuildSegments_SingleFlightRejectsOverlap proves a second concurrent
// rebuild for the SAME repo is single-flight rejected (returns a started-ack, does
// not run a duplicate scan). A blocking scanner holds the first invocation open
// while the second fires.
func TestRebuildSegments_SingleFlightRejectsOverlap(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{})
	blocking := &blockingScanner{entered: entered, release: release}
	shipper := &fakeRebuildShipper{}
	deps := rebuildClientDeps{scanner: blocking, shipper: shipper}

	var firstRes atomic.Value
	go func() {
		firstRes.Store(handleClientRebuildSegments(context.Background(), deps, manageArgs{Graph: "code", Name: "samerepo"}))
	}()
	<-entered // first invocation has claimed the single-flight slot and is blocked in scan

	// Second invocation for the SAME repo must be rejected immediately.
	second := handleClientRebuildSegments(context.Background(), deps, manageArgs{Graph: "code", Name: "samerepo"})
	require.False(t, second.IsError)
	require.Contains(t, second.Content[0].Text, "already in progress", "overlapping rebuild is single-flight rejected")

	close(release) // let the first finish
}

// blockingScanner blocks on the first PipelineScan until released, signalling
// `entered` so the test knows the single-flight slot is claimed.
type blockingScanner struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingScanner) PipelineScan(_ context.Context, _ *knowledgev1.PipelineScanRequest) (*knowledgev1.PipelineScanResponse, error) {
	b.once.Do(func() {
		close(b.entered)
		<-b.release
	})
	return &knowledgev1.PipelineScanResponse{Items: nil}, nil
}

func (b *blockingScanner) Execute(context.Context, *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	return &knowledgev1.ExecuteResponse{}, nil
}

// rebuildClientDeps is the minimal ClientDeps the driver reads: PipelineScanner +
// SegmentShipper. Every other accessor returns a zero value.
type rebuildClientDeps struct {
	scanner PipelineScanner
	shipper SegmentShipper
}

func (rebuildClientDeps) GraphClient() *graphclient.GraphClient { return nil }
func (rebuildClientDeps) Sink() collector.Sink                  { return nil }
func (rebuildClientDeps) RootDir() string                       { return "" }
func (rebuildClientDeps) WorkerRuntime() WorkerRuntimeAPI       { return nil }
func (rebuildClientDeps) WorkerCRUD() WorkerCRUDAPI             { return nil }
func (rebuildClientDeps) Embedder() embed.BinaryEmbedder        { return nil }
func (rebuildClientDeps) BackendResolver() BackendResolver      { return nil }
func (rebuildClientDeps) GraphCaller() GraphCaller              { return nil }
func (rebuildClientDeps) LocalGraphCaller() GraphCaller         { return nil }
func (rebuildClientDeps) RepoResolver() *RepoResolver           { return nil }
func (rebuildClientDeps) SegmentManager() SegmentSearcher       { return nil }
func (d rebuildClientDeps) SegmentShipper() SegmentShipper      { return d.shipper }
func (d rebuildClientDeps) PipelineScanner() PipelineScanner    { return d.scanner }
