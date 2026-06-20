// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/remote"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"
	"github.com/fulminate-io/knowledge-mcp/internal/hivemonitor"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// fakeDeps satisfies the ClientDeps interface for runLogsCollect. We
// only need Sink — the inline-Provider path means GraphClient is never
// called.
type fakeDeps struct {
	sink collector.Sink
	crud GraphTypeCRUDAPI // optional: registered-type dispatch tests inject a stub
	// pipelineNotReady flips PipelineReady() to false so a test can exercise the
	// bind-first wiring-window gate (bind-first startup) on the collect intercept. Zero value
	// keeps the pipeline ready.
	pipelineNotReady bool
}

func (d *fakeDeps) LocalLiveness() LocalLiveness                 { return nil }
func (d *fakeDeps) Sink() collector.Sink                         { return d.sink }
func (d *fakeDeps) RootDir() string                              { return "" }
func (d *fakeDeps) WorkerRuntime() WorkerRuntimeAPI              { return nil }
func (d *fakeDeps) WorkerReady() bool                            { return true }
func (d *fakeDeps) PropReady() bool                              { return true }
func (d *fakeDeps) PipelineReady() bool                          { return !d.pipelineNotReady }
func (d *fakeDeps) ClaimRegistry() *hivemonitor.Registry         { return nil }
func (d *fakeDeps) BanSet() *hivemonitor.BanSet                  { return nil }
func (d *fakeDeps) WorkerCRUD() WorkerCRUDAPI                    { return nil }
func (d *fakeDeps) GraphTypeCRUD() GraphTypeCRUDAPI              { return d.crud }
func (d *fakeDeps) Embedder() embed.BinaryEmbedder               { return nil }
func (d *fakeDeps) BackendResolver() BackendResolver             { return nil }
func (d *fakeDeps) GraphCaller() GraphCaller                     { return nil }
func (d *fakeDeps) LocalGraphCaller() GraphCaller                { return nil }
func (d *fakeDeps) RepoResolver() *RepoResolver                  { return nil }
func (d *fakeDeps) SegmentManager() SegmentSearcher              { return nil }
func (d *fakeDeps) SegmentVectorResolver() SegmentVectorResolver { return nil }
func (d *fakeDeps) SegmentShipper() SegmentShipper               { return nil }
func (d *fakeDeps) SegmentPruner() SegmentPruner                 { return nil }
func (d *fakeDeps) SegmentCoverage() SegmentCoverageReader       { return nil }
func (d *fakeDeps) PipelineScanner() PipelineScanner             { return nil }
func (d *fakeDeps) ReflectionForcer() ReflectionForcer           { return nil }
func (d *fakeDeps) SimilarityForcer() SimilarityForcer           { return nil }

func (d *fakeDeps) BlindSpotProvider() BlindSpotProvider { return nil }
func (d *fakeDeps) ClusterProvider() ClusterProvider     { return nil }
func (d *fakeDeps) TensionsProvider() TensionsProvider   { return nil }

// stubLogsProvider returns a fixed entry batch plus zero sources. Each
// test registers it under a unique name to avoid colliding with other
// tests' provider registrations (logwire.Register panics on duplicates).
type stubLogsProvider struct {
	entries []logwire.LogEntry
}

func (s *stubLogsProvider) Name() string                      { return "stub" }
func (s *stubLogsProvider) Configure(map[string]string) error { return nil }
func (s *stubLogsProvider) Collect(_ context.Context, _ logwire.Query, emit func([]logwire.LogEntry) error) error {
	if len(s.entries) == 0 {
		return nil
	}
	return emit(s.entries)
}
func (s *stubLogsProvider) ListSources(context.Context, string) ([]logwire.Source, error) {
	return nil, nil
}

// e2eEntries produces a synthetic batch with overlapping ERROR templates
// across two services ("api" and "db") so the pipeline produces:
//   - templates that cluster (≥2 distinct error templates)
//   - streams keyed on service=api and service=db
//   - a temporal correlation candidate between the two services
func e2eEntries(base time.Time) []logwire.LogEntry {
	var entries []logwire.LogEntry
	for i := range 30 {
		entries = append(entries, logwire.LogEntry{
			Timestamp: base.Add(time.Duration(i) * time.Second),
			Severity:  logwire.SeverityError,
			Message:   fmt.Sprintf("connection refused host-%d", i),
			Labels:    map[string]string{logwire.FieldService: "api", "pod": fmt.Sprintf("api-%d", i%3)},
		})
	}
	for i := range 20 {
		entries = append(entries, logwire.LogEntry{
			Timestamp: base.Add(time.Duration(5+i) * time.Second),
			Severity:  logwire.SeverityError,
			Message:   fmt.Sprintf("pool exhausted client-%d", i),
			Labels:    map[string]string{logwire.FieldService: "db", "pod": fmt.Sprintf("db-%d", i%2)},
		})
	}
	return entries
}

