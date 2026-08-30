// SPDX-License-Identifier: Apache-2.0

package tools

// intercept_deps_stub_test.go holds interceptDeps — the ClientDeps stub the
// intercept tests across this package share.
//
// SPLIT FROM intercept_search_query_dispatch_test.go FOR THE LINE BUDGET, the
// same reason the registry and coverage files are split. The lefthook file-length
// gate globs *.go with only vendor/** and gen/** excluded, so a test file hits the
// identical 500-line commit block a source file does, and the stub is the largest
// self-contained block in that file: one type plus its interface satisfaction,
// used by every intercept test in the package rather than by that file alone.

import (
	"context"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// interceptDeps satisfies ClientDeps for the intercept reroute tests: a real
// GraphClient (pointed at the fake server) + an optional embedder.
type interceptDeps struct {
	gc     *graphclient.GraphClient
	emb    embed.BinaryEmbedder
	segMgr SegmentSearcher
	segRes SegmentVectorResolver
	// pipelineNotReady flips PipelineReady() to false so a test can exercise the
	// bind-first wiring-window gate (bind-first startup) on the segment-engine search arms.
	// Zero value keeps the pipeline ready, so every pre-existing test exercises
	// the wired path.
	pipelineNotReady bool
	// segCoverage backs SegmentCoverage(). Its ZERO VALUE is nil, which is what
	// every pre-existing fixture relied on when the method returned a hardcoded
	// nil; the practice segment-gap check reads it, and a nil seam is the branch
	// that answers "this zero could not be qualified" rather than a clean zero.
	segCoverage SegmentCoverageReader

	// poolEvicted backs PoolEvicted, and loadLiveResidentErr backs the error half of
	// LoadLiveResidentDocCount. Both zero values describe the ordinary graph — a
	// resident pool whose load succeeds — so every pre-existing fixture keeps the
	// behaviour it was written against.
	//
	// THESE TWO SEAMS ARE REACHED BY TYPE ASSERTION, NOT BY A ClientDeps METHOD, and
	// that is why they are wired here rather than left to the fixtures that need them.
	// practiceSegmentGapNotice reads its LOCAL operand through loadLiveResidentFor,
	// which asserts deps to loadLiveResidentReader and answers "seam is unwired" when
	// the assertion fails. A stub that simply omitted these methods would COMPILE
	// CLEAN and route every gap fixture in this package into the truthful-inability
	// caveat — a silent rewrite of what those tests assert, with no diagnostic
	// anywhere. An interface method would have been caught by the compiler; an
	// optional capability is caught by nothing, so it is stated explicitly.
	poolEvicted         bool
	loadLiveResidentErr error
	// gtCRUD backs GraphTypeCRUD(). Its ZERO VALUE is nil — the graph-type
	// registry is unreachable — which the custom-graph selector validation
	// reports as a loud "registry unavailable" rather than treating as "nothing
	// is registered". A fixture that means "this custom type IS registered"
	// therefore has to say so by wiring a fake here.
	gtCRUD GraphTypeCRUDAPI
	// loadCalls counts LoadLiveResidentDocCount. It exists so a test can prove the
	// EVICTION FENCE RAN FIRST: the load models materialization, so a fence placed
	// after the local read would show up here as a call plus a pool that is no
	// longer evicted.
	loadCalls int
}

func (d *interceptDeps) LocalLiveness() LocalLiveness    { return d.gc }
func (d *interceptDeps) Sink() collector.Sink            { return nil }
func (d *interceptDeps) RootDir() string                 { return "" }
func (d *interceptDeps) UsageAnalyzer() UsageAnalyzerAPI { return nil }

func (d *interceptDeps) PropReady() bool     { return true }
func (d *interceptDeps) PipelineReady() bool { return !d.pipelineNotReady }

func (d *interceptDeps) GraphTypeCRUD() GraphTypeCRUDAPI              { return d.gtCRUD }
func (d *interceptDeps) Embedder() embed.BinaryEmbedder               { return d.emb }
func (d *interceptDeps) BackendResolver() BackendResolver             { return nil }
func (d *interceptDeps) GraphCaller() GraphCaller                     { return d.gc }
func (d *interceptDeps) LocalGraphCaller() GraphCaller                { return d.gc }
func (d *interceptDeps) SegmentManager() SegmentSearcher              { return d.segMgr }
func (d *interceptDeps) SegmentVectorResolver() SegmentVectorResolver { return d.segRes }
func (d *interceptDeps) SegmentShipper() SegmentShipper               { return nil }
func (d *interceptDeps) SegmentPruner() SegmentPruner                 { return nil }

func (d *interceptDeps) SegmentCacheDropper() SegmentCacheDropper { return nil }
func (d *interceptDeps) SegmentDeleter() SegmentDeleter           { return nil }
func (d *interceptDeps) SegmentCoverage() SegmentCoverageReader   { return d.segCoverage }

// PoolEvicted answers the residency fence.
func (d *interceptDeps) PoolEvicted(kgtypes.GraphType, string) bool { return d.poolEvicted }

// LoadLiveResidentDocCount is the LOADING decider's stub half.
//
// IT MODELS THE LOAD'S SIDE EFFECT, which is the whole reason a test can assert the
// fence ordering: a real load MATERIALIZES an evicted pool, so this clears
// poolEvicted. A test asserting the pool is still evicted afterwards is therefore
// asserting that this never ran — which is exactly the difference between a fence
// placed before the local read and one placed after it.
//
// THE COUNT DELEGATES TO segCoverage rather than carrying a knob of its own, so the
// fixtures that program one coverage number keep meaning what they meant when the
// local operand was ShippedSegmentDocCount. A nil seam answers zero without
// panicking; callers that care about an unwired seam test the nil coverage reader.
func (d *interceptDeps) LoadLiveResidentDocCount(
	_ context.Context, gt kgtypes.GraphType, name string,
) (int, error) {
	d.loadCalls++
	d.poolEvicted = false
	if d.loadLiveResidentErr != nil {
		return 0, d.loadLiveResidentErr
	}
	if d.segCoverage == nil {
		return 0, nil
	}
	return d.segCoverage.LiveResidentDocCount(gt, name), nil
}
func (d *interceptDeps) PipelineScanner() PipelineScanner { return nil }

func (d *interceptDeps) ClearHealLatch(kgtypes.GraphType, string) {}
func (d *interceptDeps) ReflectionForcer() ReflectionForcer       { return nil }
func (d *interceptDeps) SimilarityForcer() SimilarityForcer       { return nil }

func (d *interceptDeps) BlindSpotProvider() BlindSpotProvider { return nil }
func (d *interceptDeps) ClusterProvider() ClusterProvider     { return nil }
func (d *interceptDeps) TensionsProvider() TensionsProvider   { return nil }
