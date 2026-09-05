// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// TestRebuildSegments_PagesBucketsShipsOnce is the happy-path T1 proof: the cursor
// advances per page and terminates on the empty page; each hash bucket fires one
// StageRebuildPartition carrying both formats (ZERO per-bucket ship); exactly ONE
// FinalizeRebuild fires AFTER every partition is staged and returns the fake's pruned
// ids; InvalidateLocal fires once with exactly the HNSW half of them.
func TestRebuildSegments_PagesBucketsShipsOnce(t *testing.T) {
	min := searchengine.DefaultMinSegmentDocs
	// Two full pages of `min` items each → 2048 documents, which the bucket count
	// derivation splits into 2 buckets.
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

	// Build-concurrent / ship-once: 2 buckets → 2 StageRebuildPartition calls, each
	// carrying both formats' share, ZERO per-bucket ship (the fake has no ship method —
	// staging-only by construction), exactly ONE FinalizeRebuild.
	require.Equal(t, int64(2), shipper.stageCalls.Load(), "one StageRebuildPartition per bucket")
	require.Equal(t, int64(1), shipper.finalizeCalls.Load(), "exactly ONE FinalizeRebuild — ship happens once, never per-bucket")
	require.Equal(t, int64(2), shipper.stagesAtFinalize, "FinalizeRebuild ran AFTER every bucket was staged")

	// InvalidateLocal fired once with exactly the HNSW ids the finalize retired.
	require.Len(t, shipper.invalidate, 1)
	require.Equal(t, pruned, shipper.invalidate[0], "InvalidateLocal evicts exactly the finalize-returned HNSW superseded ids")

	// Every scanned doc reached the engine.
	require.Equal(t, 2*min, shipper.hnswDocTotal)
	require.Equal(t, 2*min, shipper.bm25DocTotal)

	// Report: the emitted bucket count, which for this corpus is 2.
	body := res.Content[0].Text
	require.Contains(t, body, "2 hash buckets built + shipped", "the emitted bucket count is reported")
}

// TestRebuildSegments_SmallCorpusShipsOneBucket proves a corpus far smaller than
// the seal threshold still ships: it derives a single bucket, that bucket is Added
// and SEALED into its own segment, and the report counts it as built. Under the
// old positional chunking such a corpus produced zero full chunks and had to be
// described as a sealed remainder; a bucket is a bucket regardless of size, so the
// small case is no longer special.
func TestRebuildSegments_SmallCorpusShipsOneBucket(t *testing.T) {
	const n = 5 // far below DefaultMinSegmentDocs (1024)
	require.Less(t, n, searchengine.DefaultMinSegmentDocs, "fixture must be below the seal threshold")
	page := makeScanPage("a", 0, n)
	scanner := &fakeRebuildScanner{pages: [][]*knowledgev1.PipelineScanItem{page}}
	shipper := &fakeRebuildShipper{}

	deps := rebuildClientDeps{scanner: scanner, shipper: shipper}
	res := handleClientRebuildSegments(context.Background(), deps, manageArgs{
		Operation: "rebuild_segments", Graph: "code", Name: "myrepo",
	})
	require.False(t, res.IsError, "a small-corpus rebuild is a successful ship, not an error: %v", res.Content)

	// One bucket: staged, then built into its own segment and shipped by the finalize.
	require.Equal(t, int64(1), shipper.stageCalls.Load(), "exactly one bucket staged")
	require.Equal(t, int64(1), shipper.finalizeCalls.Load(), "the single ship still happens once")
	require.Equal(t, n, shipper.hnswDocTotal, "every vector reaches the engine")

	// The report counts the bucket, so a small rebuild reads as a successful ship
	// rather than as nothing built.
	body := res.Content[0].Text
	require.Contains(t, body, "1 hash buckets built + shipped",
		"a small corpus still emits one counted bucket")
}

