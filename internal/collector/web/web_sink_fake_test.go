// SPDX-License-Identifier: Apache-2.0

package web

import (
	"context"
	"sync"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
)

// webCapturingSink is a collector.Sink that records the *collectorwire.CollectResult
// it receives instead of persisting it. The web collector tests assert over the
// captured batch (node-type counts, specific node IDs, edges) — the same value
// data a store readback inspected — without standing up a real store engine.
type webCapturingSink struct {
	mu      sync.Mutex
	results []*collectorwire.CollectResult
}

func (s *webCapturingSink) WriteResult(_ context.Context, _ string, result *collectorwire.CollectResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.results = append(s.results, result)
	return nil
}

// last returns the most recently captured CollectResult, or nil when none was
// written.
func (s *webCapturingSink) last() *collectorwire.CollectResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.results) == 0 {
		return nil
	}
	return s.results[len(s.results)-1]
}

// initWebTestSink installs a capturing sink as the collector's DefaultSinkFactory
// for the duration of the test, restoring the previous factory on cleanup. No
// store.Init, no func init, no TestMain factory mutation — the save/restore in
// t.Cleanup mirrors collector/pipeline_test.go's initTestStore pattern. Returns
// the sink so the test can read the captured batch.
func initWebTestSink(t *testing.T) *webCapturingSink {
	t.Helper()
	sink := &webCapturingSink{}
	prev := collector.DefaultSinkFactory
	collector.DefaultSinkFactory = func() collector.Sink { return sink }
	t.Cleanup(func() { collector.DefaultSinkFactory = prev })
	return sink
}

// countCapturedNodeTypes returns a map of nodeType-string → count over a
// captured node slice — the store-free replacement for the old countNodeTypes
// helper that walked a store.DB.
func countCapturedNodeTypes(nodes []*knowledgev1.Node) map[string]int {
	counts := make(map[string]int)
	for _, n := range nodes {
		counts[n.Type]++
	}
	return counts
}