// buildSubgraphResponse hand-builds a *FetchCloudSubgraphResponse with
// one slice for "acct-1" containing two service-like NodeCloudResources
// (api/ecs:service, db/Deployment) plus a CONNECTS_TO edge api → db so
// the dep-checker reports HasDependency=true for the api ↔ db pair.
func buildSubgraphResponse(t *testing.T) *knowledgev1.FetchCloudSubgraphResponse {
	t.Helper()
	apiNode := knowledgev1.Node{
		Id:         "arn:aws:ecs:us-east-1:1:service/api",
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: "api",
		Source:     "cloud",
	}
	kgtypes.SetValue(&apiNode, "resource_type", "ecs:service")
	dbNode := knowledgev1.Node{
		Id:         "k8s:default/Deployment/db",
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: "db",
		Source:     "cloud",
	}
	kgtypes.SetValue(&dbNode, "resource_type", "Deployment")

	nodesJSON, err := json.Marshal([]*knowledgev1.Node{&apiNode, &dbNode})
	require.NoError(t, err)

	return &knowledgev1.FetchCloudSubgraphResponse{
		Slices: []*knowledgev1.CloudSubgraphSlice{{
			GraphName: "acct-1",
			NodesJson: nodesJSON,
			Edges: []*knowledgev1.Edge{{
				FromId: apiNode.Id,
				ToId:   dbNode.Id,
				Type:   string(kgtypes.EdgeConnectsTo),
			}},
		}},
	}
}

// uniqueProviderName returns a per-test logs provider name. logwire.Register
// panics on duplicates so tests pin their own slot via t.Name + nano.
func uniqueProviderName(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("test-collect-e2e-%s-%d", t.Name(), time.Now().UnixNano())
}

// TestRunLogsCollect_E2E exercises the entries-first wire path against a
// fake IngestServiceClient seeded with a hand-built CloudSubgraph slice.
//
// runLogsCollect runs MaterializeLogGraph CLIENT-SIDE and
// ships the resulting ([]knowledgev1.Node, []kgwire.BatchEdge) via the standard
// UploadSink CollectChunk + Finalize flow — same wire as code/cloud/cicd. The
// test captures the final CollectChunkRequest (which carries the edges) and
// verifies:
//
//   - graph_type == "logs"
//   - edges carry an EMITTED_BY edge into the cloud proxy ID
//   - edges carry a CORRELATES_WITH edge with non-zero Confidence
//     (the BatchEdge.Confidence audit guard)
//
// The proxy-node presence is asserted via the captured CollectResult: the
// handler accumulated the inline chunk nodes and the capturing sink recorded
// the full materialized batch (nodes + edges) on Finalize — no store engine.
func TestRunLogsCollect_E2E(t *testing.T) {
	provName := uniqueProviderName(t)
	logwire.Register(provName, func() logwire.Provider {
		return &stubLogsProvider{entries: e2eEntries(time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC))}
	})

	// In-process ingest server: CollectChunk → Finalize runs end-to-end; the
	// handler captures both the final CollectChunkRequest (edge carrier shape)
	// and the accumulated CollectResult (node bodies).
	srv, captured, handler := startInProcessIngestServer(t, buildSubgraphResponse(t), nil)
	t.Cleanup(srv.close)

	deps := &fakeDeps{sink: remote.NewUploadSink(srv.client)}

	result := runLogsCollect(deps, collectArgs{
		Type:     "logs",
		Provider: provName,
	})
	require.False(t, result.IsError, "runLogsCollect returned IsError; content=%q", resultText(result))

	require.NotNil(t, captured.req, "expected CollectChunk+Finalize to be called")
	assert.Equal(t, string(kgtypes.GraphLogs), captured.req.GraphType)

	edges := batchEdgesFromProtoForTest(captured.req.GetEdges())

	// (a) An EMITTED_BY edge into a proxy:cloud:acct-1:* ID.
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
	assert.True(t, foundProxyEdge, "expected at least one EMITTED_BY edge into a proxy:cloud:acct-1:* node; got edges=%+v", edges)

	// (b) A CORRELATES_WITH edge with non-zero Confidence — the
	// BatchEdge.Confidence audit guard. If this regresses to zero, the
	// new BatchEdge.Confidence field is silently dropped on the wire.
	foundCorr := false
	for _, e := range edges {
		if e.Type != kgtypes.EdgeCorrelatesWith {
			continue
		}
		foundCorr = true
		assert.Greater(t, e.Confidence, 0.0,
			"expected CORRELATES_WITH edge with Confidence > 0 (BatchEdge.Confidence audit guard); got %+v", e)
	}
	assert.True(t, foundCorr, "expected at least one CORRELATES_WITH BatchEdge; got %+v", edges)

	// (c) The reassembled CollectResult batch carries the materialized proxy
	// node and its EMITTED_BY edge — the same node+edge set the server-side
	// CollectChunk+Finalize landed, now asserted on the captured wire
	// payload instead of a store readback.
	batch := handler.sink.last()
	require.NotNil(t, batch, "expected the server to capture a CollectResult")
	assert.Equal(t, kgtypes.GraphLogs, batch.GraphType)

	proxyCount := 0
	for i := range batch.Nodes {
		if kgtypes.NodeType(batch.Nodes[i].Type) == kgtypes.NodeProxy {
			proxyCount++
		}
	}
	require.Positive(t, proxyCount, "expected ≥1 NodeProxy in the materialized batch")

	emittedByCount := 0
	for _, e := range batch.Edges {
		if e.Type == kgtypes.EdgeEmittedBy && strings.HasPrefix(e.ToID, "proxy:cloud:acct-1:") {
			emittedByCount++
		}
	}
	assert.Positive(t, emittedByCount, "expected ≥1 EMITTED_BY edge into a proxy node in the materialized batch")
}

