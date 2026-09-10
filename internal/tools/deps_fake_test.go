// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// deps_fake_test.go holds fakeDeps, the package's minimal ClientDeps double.
//
// IT LIVES IN A FILE NAMED FOR WHAT IT IS because it used to live in the log
// collector's end-to-end test, and that test was deleted with the log
// collectors: a ClientDeps double is not the property of whichever feature
// happened to introduce it, and filing it under one meant deleting that feature
// took an unrelated pipeline-control test's scaffolding with it. The same move
// the shared wire-arg types and the linker's fixtures made, for the same reason.

// fakeDeps satisfies ClientDeps with the two seams a test usually needs wired
// and everything else nil. A nil accessor is the DEGRADED-CLIENT shape the
// production nil-guards exist for, so a test that leaves one nil is exercising
// that guard rather than working around a missing fixture.
type fakeDeps struct {
	sink collector.Sink
	crud GraphTypeCRUDAPI // optional: registered-type dispatch tests inject a stub
	// pipelineNotReady flips PipelineReady() to false so a test can exercise the
	// bind-first wiring-window gate on the collect intercept. The zero value
	// keeps the pipeline ready.
	pipelineNotReady bool
}

func (d *fakeDeps) LocalLiveness() LocalLiveness    { return nil }
func (d *fakeDeps) Sink() collector.Sink            { return d.sink }
func (d *fakeDeps) RootDir() string                 { return "" }
func (d *fakeDeps) UsageAnalyzer() UsageAnalyzerAPI { return nil }

func (d *fakeDeps) PropReady() bool     { return true }
func (d *fakeDeps) PipelineReady() bool { return !d.pipelineNotReady }

func (d *fakeDeps) GraphTypeCRUD() GraphTypeCRUDAPI              { return d.crud }
func (d *fakeDeps) Embedder() embed.BinaryEmbedder               { return nil }
func (d *fakeDeps) BackendResolver() BackendResolver             { return nil }
func (d *fakeDeps) GraphCaller() GraphCaller                     { return nil }
func (d *fakeDeps) LocalGraphCaller() GraphCaller                { return nil }
func (d *fakeDeps) SegmentManager() SegmentSearcher              { return nil }
func (d *fakeDeps) SegmentVectorResolver() SegmentVectorResolver { return nil }
func (d *fakeDeps) SegmentShipper() SegmentShipper               { return nil }
func (d *fakeDeps) SegmentPruner() SegmentPruner                 { return nil }

func (d *fakeDeps) SegmentCacheDropper() SegmentCacheDropper { return nil }
func (d *fakeDeps) SegmentDeleter() SegmentDeleter           { return nil }
func (d *fakeDeps) SegmentCoverage() SegmentCoverageReader   { return nil }
func (d *fakeDeps) PipelineScanner() PipelineScanner         { return nil }

func (d *fakeDeps) ClearHealLatch(kgtypes.GraphType, string) {}
func (d *fakeDeps) ReflectionForcer() ReflectionForcer       { return nil }
func (d *fakeDeps) SimilarityForcer() SimilarityForcer       { return nil }

func (d *fakeDeps) BlindSpotProvider() BlindSpotProvider { return nil }
func (d *fakeDeps) ClusterProvider() ClusterProvider     { return nil }
func (d *fakeDeps) TensionsProvider() TensionsProvider   { return nil }

// graphCallerDeps is a ClientDeps exposing ONE seam, the GraphCaller, with every
// other accessor nil. It is the double for an intercept whose whole surface is a
// graph read: the criterion, rule and parity tests all drive one.
//
// IT TOO IS RELOCATED, and renamed to say what it is. It was written in the log
// intercepts' end-to-end test under a log-shaped name and used by eleven files
// that had nothing to do with logs, which is the same filing mistake the wire
// args and the linker fixtures made, three times over in one package.
type graphCallerDeps struct {
	gc GraphCaller
}

func (d *graphCallerDeps) LocalLiveness() LocalLiveness    { return nil }
func (d *graphCallerDeps) Sink() collector.Sink            { return nil }
func (d *graphCallerDeps) RootDir() string                 { return "" }
func (d *graphCallerDeps) UsageAnalyzer() UsageAnalyzerAPI { return nil }

func (d *graphCallerDeps) PropReady() bool     { return true }
func (d *graphCallerDeps) PipelineReady() bool { return true }

func (d *graphCallerDeps) GraphTypeCRUD() GraphTypeCRUDAPI              { return nil }
func (d *graphCallerDeps) Embedder() embed.BinaryEmbedder               { return nil }
func (d *graphCallerDeps) BackendResolver() BackendResolver             { return nil }
func (d *graphCallerDeps) GraphCaller() GraphCaller                     { return d.gc }
func (d *graphCallerDeps) LocalGraphCaller() GraphCaller                { return d.gc }
func (d *graphCallerDeps) SegmentManager() SegmentSearcher              { return nil }
func (d *graphCallerDeps) SegmentVectorResolver() SegmentVectorResolver { return nil }
func (d *graphCallerDeps) SegmentShipper() SegmentShipper               { return nil }
func (d *graphCallerDeps) SegmentPruner() SegmentPruner                 { return nil }

func (d *graphCallerDeps) SegmentCacheDropper() SegmentCacheDropper { return nil }
func (d *graphCallerDeps) SegmentDeleter() SegmentDeleter           { return nil }
func (d *graphCallerDeps) SegmentCoverage() SegmentCoverageReader   { return nil }
func (d *graphCallerDeps) PipelineScanner() PipelineScanner         { return nil }

func (d *graphCallerDeps) ClearHealLatch(kgtypes.GraphType, string) {}
func (d *graphCallerDeps) ReflectionForcer() ReflectionForcer       { return nil }
func (d *graphCallerDeps) SimilarityForcer() SimilarityForcer       { return nil }

func (d *graphCallerDeps) BlindSpotProvider() BlindSpotProvider { return nil }
func (d *graphCallerDeps) ClusterProvider() ClusterProvider     { return nil }
func (d *graphCallerDeps) TensionsProvider() TensionsProvider   { return nil }

// noopSink is a minimal collector.Sink that drops every write, for the tests
// whose subject is the dispatch rather than the wire.
//
// IT IS THE FOURTH FIXTURE RELOCATED OUT OF A DELETED FEATURE'S TEST FILE. It
// lived in the cloud cascade test, which went with the cloud collectors, and is
// used by six files that never touched a cascade.
type noopSink struct{}

func (noopSink) WriteResult(context.Context, string, *collectorwire.CollectResult) error {
	return nil
}
