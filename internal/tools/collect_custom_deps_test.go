// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"sync"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// collect_custom_deps_test.go — the ClientDeps DOUBLE every custom-collect test
// in this package is driven through, split out of collect_custom_test.go when
// the read seams the declared-context fill pulls through were added to it.
//
// IT IS A FULL ClientDeps and most of it is nil, which is the point: the
// dispatch takes the whole interface and each test wires exactly the seams its
// row needs, so a row that reached a seam it did not wire fails on a nil rather
// than on a convenient default.

// customDeps is a ClientDeps that records the pipeline wake, which is the tail
// signal R10 is about. It also carries the capturing sink and the registration
// stub the dispatch resolves through.
type customDeps struct {
	sink *capturingSink
	crud GraphTypeCRUDAPI
	// rt is the standing collect runtime the dispatch reaches through the
	// optional collectRuntimeProvider seam. NIL for every test that does not care
	// about the wait-or-detach runtime, which is the degraded-client shape
	// collectWaitOrDetach already handles by running synchronously.
	rt *CollectRuntime
	// scope is the temp-directory config file this deps' families are registered
	// in. A test rewrites it to drive a hand edit.
	scope *collectorScope
	// graphs is the READ SEAM the declared-context fill pulls through. It stays
	// NIL by default, which is the shape every test that predates that contract
	// runs in and is what the fill's own refusal rows exercise; a test that needs
	// a filled block wires one.
	graphs GraphCaller

	mu    sync.Mutex
	wakes int
}

// CollectRuntime satisfies the dispatch's optional collectRuntimeProvider seam.
func (d *customDeps) CollectRuntime() *CollectRuntime { return d.rt }

func (d *customDeps) WakePipeline() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.wakes++
}

func (d *customDeps) wakeCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.wakes
}

func (d *customDeps) LocalLiveness() LocalLiveness    { return nil }
func (d *customDeps) Sink() collector.Sink            { return d.sink }
func (d *customDeps) RootDir() string                 { return "" }
func (d *customDeps) UsageAnalyzer() UsageAnalyzerAPI { return nil }

func (d *customDeps) PropReady() bool     { return true }
func (d *customDeps) PipelineReady() bool { return true }

func (d *customDeps) GraphTypeCRUD() GraphTypeCRUDAPI              { return d.crud }
func (d *customDeps) Embedder() embed.BinaryEmbedder               { return nil }
func (d *customDeps) BackendResolver() BackendResolver             { return nil }
func (d *customDeps) GraphCaller() GraphCaller                     { return d.graphs }
func (d *customDeps) LocalGraphCaller() GraphCaller                { return nil }
func (d *customDeps) SegmentManager() SegmentSearcher              { return nil }
func (d *customDeps) SegmentVectorResolver() SegmentVectorResolver { return nil }
func (d *customDeps) SegmentShipper() SegmentShipper               { return nil }
func (d *customDeps) SegmentPruner() SegmentPruner                 { return nil }

func (d *customDeps) SegmentCacheDropper() SegmentCacheDropper { return nil }
func (d *customDeps) SegmentDeleter() SegmentDeleter           { return nil }
func (d *customDeps) SegmentCoverage() SegmentCoverageReader   { return nil }
func (d *customDeps) PipelineScanner() PipelineScanner         { return nil }

func (d *customDeps) ClearHealLatch(kgtypes.GraphType, string) {}
func (d *customDeps) ReflectionForcer() ReflectionForcer       { return nil }
func (d *customDeps) SimilarityForcer() SimilarityForcer       { return nil }

func (d *customDeps) BlindSpotProvider() BlindSpotProvider { return nil }
func (d *customDeps) ClusterProvider() ClusterProvider     { return nil }
func (d *customDeps) TensionsProvider() TensionsProvider   { return nil }

// newCustomDeps writes the given entries into a temp-directory user scope and
// wires a recording deps over it. The CRUD stub is wired too, holding NO catalog
// records: it is what the loader upserts each family's behavior through, and
// what the legacy check reads on a miss.
func newCustomDeps(t *testing.T, defs ...namedEntry) *customDeps {
	t.Helper()
	kept := make([]namedEntry, 0, len(defs))
	for _, d := range defs {
		if d.name != "" {
			kept = append(kept, d)
		}
	}
	scope := useTempCollectorScope(t, kept...)
	return &customDeps{sink: &capturingSink{}, crud: &stubGraphTypeCRUD{}, scope: scope}
}
