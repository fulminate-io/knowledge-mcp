// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// manage_status_coverage_deps_test.go — the ClientDeps stub the coverage tests
// assemble rows through.
//
// SPLIT OUT FOR THE FILE-LENGTH CAP, which the sibling crossed when the
// branch-inventory rows and the consumer-position columns landed in it from two
// directions at once. The seam is the one with no judgement on either side: this
// file is the interface surface ClientDeps demands and nothing else, so the
// sibling keeps every fake that a test PROGRAMS and every assertion that reads one.

// coverageDeps is the minimal ClientDeps whose GraphCaller is the coverageFake and
// whose SegmentCoverage seam is an optional coverageSegReader stub (nil when the
// test does not exercise the segment column).
type coverageDeps struct {
	gc     GraphCaller
	segCov SegmentCoverageReader
}

func (d *coverageDeps) LocalLiveness() LocalLiveness    { return nil }
func (d *coverageDeps) Sink() collector.Sink            { return nil }
func (d *coverageDeps) RootDir() string                 { return "" }
func (d *coverageDeps) UsageAnalyzer() UsageAnalyzerAPI { return nil }

func (d *coverageDeps) PropReady() bool     { return true }
func (d *coverageDeps) PipelineReady() bool { return true }

func (d *coverageDeps) GraphTypeCRUD() GraphTypeCRUDAPI              { return nil }
func (d *coverageDeps) Embedder() embed.BinaryEmbedder               { return nil }
func (d *coverageDeps) BackendResolver() BackendResolver             { return nil }
func (d *coverageDeps) GraphCaller() GraphCaller                     { return d.gc }
func (d *coverageDeps) LocalGraphCaller() GraphCaller                { return d.gc }
func (d *coverageDeps) SegmentManager() SegmentSearcher              { return nil }
func (d *coverageDeps) SegmentVectorResolver() SegmentVectorResolver { return nil }
func (d *coverageDeps) SegmentShipper() SegmentShipper               { return nil }
func (d *coverageDeps) SegmentPruner() SegmentPruner                 { return nil }

func (d *coverageDeps) SegmentCacheDropper() SegmentCacheDropper { return nil }
func (d *coverageDeps) SegmentDeleter() SegmentDeleter           { return nil }
func (d *coverageDeps) SegmentCoverage() SegmentCoverageReader   { return d.segCov }
func (d *coverageDeps) PipelineScanner() PipelineScanner         { return nil }

func (d *coverageDeps) ClearHealLatch(kgtypes.GraphType, string) {}
func (d *coverageDeps) ReflectionForcer() ReflectionForcer       { return nil }
func (d *coverageDeps) SimilarityForcer() SimilarityForcer       { return nil }

func (d *coverageDeps) BlindSpotProvider() BlindSpotProvider { return nil }
func (d *coverageDeps) ClusterProvider() ClusterProvider     { return nil }
func (d *coverageDeps) TensionsProvider() TensionsProvider   { return nil }
