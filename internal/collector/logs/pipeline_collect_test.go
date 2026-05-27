// SPDX-License-Identifier: Apache-2.0

package logs

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	wirelogs "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// fakeProvider emits a fixed batch of entries on every Collect. Used by
// the Pipeline.Collect integration tests to drive the orchestration
// without pulling a real log backend into the test run.
type fakeProvider struct {
	id      string
	entries []wirelogs.LogEntry
	calls   int
}

func (f *fakeProvider) Configure(map[string]string) error { return nil }
func (f *fakeProvider) Collect(_ context.Context, _ wirelogs.Query, emit func([]wirelogs.LogEntry) error) error {
	f.calls++
	if len(f.entries) == 0 {
		return nil
	}
	return emit(f.entries)
}
func (f *fakeProvider) ListSources(context.Context, string) ([]wirelogs.Source, error) {
	return nil, nil
}

// TestPipelineCollect_FullOrchestration exercises every stage of
// Pipeline.CollectFromEntries end-to-end: provider drain, clustering,
// chunking, correlation, and summary. It also verifies the
// QueryEngine is registered and recoverable by queryID.
//
// BCN11.2: graph writes moved out of the pipeline entirely. The
// CollectResult slices the client materializes via WriteResult are the
// assertion surface here.
func TestPipelineCollect_FullOrchestration(t *testing.T) {
	queryID := fmt.Sprintf("query-%d", time.Now().UnixNano())
	base := time.Date(2026, 4, 13, 14, 0, 0, 0, time.UTC)
	entries := baseEntries(base)

	resolver := &mockResolver{services: map[string]string{
		"api": "arn:cloud:api",
		"db":  "arn:cloud:db",
	}}

	provider := &fakeProvider{id: "fake", entries: entries}
	pipeline := NewPipeline(
		provider,
		queryID,
		WithCloudResolver(resolver),
		// Checker keys on the cloud-graph IDs returned by the resolver.
		WithDependencyChecker(newFakeChecker([2]string{"arn:cloud:api", "arn:cloud:db"})),
	)

	ctx := context.Background()
	q := wirelogs.Query{StartTime: base, EndTime: base.Add(time.Minute)}
	rawEntries, err := CollectEntries(ctx, provider, q)
	if err != nil {
		t.Fatalf("CollectEntries: %v", err)
	}
	rawEntries = ReclassifySeverity(rawEntries)
	result, err := pipeline.CollectFromEntries(ctx, rawEntries, q)
	if err != nil {
		t.Fatalf("CollectFromEntries: %v", err)
	}
	if result == nil {
		t.Fatal("CollectFromEntries returned nil result")
	}

	if provider.calls != 1 {
		t.Errorf("expected provider.Collect called once, got %d", provider.calls)
	}
	if result.QueryID != queryID {
		t.Errorf("result.QueryID = %q, want %q", result.QueryID, queryID)
	}
	if result.TotalEntries != len(entries) {
		t.Errorf("TotalEntries = %d, want %d", result.TotalEntries, len(entries))
	}
	if len(result.Templates) == 0 {
		t.Error("expected templates from collection")
	}
	if len(result.Streams) == 0 {
		t.Error("expected streams from collection")
	}
	if len(result.Chunks) == 0 {
		t.Error("expected chunks from collection")
	}
	if result.QueryEngine == nil {
		t.Error("QueryEngine should be populated")
	}
	if result.TimeRange.Start.IsZero() || result.TimeRange.End.IsZero() {
		t.Errorf("TimeRange should bound the collection, got %+v", result.TimeRange)
	}
	if !result.TimeRange.Start.Equal(base) {
		t.Errorf("TimeRange.Start = %v, want %v", result.TimeRange.Start, base)
	}
	if !strings.Contains(result.Summary, "Log Collection Summary") {
		t.Errorf("Summary missing header: %q", result.Summary)
	}

	// Engine registry lookup should return the same pointer.
	engine, ok := LookupEngine(queryID)
	if !ok {
		t.Fatal("LookupEngine failed to return engine after Collect")
	}
	if engine != result.QueryEngine {
		t.Error("LookupEngine returned a different engine than CollectResult")
	}

	// CollectResult slices carry the data the client materializes;
	// no in-pipeline graph to inspect post-BCN11.2.
	if len(result.Templates) == 0 {
		t.Error("expected templates in CollectResult after Collect")
	}

	t.Cleanup(func() { UnregisterEngine(queryID) })
}

