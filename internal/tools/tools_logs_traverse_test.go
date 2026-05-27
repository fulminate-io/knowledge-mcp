// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/logs"
	collectorlogs "github.com/fulminate-io/knowledge-mcp/internal/collector/logs"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// fakeLogProvider is a minimal logwire.Provider implementation that emits a
// fixed batch of entries on every Collect call. Tests drive the real
// pipeline through it so chunks carry genuine ZSTD-compressed payloads
// and the traverse handler exercises the real decode code path.
type fakeLogProvider struct {
	entries []logwire.LogEntry
}

// fakeCloudResolver is an inline test double for logs.CloudResolver.
// It returns the configured ResolvedResource for any non-empty service
// name and nothing for namespaces. The server tests only need the
// service-to-cloud edge to surface in traverse output, so namespace
// resolution stays empty.
type fakeCloudResolver struct {
	resource logs.ResolvedResource
}

func (f *fakeCloudResolver) ResolveService(_ context.Context, _ *logwire.LogStream, service string) (logs.ResolvedResource, bool) {
	if service == "" {
		return logs.ResolvedResource{}, false
	}
	return f.resource, true
}

func (f *fakeCloudResolver) ResolveNamespace(_ context.Context, _ *logwire.LogStream, _ string) (logs.ResolvedResource, bool) {
	return logs.ResolvedResource{}, false
}

func (f *fakeLogProvider) Configure(map[string]string) error { return nil }
func (f *fakeLogProvider) Collect(_ context.Context, _ logwire.Query, emit func([]logwire.LogEntry) error) error {
	if len(f.entries) == 0 {
		return nil
	}
	return emit(f.entries)
}
func (f *fakeLogProvider) ListSources(context.Context, string) ([]logwire.Source, error) {
	return nil, nil
}

// logGraphFixture captures the key IDs a traverseLogs test needs to
// assert against the nodes it seeds. Bundling them keeps each test's
// setup/exec/assert phases short.
type logGraphFixture struct {
	QueryID    string
	TemplateID string
	StreamID   string
	ChunkIDs   []string
	// Nodes is the materialized store-free corpus seeded onto the fake.
	// Alias-resolution tests rebuild the engine from it via engineFromCorpus
	// (the process-local logs.LookupEngine registry is not populated under the
	// fake).
	Nodes []*knowledgev1.Node
}

// buildLogTraversalFixture runs a tiny end-to-end logs pipeline as a pure
// transform (no store) and seeds the resulting corpus onto a
// fakeLogGraphCaller-backed *Handler. It uses logs.NewPipeline so chunks
// carry real ZSTD-compressed data and the traverse path exercises the live
// decode helper. A nil resolver skips proxy wiring entirely; a non-nil
// resolver makes the pipeline emit ResolvedProxyEntry rows that
// MaterializeLogGraph turns into NodeProxy nodes + EMITTED_BY edges in the
// corpus — no cloud graph needs to be loaded for the fakeCloudResolver path.
func buildLogTraversalFixture(
	t *testing.T, resolver logs.CloudResolver,
) (*logGraphFixture, *Handler) {
	t.Helper()
	base := time.Date(2026, 4, 13, 14, 0, 0, 0, time.UTC)
	entries := make([]logwire.LogEntry, 0, 10)
	for i := range 6 {
		entries = append(entries, logwire.LogEntry{
			Timestamp: base.Add(time.Duration(i) * time.Second),
			Severity:  logwire.SeverityError,
			Message:   "connection refused host-a",
			Labels:    map[string]string{logwire.FieldService: "api", "pod": "api-0"},
		})
	}
	for i := range 4 {
		entries = append(entries, logwire.LogEntry{
			Timestamp: base.Add(time.Duration(10+i) * time.Second),
			Severity:  logwire.SeverityInfo,
			Message:   "request handled in 12ms",
			Labels:    map[string]string{logwire.FieldService: "api", "pod": "api-0"},
		})
	}

	queryID := "q-traversal"
	opts := []logs.PipelineOption{}
	if resolver != nil {
		opts = append(opts, logs.WithCloudResolver(resolver))
	}
	provider := &fakeLogProvider{entries: entries}
	pipeline := logs.NewPipeline(provider, queryID, opts...)
	q := logwire.Query{StartTime: base, EndTime: base.Add(time.Minute)}
	rawEntries, err := logs.CollectEntries(context.Background(), provider, q)
	require.NoError(t, err)
	result, err := pipeline.CollectFromEntries(context.Background(), logs.ReclassifySeverity(rawEntries), q)
	require.NoError(t, err)
	require.NotNil(t, result)
	t.Cleanup(func() { logs.UnregisterEngine(queryID) })

	// BCN11.2: pipeline is a pure transform. Materialize the result into a
	// store-free corpus and seed it onto the fake so subsequent traversal
	// queries walk the same graph over the Execute carrier seam.
	nodes, batchEdges, err := collectorlogs.MaterializeLogGraph(
		result.QueryID, result.Templates, result.Streams, result.Chunks,
		result.Correlations, result.Resolutions,
	)
	require.NoError(t, err)
	fake := newFakeLogGraphCaller()
	fake.seedLogGraph(queryID, nodes, batchEdgesToEdges(batchEdges))

	f := &logGraphFixture{QueryID: queryID, Nodes: nodes}
	if len(result.Templates) > 0 {
		f.TemplateID = result.Templates[0].ID
	}
	if len(result.Streams) > 0 {
		f.StreamID = result.Streams[0].ID
	}
	for _, c := range result.Chunks {
		f.ChunkIDs = append(f.ChunkIDs, c.ID)
	}
	return f, &Handler{graphCallerOverride: fake}
}

