// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
)

// seedStubName is the unique collector name used by stubSeedCollector. Lives
// at file scope so the registration helper and the test can share it. The
// value must not collide with any real collector — collector.Register panics
// on duplicates.
const seedStubName = "cascade-seed-install-test"

// stubSeedCollector is a minimal collector.Collector that captures the ctx
// passed to Collect so the test can assert the CascadeSet pre-marking
// installed by InterceptCollect. Returns a zero-content CollectResult; the
// noop sink discards it.
type stubSeedCollector struct {
	mu          sync.Mutex
	capturedCtx context.Context
}

func (s *stubSeedCollector) Name() string { return seedStubName }

func (s *stubSeedCollector) Collect(ctx context.Context, _ string, _ collector.CollectOptions) (*collectorwire.CollectResult, error) {
	s.mu.Lock()
	s.capturedCtx = ctx
	s.mu.Unlock()
	return &collectorwire.CollectResult{
		GraphType: kgtypes.GraphCloud,
		GraphName: "smoke",
	}, nil
}

// captured returns the ctx threaded through the most recent Collect call.
func (s *stubSeedCollector) captured() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.capturedCtx
}

// seedStub is the singleton instance registered with the collector registry.
// Registered once via sync.Once-guarded init helper so a re-run inside the
// same `go test` invocation does not panic on duplicate registration.
var (
	seedStub     = &stubSeedCollector{}
	seedStubOnce sync.Once
)

func registerSeedStub() {
	seedStubOnce.Do(func() { collector.Register(seedStub) })
}

// noopSink is a minimal collector.Sink that drops every write. The cascade
// seed-install test does not exercise the wire path, so the WriteResult
// stub is never expected to error.
type noopSink struct{}

func (noopSink) WriteResult(context.Context, string, *collectorwire.CollectResult) error {
	return nil
}

// TestInterceptCollect_InstallsCascadeSets verifies the Phase 1 contract:
// InterceptCollect installs both cloud and cicd CascadeSets onto the ctx it
// hands to collector.Collect, and pre-marks each set with the seed (a.Type,
// a.ID) so a same-key cascade target is suppressed.
func TestInterceptCollect_InstallsCascadeSets(t *testing.T) {
	registerSeedStub()

	deps := &fakeDeps{sink: noopSink{}}

	// force=true skips checkGraphSafety. Other tests in this package init a
	// fresh per-test store; if our test happens to run after one of them
	// the safety check on result.GraphType=cloud / GraphName=smoke would
	// fire. Force makes the check unreachable regardless of ordering.
	args := json.RawMessage(`{"type":"` + seedStubName + `","id":"seed-id","force":true}`)
	handled, result := InterceptCollect(opCtx(), deps, kgtools.CallToolParams{
		Name:      "collect",
		Arguments: args,
	})
	require.True(t, handled, "InterceptCollect should handle the call")
	require.False(t, result.IsError, "InterceptCollect returned IsError; content=%q", resultText(result))

	ctx := seedStub.captured()
	require.NotNil(t, ctx, "expected stub Collect to capture a non-nil ctx")

	cloudCS := cloud.CascadeSetFrom(ctx)
	require.NotNil(t, cloudCS, "expected cloud.CascadeSet on captured ctx")
	assert.False(t, cloudCS.Mark(seedStubName, "seed-id"),
		"seed (type,id) should already be marked on cloud.CascadeSet")
	assert.True(t, cloudCS.Mark("new", "x"),
		"unseen key should be markable on cloud.CascadeSet")
}