// TestRebuildSegments_SubThresholdReRunNoOp proves the determinism/idempotency
// property holds for a small corpus: a second rebuild over the same fixture whose
// FinalizeRebuild returns an empty pruned set still renders a successful
// (no-op) ship.
func TestRebuildSegments_SubThresholdReRunNoOp(t *testing.T) {
	const n = 5
	page := makeScanPage("a", 0, n)
	// A re-run over an unchanged node set ships byte-identical segments → the
	// content-hash diff is empty → FinalizeRebuild prunes nothing.
	scanner := &fakeRebuildScanner{pages: [][]*knowledgev1.PipelineScanItem{page}}
	shipper := &fakeRebuildShipper{pruned: nil}

	deps := rebuildClientDeps{scanner: scanner, shipper: shipper}
	res := handleClientRebuildSegments(context.Background(), deps, manageArgs{Graph: "code", Name: "myrepo"})
	require.False(t, res.IsError, "the re-run no-op still renders success: %v", res.Content)

	require.Equal(t, int64(1), shipper.finalizeCalls.Load(), "the single ship still happens once on the re-run")
	require.Empty(t, shipper.invalidate[0], "an empty pruned set means nothing superseded (content-hash no-op)")
	body := res.Content[0].Text
	require.Contains(t, body, "1 hash buckets built + shipped")
	require.Contains(t, body, "content-hash no-op", "the report still describes the idempotent re-run")
}

// TestRebuildSegments_EveryDocReachesABucket proves nothing is dropped when the
// corpus does not divide evenly: min+5 items derive 2 buckets, both are staged, and
// every scanned vector reaches the engine. Under positional chunking this fixture was
// one full chunk plus a 5-item remainder; bucketing has no remainder, so the assertion
// is now about total coverage rather than a tail.
func TestRebuildSegments_EveryDocReachesABucket(t *testing.T) {
	min := searchengine.DefaultMinSegmentDocs
	page := makeScanPage("a", 0, min+5)
	scanner := &fakeRebuildScanner{pages: [][]*knowledgev1.PipelineScanItem{page}}
	shipper := &fakeRebuildShipper{}

	deps := rebuildClientDeps{scanner: scanner, shipper: shipper}
	res := handleClientRebuildSegments(context.Background(), deps, manageArgs{Graph: "code", Name: "myrepo"})
	require.False(t, res.IsError)

	// 2 buckets → 2 staged partitions; all docs reach the engine.
	require.Equal(t, int64(2), shipper.stageCalls.Load(), "one staged partition per bucket")
	require.Equal(t, int64(1), shipper.finalizeCalls.Load())
	require.Equal(t, min+5, shipper.hnswDocTotal, "every scanned vector reaches the engine")
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
	require.Equal(t, int64(0), shipper.stageCalls.Load(), "must not dispatch the rebuild when not ready")
}