// TestTraverseLogs_TemplateDown walks from a template and asserts chunks
// are reached AND one chunk has decoded example entries rendered inline.
// Decoding is what distinguishes this from a plain "contains" walk and
// is the single most important thing to cover.
func TestTraverseLogs_TemplateDown(t *testing.T) {
	f, h := buildLogTraversalFixture(t, nil)
	require.NotEmpty(t, f.TemplateID)

	result := h.traverseLogs(context.Background(), traverseArgs{
		Graph: "logs", Name: f.QueryID, Start: f.TemplateID, Direction: "down",
	})
	require.False(t, result.IsError, "expected success, got: %s", resultText(result))
	text := resultText(result)

	assert.Contains(t, text, "Log template", "section header must name the node type")
	assert.Contains(t, text, "Chunks", "chunk section must render")
	assert.Contains(t, text, "2026-04-13T14:", "decoded entry timestamps must appear inline")
}

// TestTraverseLogs_StreamBoth asserts the stream walk covers labels and
// chunks. Cloud proxies are absent because no resolver is wired — the
// resolver-wired path lives in TestTraverseLogs_StreamBothWithCloudProxies.
func TestTraverseLogs_StreamBoth(t *testing.T) {
	f, h := buildLogTraversalFixture(t, nil)
	require.NotEmpty(t, f.StreamID)

	result := h.traverseLogs(context.Background(), traverseArgs{
		Graph: "logs", Name: f.QueryID, Start: f.StreamID, Direction: "both",
	})
	require.False(t, result.IsError, "expected success, got: %s", resultText(result))
	text := resultText(result)

	assert.Contains(t, text, "Log stream", "header should name the stream")
	assert.Contains(t, text, "Shared labels", "labels section must render")
	assert.Contains(t, text, "service=api", "inline stream label must render")
	assert.Contains(t, text, "Chunks", "chunks section must render")
	assert.Contains(t, text, "**Alias:**", "header must include readable alias line")
	assert.Contains(t, text, "**ID:**", "header must include canonical ID line")
}

// TestTraverseLogs_StreamBothWithCloudProxies wires a CloudResolver that
// matches the api service to a cloud node, so the pipeline lays down an
// EMITTED_BY edge the traversal should follow and render. The resolver
// auto-discovers the cloud graph from loaded graphs, so the cloud graph
// name is no longer fixed at construction time — any loaded graph
// containing a matching SymbolName works.
func TestTraverseLogs_StreamBothWithCloudProxies(t *testing.T) {
	// The fakeCloudResolver returns a fixed ResolvedResource for any non-empty
	// service, so the pipeline emits a ResolvedProxyEntry that
	// MaterializeLogGraph turns into a NodeProxy + EMITTED_BY edge in the
	// corpus. No cloud graph needs to be loaded — the resolver is a pure
	// value-type double, not a real cloud-graph scanner.
	const apiResourceID = "arn:aws:ecs:us-east-1:1:service/api"
	resolver := &fakeCloudResolver{resource: logs.ResolvedResource{Account: "test-account", ID: apiResourceID}}
	f, h := buildLogTraversalFixture(t, resolver)
	require.NotEmpty(t, f.StreamID)

	result := h.traverseLogs(context.Background(), traverseArgs{
		Graph: "logs", Name: f.QueryID, Start: f.StreamID, Direction: "both",
	})
	require.False(t, result.IsError, "expected success, got: %s", resultText(result))
	text := resultText(result)

	assert.Contains(t, text, "cloud:", "cloud proxy pointer must render")
	assert.Contains(t, text, "api", "resolved cloud resource symbol name should appear")
}