// TestPipelineCollect_EmptyProvider confirms CollectFromEntries handles an
// empty entry slice gracefully — no chunks, no correlations, still a
// non-nil result with a header-only summary.
func TestPipelineCollect_EmptyProvider(t *testing.T) {
	queryID := fmt.Sprintf("query-%d", time.Now().UnixNano())
	provider := &fakeProvider{id: "fake"}
	pipeline := NewPipeline(provider, queryID)

	ctx := context.Background()
	entries, err := CollectEntries(ctx, provider, wirelogs.Query{})
	if err != nil {
		t.Fatalf("CollectEntries: %v", err)
	}
	result, err := pipeline.CollectFromEntries(ctx, ReclassifySeverity(entries), wirelogs.Query{})
	if err != nil {
		t.Fatalf("CollectFromEntries: %v", err)
	}
	if result.TotalEntries != 0 {
		t.Errorf("TotalEntries = %d, want 0", result.TotalEntries)
	}
	if len(result.Chunks) != 0 {
		t.Errorf("expected no chunks, got %d", len(result.Chunks))
	}
	if result.CorrelationsFound != 0 {
		t.Errorf("expected no correlations, got %d", result.CorrelationsFound)
	}
	if !strings.Contains(result.Summary, "Log Collection Summary") {
		t.Errorf("empty summary missing header: %q", result.Summary)
	}

	t.Cleanup(func() { UnregisterEngine(queryID) })
}

// TestPipelineCollect_NilCallbacks_Degrades confirms the pipeline runs
// end-to-end with nil cloudResolver + nil dependencyChecker — correlations
// and proxy resolution are skipped but the core pipeline still builds.
func TestPipelineCollect_NilCallbacks_Degrades(t *testing.T) {
	queryID := fmt.Sprintf("query-%d", time.Now().UnixNano())
	base := time.Date(2026, 4, 13, 14, 0, 0, 0, time.UTC)
	provider := &fakeProvider{id: "fake", entries: baseEntries(base)}
	pipeline := NewPipeline(provider, queryID)

	ctx := context.Background()
	q := wirelogs.Query{StartTime: base, EndTime: base.Add(time.Minute)}
	entries, err := CollectEntries(ctx, provider, q)
	if err != nil {
		t.Fatalf("CollectEntries: %v", err)
	}
	result, err := pipeline.CollectFromEntries(ctx, ReclassifySeverity(entries), q)
	if err != nil {
		t.Fatalf("CollectFromEntries: %v", err)
	}
	if result.CorrelationsFound != 0 {
		t.Errorf("nil checker should yield no confirmed correlations, got %d", result.CorrelationsFound)
	}
	if result.QueryEngine == nil {
		t.Error("QueryEngine should still be built without cloud resolver")
	}

	t.Cleanup(func() { UnregisterEngine(queryID) })
}

// TestEngineRegistry_RoundTrip exercises wirelogs.Register/Lookup/Unregister.
func TestEngineRegistry_RoundTrip(t *testing.T) {
	id := "test-engine-" + time.Now().Format("150405.000")
	defer UnregisterEngine(id)

	// Empty lookup.
	if _, ok := LookupEngine(id); ok {
		t.Error("expected missing engine to return ok=false")
	}

	engine := NewQueryEngine(nil, nil, nil)
	RegisterEngine(id, engine)

	got, ok := LookupEngine(id)
	if !ok || got != engine {
		t.Errorf("Lookup after wirelogs.Register failed: got=%v ok=%v", got, ok)
	}

	UnregisterEngine(id)
	if _, ok := LookupEngine(id); ok {
		t.Error("expected engine gone after Unregister")
	}

	// Empty queryID is a no-op.
	RegisterEngine("", engine)
	if _, ok := LookupEngine(""); ok {
		t.Error("empty queryID should never be stored")
	}

	// Nil engine removes the entry.
	RegisterEngine(id, engine)
	RegisterEngine(id, nil)
	if _, ok := LookupEngine(id); ok {
		t.Error("nil engine should remove registry entry")
	}
}