// TestRebuildSegments_StageErrorAbortsBeforeFinalize is the T3 fail-closed proof: a
// staging error aborts the rebuild BEFORE FinalizeRebuild — ZERO FinalizeRebuild and
// ZERO InvalidateLocal, so no partial set is shipped.
func TestRebuildSegments_StageErrorAbortsBeforeFinalize(t *testing.T) {
	min := searchengine.DefaultMinSegmentDocs
	page := makeScanPage("a", 0, min)
	scanner := &fakeRebuildScanner{pages: [][]*knowledgev1.PipelineScanItem{page}}
	shipper := &fakeRebuildShipper{stageErr: errors.New("staging boom")}

	deps := rebuildClientDeps{scanner: scanner, shipper: shipper}
	res := handleClientRebuildSegments(context.Background(), deps, manageArgs{Graph: "code", Name: "myrepo"})

	require.True(t, res.IsError, "a staging error must surface as a tool error")
	require.Equal(t, int64(0), shipper.finalizeCalls.Load(), "ABORT before the finalize — ZERO FinalizeRebuild on a staging error")
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

	t.Run("embeddable builtin (practice) is accepted, gt reaches the scanner", func(t *testing.T) {
		min := searchengine.DefaultMinSegmentDocs
		scanner := &fakeRebuildScanner{pages: [][]*knowledgev1.PipelineScanItem{makeScanPage("p", 0, min)}}
		shipper := &fakeRebuildShipper{}
		deps := rebuildClientDeps{scanner: scanner, shipper: shipper, crud: crud}

		// practice is an embeddable builtin — it carries rebuildable segments
		// (HasRebuildableSegments == true), so the gate accepts it and the threaded
		// GraphType reaches the scanner.
		res := handleClientRebuildSegments(context.Background(), deps, manageArgs{Graph: string(kgtypes.GraphPractice), Name: "go"})
		require.False(t, res.IsError, "an embeddable builtin (practice) must be accepted: %v", res.Content)
		require.NotEmpty(t, scanner.graphTypes)
		for _, gt := range scanner.graphTypes {
			require.Equal(t, string(kgtypes.GraphPractice), gt,
				"the threaded GraphType must reach the PipelineScan request (practice)")
		}
	})

	t.Run("non-embeddable builtin is rejected", func(t *testing.T) {
		deps := rebuildClientDeps{scanner: &fakeRebuildScanner{}, shipper: &fakeRebuildShipper{}, crud: crud}
		// linkage is a builtin but NOT embeddable — it holds proxy edges and no
		// text, so it carries no rebuildable segments and the gate rejects it
		// (HasRebuildableSegments == false).
		res := handleClientRebuildSegments(context.Background(), deps, manageArgs{Graph: string(kgtypes.GraphLinkage), Name: "default"})
		require.True(t, res.IsError, "a non-embeddable builtin graph has no rebuildable segments and must be rejected")
		require.Contains(t, res.Content[0].Text, "no rebuildable segments",
			"the rejection names the no-rebuildable-segments boundary")
	})

	t.Run("unregistered custom typo is rejected", func(t *testing.T) {
		deps := rebuildClientDeps{scanner: &fakeRebuildScanner{}, shipper: &fakeRebuildShipper{}, crud: crud}
		res := handleClientRebuildSegments(context.Background(), deps, manageArgs{Graph: "hellogarph", Name: "demo"})
		require.True(t, res.IsError, "an unregistered custom graph type must be rejected")
	})
}

// blockingScanner blocks on the first PipelineScan until released, signaling
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

func (rebuildClientDeps) LocalLiveness() LocalLiveness          { return nil }
func (rebuildClientDeps) Sink() collector.Sink                  { return nil }
func (rebuildClientDeps) SubgraphFetcher() CloudSubgraphFetcher { return nil }
func (rebuildClientDeps) RootDir() string                       { return "" }
func (rebuildClientDeps) UsageAnalyzer() UsageAnalyzerAPI       { return nil }

func (rebuildClientDeps) PropReady() bool       { return true }
func (d rebuildClientDeps) PipelineReady() bool { return !d.pipelineNotReady }

func (d rebuildClientDeps) GraphTypeCRUD() GraphTypeCRUDAPI            { return d.crud }
func (rebuildClientDeps) Embedder() embed.BinaryEmbedder               { return nil }
func (rebuildClientDeps) BackendResolver() BackendResolver             { return nil }
func (rebuildClientDeps) GraphCaller() GraphCaller                     { return nil }
func (rebuildClientDeps) LocalGraphCaller() GraphCaller                { return nil }
func (rebuildClientDeps) SegmentManager() SegmentSearcher              { return nil }
func (rebuildClientDeps) SegmentVectorResolver() SegmentVectorResolver { return nil }
func (d rebuildClientDeps) SegmentShipper() SegmentShipper             { return d.shipper }
func (rebuildClientDeps) SegmentPruner() SegmentPruner                 { return nil }

func (rebuildClientDeps) SegmentCacheDropper() SegmentCacheDropper { return nil }
func (rebuildClientDeps) SegmentDeleter() SegmentDeleter           { return nil }
func (rebuildClientDeps) SegmentCoverage() SegmentCoverageReader   { return nil }
func (d rebuildClientDeps) PipelineScanner() PipelineScanner       { return d.scanner }

func (d rebuildClientDeps) ClearHealLatch(kgtypes.GraphType, string) {}
func (d rebuildClientDeps) ReflectionForcer() ReflectionForcer       { return nil }
func (d rebuildClientDeps) SimilarityForcer() SimilarityForcer       { return nil }

func (d rebuildClientDeps) BlindSpotProvider() BlindSpotProvider { return nil }
func (d rebuildClientDeps) ClusterProvider() ClusterProvider     { return nil }
func (d rebuildClientDeps) TensionsProvider() TensionsProvider   { return nil }
