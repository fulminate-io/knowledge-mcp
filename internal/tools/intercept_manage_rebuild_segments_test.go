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
	"github.com/fulminate-io/knowledge-mcp/internal/hivemonitor"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// --- fakes -------------------------------------------------------------------

// fakeRebuildScanner returns scripted segment_rebuild pages keyed by the after_id
// cursor. It records every requested after_id so the test can assert the cursor
// advanced to each page's last node_id and terminated on the empty page.
type fakeRebuildScanner struct {
	mu         sync.Mutex
	pages      [][]*knowledgev1.PipelineScanItem // returned in order
	calls      int
	cursors    []string
	graphTypes []string // the GraphType requested on each PipelineScan
	pageIter   int
}

func (f *fakeRebuildScanner) PipelineScan(_ context.Context, req *knowledgev1.PipelineScanRequest) (*knowledgev1.PipelineScanResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.cursors = append(f.cursors, req.GetAfterId())
	f.graphTypes = append(f.graphTypes, req.GetGraphType())
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

	// Report (additive partial-tail surfacing): two FULL chunks, no tail →
	// built=2, partial=0. The all-full case must read "no partial tail" and
	// otherwise be unchanged.
	body := res.Content[0].Text
	require.Contains(t, body, "2 full deterministic chunks built + shipped", "the full-chunk count is reported")
	require.Contains(t, body, "no partial tail", "an exact-multiple rebuild has no sealed tail to name")
}

// TestRebuildSegments_SubThresholdShipsTail is the Part-2 fails-when-absent proof:
// a PURE sub-threshold rebuild (0<N<DefaultMinSegmentDocs) builds ZERO full chunks
// but DOES Add the tail and FlushDeterministic-seals it, and the rendered report
// NAMES the sealed partial chunk (partial>0) so the result reads as a successful
// ship of the tail — NOT "0 chunks built" with nothing shipped. Reverting the
// partial-count threading makes the report assertion fail.
func TestRebuildSegments_SubThresholdShipsTail(t *testing.T) {
	const n = 5 // 0 < 5 < DefaultMinSegmentDocs (1024): a pure sub-threshold page
	require.Less(t, n, searchengine.DefaultMinSegmentDocs, "fixture must be sub-threshold")
	page := makeScanPage("a", 0, n)
	scanner := &fakeRebuildScanner{pages: [][]*knowledgev1.PipelineScanItem{page}}
	shipper := &fakeRebuildShipper{}

	deps := rebuildClientDeps{scanner: scanner, shipper: shipper}
	res := handleClientRebuildSegments(context.Background(), deps, manageArgs{
		Operation: "rebuild_segments", Graph: "code", Name: "myrepo",
	})
	require.False(t, res.IsError, "a sub-threshold rebuild is a successful ship, not an error: %v", res.Content)

	// ZERO full chunks built (n < minDocs) but the tail IS Added + sealed.
	require.Equal(t, int64(1), shipper.addDetCalls.Load(), "exactly one tail Add (no full chunks)")
	require.Equal(t, int64(1), shipper.flushCalls.Load(), "FlushDeterministic seals the tail once")
	require.Equal(t, n, shipper.hnswDocTotal, "every sub-threshold vector reaches the engine via the tail")

	// The report NAMES the sealed partial tail (partial>0) so built=0 reads as a
	// successful tail ship — reverting the partial threading drops this string.
	body := res.Content[0].Text
	require.Contains(t, body, "0 full deterministic chunks built", "zero full chunks for a sub-threshold page")
	require.Contains(t, body, fmt.Sprintf("%d-node partial tail chunk sealed + shipped", n),
		"the report names the sealed partial tail so a sub-threshold rebuild reads as a successful ship")
}