// TestTraverseLogs_UnknownStart returns an error when the start ID does
// not exist in the target log graph. The contract expects a loud miss,
// not a silent empty response.
func TestTraverseLogs_UnknownStart(t *testing.T) {
	f, h := buildLogTraversalFixture(t, nil)

	result := h.traverseLogs(context.Background(), traverseArgs{
		Graph: "logs", Name: f.QueryID, Start: "log-template:does-not-exist", Direction: "down",
	})
	require.True(t, result.IsError, "unknown start should be an error")
	assert.Contains(t, resultText(result), "not found")
}

// TestTraverseLogs_UnknownGraph returns an error when the log graph
// itself was never collected.
func TestTraverseLogs_UnknownGraph(t *testing.T) {
	// An unseeded queryID → the fake returns an empty corpus → the start node
	// is "not found" in the (empty) log state.
	h := &Handler{graphCallerOverride: newFakeLogGraphCaller()}
	result := h.traverseLogs(context.Background(), traverseArgs{
		Graph: "logs", Name: "q-missing", Start: "log-template:abc", Direction: "down",
	})
	require.True(t, result.IsError)
	assert.Contains(t, resultText(result), "not found")
}

// TestTraverseLogs_WrongStartType rejects a start node that is neither a
// template nor a stream. Chunks and labels are valid nodes in the graph
// but not valid entry points for the traversal view.
func TestTraverseLogs_WrongStartType(t *testing.T) {
	f, h := buildLogTraversalFixture(t, nil)
	require.NotEmpty(t, f.ChunkIDs)

	result := h.traverseLogs(context.Background(), traverseArgs{
		Graph: "logs", Name: f.QueryID, Start: f.ChunkIDs[0], Direction: "down",
	})
	require.True(t, result.IsError, "chunk start should be rejected")
	text := resultText(result)
	assert.Contains(t, text, "log-template")
	assert.Contains(t, text, "log-stream")
}

// TestTraverseLogs_NoChunks covers the template-with-no-children edge
// case. An isolated template (not emitted by buildGraph, but still a
// real possibility after retention trims chunks) should produce a clean
// "no chunks" response instead of a cryptic empty section.
func TestTraverseLogs_NoChunks(t *testing.T) {
	queryID := "q-no-chunks"
	orphan := knowledgev1.Node{
		Id:         "log-template:orphan",
		Type:       string(kgtypes.NodeLogTemplate),
		SymbolName: "orphan template <*>",
		Metadata:   map[string]string{"pattern": "orphan template <*>", "severity": "INFO", "count": "0"},
	}
	fake := newFakeLogGraphCaller()
	fake.seedLogGraph(queryID, []*knowledgev1.Node{&orphan}, nil)
	h := &Handler{graphCallerOverride: fake}

	result := h.traverseLogs(context.Background(), traverseArgs{
		Graph: "logs", Name: queryID, Start: "log-template:orphan", Direction: "down",
	})
	require.False(t, result.IsError, "orphan template should not error")
	text := resultText(result)
	assert.Contains(t, text, "No chunks stored")
}

// TestTraverseLogs_ViaHandleTraverse exercises the public dispatch entry
// point so the graph/name routing added in Phase 6 step 1 is covered.
func TestTraverseLogs_ViaHandleTraverse(t *testing.T) {
	f, h := buildLogTraversalFixture(t, nil)

	raw, err := json.Marshal(map[string]any{
		"graph": "logs", "name": f.QueryID, "start": f.TemplateID, "direction": "out",
	})
	require.NoError(t, err)
	result := h.handleTraverse(context.Background(), raw)
	require.False(t, result.IsError, "expected success, got: %s", resultText(result))
	assert.Contains(t, resultText(result), "Log template")
}