// TestRunLogsCollect_FetchSubgraphError validates the slog.Warn /
// proceed-without-cloud-enrichment path: a transport error from
// FetchCloudSubgraph must NOT fail the tool — runLogsCollect drops the
// resolver/dep-checker and still ships the temporal-only pipeline output
// via CollectChunk+Finalize (no proxy nodes, no resolver-dependent edges).
func TestRunLogsCollect_FetchSubgraphError(t *testing.T) {
	provName := uniqueProviderName(t)
	logwire.Register(provName, func() logwire.Provider {
		return &stubLogsProvider{entries: e2eEntries(time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC))}
	})

	fetchErr := connect.NewError(connect.CodeUnavailable, errors.New("simulated server outage"))
	srv, captured, _ := startInProcessIngestServer(t, nil, fetchErr)
	t.Cleanup(srv.close)

	deps := &fakeDeps{sink: remote.NewUploadSink(srv.client)}

	result := runLogsCollect(deps, collectArgs{
		Type:     "logs",
		Provider: provName,
	})
	require.False(t, result.IsError, "FetchCloudSubgraph error must be non-fatal; content=%q", resultText(result))

	require.NotNil(t, captured.req, "expected CollectChunk+Finalize to be called even after FetchCloudSubgraph failure")
	assert.Equal(t, string(kgtypes.GraphLogs), captured.req.GraphType)

	edges := batchEdgesFromProtoForTest(captured.req.GetEdges())
	for _, e := range edges {
		assert.NotEqualf(t, kgtypes.EdgeEmittedBy, e.Type,
			"FetchCloudSubgraph error → no resolver → no EMITTED_BY edges should ship; got %+v", e)
		if e.Type == kgtypes.EdgeCorrelatesWith {
			// Temporal-only correlations may still ship, but the
			// pipeline's StructurallyConfirmed gate should keep
			// them out without a dep-checker. The materializer
			// only emits CORRELATES_WITH for StructurallyConfirmed.
			t.Fatalf("no dep-checker should mean no StructurallyConfirmed correlations on the wire; got %+v", e)
		}
	}
}

// TestInterceptCollect_NotReadyGate (FAILS-WHEN-ABSENT) proves the bind-first
// wiring-window gate (bind-first startup): with PipelineReady()=false, a collect returns the
// uniform "daemon still starting" error rather than uploading chunks into a
// not-yet-draining pipeline. Dropping the gate would let the collect proceed past
// this point.
func TestInterceptCollect_NotReadyGate(t *testing.T) {
	deps := &fakeDeps{pipelineNotReady: true}
	handled, res := InterceptCollect(deps, kgtools.CallToolParams{
		Name:      "collect",
		Arguments: json.RawMessage(`{"type":"code","id":"/tmp/somerepo"}`),
	})
	require.True(t, handled, "collect must be handled client-side")
	require.True(t, res.IsError, "a not-ready collect must be an error result")
	require.Contains(t, toolResultText(res), "daemon still starting")
}