// TestRebuildSegments_SubThresholdReRunNoOp proves the determinism/idempotency
// property is unchanged by the partial-count surfacing: a second rebuild over the
// same sub-threshold fixture whose FlushDeterministic returns an empty pruned set
// still renders a successful (no-op) ship. The count surfacing is purely a report
// change — it does not perturb the deterministic build.
func TestRebuildSegments_SubThresholdReRunNoOp(t *testing.T) {
	const n = 5
	page := makeScanPage("a", 0, n)
	// A re-run over an unchanged node set ships byte-identical segments → the
	// content-hash diff is empty → FlushDeterministic prunes nothing.
	scanner := &fakeRebuildScanner{pages: [][]*knowledgev1.PipelineScanItem{page}}
	shipper := &fakeRebuildShipper{pruned: nil}

	deps := rebuildClientDeps{scanner: scanner, shipper: shipper}
	res := handleClientRebuildSegments(context.Background(), deps, manageArgs{Graph: "code", Name: "myrepo"})
	require.False(t, res.IsError, "the re-run no-op still renders success: %v", res.Content)

	require.Equal(t, int64(1), shipper.flushCalls.Load(), "the tail is still sealed once on the re-run")
	require.Empty(t, shipper.invalidate[0], "an empty pruned set means nothing superseded (content-hash no-op)")
	body := res.Content[0].Text
	require.Contains(t, body, fmt.Sprintf("%d-node partial tail chunk sealed + shipped", n))
	require.Contains(t, body, "content-hash no-op", "the report still describes the idempotent re-run")
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

// TestRebuildSegments_NotReadyGate (FAILS-WHEN-ABSENT) proves the bind-first
// wiring-window gate (bind-first startup): with PipelineReady()=false, handleClientRebuildSegments
// returns the uniform "daemon still starting" error and does NOT dispatch — even
// with a live scanner + shipper present (the readiness check fires BEFORE the
// nil-handle check, so the scanner is never paged). Dropping the gate would dispatch
// the rebuild instead of returning the not-ready error.
func TestRebuildSegments_NotReadyGate(t *testing.T) {
	min := searchengine.DefaultMinSegmentDocs
	page := makeScanPage("a", 0, min+5)
	scanner := &fakeRebuildScanner{pages: [][]*knowledgev1.PipelineScanItem{page}}
	shipper := &fakeRebuildShipper{}

	deps := rebuildClientDeps{scanner: scanner, shipper: shipper, pipelineNotReady: true}
	res := handleClientRebuildSegments(context.Background(), deps, manageArgs{Graph: "code", Name: "myrepo"})

	require.True(t, res.IsError)
	require.Contains(t, toolResultText(res), "daemon still starting")
	require.Equal(t, int64(0), shipper.addDetCalls.Load(), "must not dispatch the rebuild when not ready")
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

// TestRebuildSegments_RegisteredCustomGraph is the Group B fails-when-absent guard:
// rebuild_segments must accept a REGISTERED custom graph type AND the builtin
// knowledge graph, threading that gt all the way to the PipelineScan wire
// (GraphType=<custom>|knowledge, not the hardcoded code); a nameless knowledge
// rebuild defaults to "default" and a knowledge "@"-overlay name is rejected
// (base-only v1). Another builtin (practice) and an unregistered typo are rejected
// by the registry gate. Reverting the gt threading (back to kgtypes.GraphCode)
// makes the wire-GraphType assertion FAIL; reverting the knowledge gate makes the
// knowledge-accepted assertion FAIL; reverting the registry gate makes the
// custom-accepted assertion FAIL.
func TestRebuildSegments_RegisteredCustomGraph(t *testing.T) {
	const customType = "hellograph"
	crud := &fakeGraphTypeCRUD{graph: map[string]*knowledgev1.GraphTypeDef{
		customType: {Name: customType},
	}}

	t.Run("registered custom graph is accepted and gt reaches the scanner", func(t *testing.T) {
		min := searchengine.DefaultMinSegmentDocs
		scanner := &fakeRebuildScanner{pages: [][]*knowledgev1.PipelineScanItem{makeScanPage("a", 0, min)}}
		shipper := &fakeRebuildShipper{}
		deps := rebuildClientDeps{scanner: scanner, shipper: shipper, crud: crud}

		res := handleClientRebuildSegments(context.Background(), deps, manageArgs{
			Operation: "rebuild_segments", Graph: customType, Name: "demo",
		})
		require.False(t, res.IsError, "a registered custom graph must be accepted, not rejected as code-only: %v", res.Content)
		require.Contains(t, res.Content[0].Text, customType, "the rendered result names the custom graph")

		// The threaded gt reached the wire: every PipelineScan carried GraphType=custom.
		require.NotEmpty(t, scanner.graphTypes)
		for _, gt := range scanner.graphTypes {
			require.Equal(t, customType, gt, "the threaded GraphType must reach the PipelineScan request (not hardcoded code)")
		}
	})

	t.Run("builtin knowledge graph is accepted, name defaults to default, gt reaches the scanner", func(t *testing.T) {
		min := searchengine.DefaultMinSegmentDocs
		scanner := &fakeRebuildScanner{pages: [][]*knowledgev1.PipelineScanItem{makeScanPage("a", 0, min)}}
		shipper := &fakeRebuildShipper{}
		deps := rebuildClientDeps{scanner: scanner, shipper: shipper, crud: crud}

		// No name supplied — the builtin knowledge graph defaults to its single
		// canonical "default" instance.
		res := handleClientRebuildSegments(context.Background(), deps, manageArgs{
			Operation: "rebuild_segments", Graph: string(kgtypes.GraphKnowledge),
		})
		require.False(t, res.IsError, "the builtin knowledge graph must be accepted: %v", res.Content)
		require.Contains(t, res.Content[0].Text, "knowledge/default",
			"a nameless knowledge rebuild renders the default instance")

		// The threaded gt reached the wire: every PipelineScan carried GraphType=knowledge.
		require.NotEmpty(t, scanner.graphTypes)
		for _, gt := range scanner.graphTypes {
			require.Equal(t, string(kgtypes.GraphKnowledge), gt,
				"the threaded GraphType must reach the PipelineScan request (knowledge, not hardcoded code)")
		}
	})

	t.Run("knowledge overlay name is rejected (base layer only in v1)", func(t *testing.T) {
		deps := rebuildClientDeps{scanner: &fakeRebuildScanner{}, shipper: &fakeRebuildShipper{}, crud: crud}
		res := handleClientRebuildSegments(context.Background(), deps, manageArgs{
			Graph: string(kgtypes.GraphKnowledge), Name: "default@session-x",
		})
		require.True(t, res.IsError, "a knowledge overlay (@-suffixed) name has no v1 segment rebuild and must be rejected")
		require.Contains(t, res.Content[0].Text, "overlay rebuilds not supported in v1",
			"the rejection names the v1 base-layer-only boundary")
	})

	t.Run("non-code builtin is rejected", func(t *testing.T) {
		deps := rebuildClientDeps{scanner: &fakeRebuildScanner{}, shipper: &fakeRebuildShipper{}, crud: crud}
		res := handleClientRebuildSegments(context.Background(), deps, manageArgs{Graph: "practice", Name: "go"})
		require.True(t, res.IsError, "a non-code/non-knowledge builtin graph has no rebuildable segments and must be rejected")
	})

	t.Run("unregistered custom typo is rejected", func(t *testing.T) {
		deps := rebuildClientDeps{scanner: &fakeRebuildScanner{}, shipper: &fakeRebuildShipper{}, crud: crud}
		res := handleClientRebuildSegments(context.Background(), deps, manageArgs{Graph: "hellogarph", Name: "demo"})
		require.True(t, res.IsError, "an unregistered custom graph type must be rejected")
	})
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
// SegmentShipper + GraphTypeCRUD (for the registry gate). Every other accessor
// returns a zero value.
type rebuildClientDeps struct {
	scanner PipelineScanner
	shipper SegmentShipper
	crud    GraphTypeCRUDAPI
	// pipelineNotReady flips PipelineReady() to false so a test can exercise the
	// bind-first wiring-window gate (bind-first startup). Zero value keeps the pipeline ready.
	pipelineNotReady bool
}

func (rebuildClientDeps) LocalLiveness() LocalLiveness                 { return nil }
func (rebuildClientDeps) Sink() collector.Sink                         { return nil }
func (rebuildClientDeps) RootDir() string                              { return "" }
func (rebuildClientDeps) WorkerRuntime() WorkerRuntimeAPI              { return nil }
func (rebuildClientDeps) WorkerReady() bool                            { return true }
func (rebuildClientDeps) PropReady() bool                              { return true }
func (d rebuildClientDeps) PipelineReady() bool                        { return !d.pipelineNotReady }
func (rebuildClientDeps) ClaimRegistry() *hivemonitor.Registry         { return nil }
func (rebuildClientDeps) BanSet() *hivemonitor.BanSet                  { return nil }
func (rebuildClientDeps) WorkerCRUD() WorkerCRUDAPI                    { return nil }
func (d rebuildClientDeps) GraphTypeCRUD() GraphTypeCRUDAPI            { return d.crud }
func (rebuildClientDeps) Embedder() embed.BinaryEmbedder               { return nil }
func (rebuildClientDeps) BackendResolver() BackendResolver             { return nil }
func (rebuildClientDeps) GraphCaller() GraphCaller                     { return nil }
func (rebuildClientDeps) LocalGraphCaller() GraphCaller                { return nil }
func (rebuildClientDeps) RepoResolver() *RepoResolver                  { return nil }
func (rebuildClientDeps) SegmentManager() SegmentSearcher              { return nil }
func (rebuildClientDeps) SegmentVectorResolver() SegmentVectorResolver { return nil }
func (d rebuildClientDeps) SegmentShipper() SegmentShipper             { return d.shipper }
func (rebuildClientDeps) SegmentCoverage() SegmentCoverageReader       { return nil }
func (d rebuildClientDeps) PipelineScanner() PipelineScanner           { return d.scanner }
func (d rebuildClientDeps) ReflectionForcer() ReflectionForcer         { return nil }
func (d rebuildClientDeps) SimilarityForcer() SimilarityForcer         { return nil }
