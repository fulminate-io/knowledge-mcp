// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/remote"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// recordingWrapSink is the test analog of the production admitting wrapper: it
// satisfies collector.Sink, records the graph type of every write, and then
// delegates verbatim to an inner sink. Every other sink double in this package
// is TERMINAL (noopSink discards, capturingSink appends, orderAssertingSink and
// recipeCaptureSink record) — delegation is what this one adds, because the
// e2e wire path must still reach the in-process ingest server behind it.
//
// bootstrap.admittingSink itself cannot be reused here: bootstrap imports
// tools, so importing it back would be an import cycle.
type recordingWrapSink struct {
	inner   collector.Sink
	written []string
}

func (s *recordingWrapSink) WriteResult(ctx context.Context, collectorName string, result *collectorwire.CollectResult) error {
	if result != nil {
		s.written = append(s.written, string(result.GraphType))
	}
	return s.inner.WriteResult(ctx, collectorName, result)
}

// TestRunLogsCollect_ThroughWrappedSink is the regression gate for the
// production defect: a logs collect must succeed when the injected sink is a
// WRAPPER around the ingest sink rather than the ingest sink itself, and the
// materialized log graph must ship THROUGH that wrapper rather than around it.
//
// It is also the sole catcher for the invariant "the admitting wrapper stays in
// front of every WriteResult" — the two pre-existing logs e2e tests inject the
// bare concrete sink, so a fix that reached the cloud fetch by name but kept
// writing through the raw uploader would leave them both green.
func TestRunLogsCollect_ThroughWrappedSink(t *testing.T) {
	provName := uniqueProviderName(t)
	logwire.Register(provName, func() logwire.Provider {
		return &stubLogsProvider{entries: e2eEntries(time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC))}
	})

	srv, captured, _ := startInProcessIngestServer(t, buildSubgraphResponse(t), nil)
	t.Cleanup(srv.close)

	// The two seams are deliberately DIFFERENT objects here, which is the
	// production arrangement: the fetcher is the inner uploader, the sink is
	// the wrapper around it.
	uploader := remote.NewUploadSink(srv.client)
	wrapped := &recordingWrapSink{inner: uploader}
	deps := &fakeDeps{sink: wrapped, fetcher: uploader}

	result := runLogsCollect(opCtx(), deps, collectArgs{
		Type:     "logs",
		Provider: provName,
	})
	require.False(t, result.IsError,
		"a logs collect must succeed when the sink is a wrapper around the ingest sink; content=%q", resultText(result))

	require.NotNil(t, captured.req, "expected CollectChunk+Finalize to be called")
	assert.Equal(t, string(kgtypes.GraphLogs), captured.req.GraphType)

	assert.Equal(t, []string{string(kgtypes.GraphLogs)}, wrapped.written,
		"the log graph must be written THROUGH the wrapping sink, not around it")

	edges := batchEdgesFromProtoForTest(captured.req.GetEdges())
	foundProxyEdge := false
	for _, e := range edges {
		if e.Type != kgtypes.EdgeEmittedBy {
			continue
		}
		if strings.HasPrefix(e.ToID, "proxy:cloud:acct-1:") {
			foundProxyEdge = true
			break
		}
	}
	assert.True(t, foundProxyEdge,
		"expected at least one EMITTED_BY edge into a proxy:cloud:acct-1:* node; got edges=%+v", edges)
}
