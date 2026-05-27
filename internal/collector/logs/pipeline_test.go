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

// BCN11.2 deleted the in-process Pipeline.store path along with the
// buildGraph / writeProxiesFromResolutions / writeCorrelations
// helpers. The end-to-end coverage now lives in
// TestPipelineCollect_FullOrchestration (pipeline_collect_test.go),
// which exercises the pure-transform shape (CollectFromEntries →
// CollectResult) the client materializes via WriteResult. The DB-write
// tests that previously called those helpers directly are removed
// rather than rewritten; their assertions don't survive the helper
// deletion.

// mockResolver maps a small set of service names to ResolvedResources.
// The optional account override drives the Account field on every hit
// — tests use it to point all resolutions at a fixed cloud graph name
// when they want to assert against a single account, or leave it empty
// to fall back to "test-acct".
type mockResolver struct {
	services   map[string]string
	namespaces map[string]string
	account    string
}

func (m *mockResolver) ResolveService(_ context.Context, _ *wirelogs.LogStream, name string) (ResolvedResource, bool) {
	id, ok := m.services[name]
	if !ok {
		return ResolvedResource{}, false
	}
	return ResolvedResource{Account: m.accountOrDefault(), ID: id}, true
}

func (m *mockResolver) ResolveNamespace(_ context.Context, _ *wirelogs.LogStream, ns string) (ResolvedResource, bool) {
	id, ok := m.namespaces[ns]
	if !ok {
		return ResolvedResource{}, false
	}
	return ResolvedResource{Account: m.accountOrDefault(), ID: id}, true
}

func (m *mockResolver) accountOrDefault() string {
	if m.account != "" {
		return m.account
	}
	return "test-acct"
}

// BCN11.2 removed newTestLogStore — the pipeline is a pure transform
// and never touches store.DB. Tests that need a queryID build one with
// fmt.Sprintf("query-%d", time.Now().UnixNano()) directly.

// baseEntries creates a synthetic batch of log entries spread across three
// services and three templates. The API errors overlap in time with the
// DB errors; worker logs sit nearby but reference an unrelated service.
func baseEntries(base time.Time) []wirelogs.LogEntry {
	var entries []wirelogs.LogEntry
	// api: 40 "connection refused" errors
	for i := range 40 {
		entries = append(entries, wirelogs.LogEntry{
			Timestamp: base.Add(time.Duration(i) * time.Second),
			Severity:  wirelogs.SeverityError,
			Message:   fmt.Sprintf("connection refused host-%d", i),
			Labels:    map[string]string{wirelogs.FieldService: "api", "pod": fmt.Sprintf("api-%d", i%4)},
		})
	}
	// db: 20 "pool exhausted" errors overlapping with api's window
	for i := range 20 {
		entries = append(entries, wirelogs.LogEntry{
			Timestamp: base.Add(time.Duration(10+i) * time.Second),
			Severity:  wirelogs.SeverityError,
			Message:   fmt.Sprintf("pool exhausted client-%d", i),
			Labels:    map[string]string{wirelogs.FieldService: "db", "pod": fmt.Sprintf("db-%d", i%2)},
		})
	}
	// worker: 10 INFO entries - should NOT correlate (wrong severity).
	for i := range 10 {
		entries = append(entries, wirelogs.LogEntry{
			Timestamp: base.Add(time.Duration(i) * time.Second),
			Severity:  wirelogs.SeverityInfo,
			Message:   fmt.Sprintf("processed job-%d", i),
			Labels:    map[string]string{wirelogs.FieldService: "worker"},
		})
	}
	return entries
}

// TestPipeline_WithoutCloudGraph_NilResolver: pure-transform variant.
// Confirms a nil resolver produces an empty resolutions slice and that
// findCorrelations leaves every correlation unconfirmed.
func TestPipeline_WithoutCloudGraph_NilResolver(t *testing.T) {
	base := time.Date(2026, 4, 13, 14, 0, 0, 0, time.UTC)
	entries := baseEntries(base)

	ctx := context.Background()
	templates, tplIDs := processEntries(entries, DefaultDrainConfig())
	streams, sidIDs := buildStreams(entries, 0)
	chunks, err := assembleChunks(entries, sidIDs, tplIDs, templates, 5*time.Minute)
	if err != nil {
		t.Fatalf("assembleChunks: %v", err)
	}

	// Nil resolver → no resolutions, no proxy work, no error.
	resolutions := computeStreamResolutions(ctx, streams, nil)
	if len(resolutions) != 0 {
		t.Errorf("nil resolver should produce empty resolutions slice, got %d entries", len(resolutions))
	}

	// Correlation with nil resolver+checker still runs, producing unconfirmed candidates.
	correlations := findCorrelations(ctx, templates, chunks, streams, nil, nil, nil)
	for _, c := range correlations {
		if c.StructurallyConfirmed {
			t.Errorf("nil checker should leave correlations unconfirmed, got %+v", c)
		}
	}
}

// TestPipeline_CorrelationConfirmed_PureTransform: pure-transform
// variant. Confirms that a confirming DependencyChecker marks the
// expected api↔db correlation StructurallyConfirmed. The actual
// CORRELATES_WITH edge write happens client-side via
// MaterializeLogGraph and is covered by the client materialize tests.
func TestPipeline_CorrelationConfirmed_PureTransform(t *testing.T) {
	base := time.Date(2026, 4, 13, 14, 0, 0, 0, time.UTC)
	entries := baseEntries(base)

	ctx := context.Background()
	templates, tplIDs := processEntries(entries, DefaultDrainConfig())
	streams, sidIDs := buildStreams(entries, 0)
	chunks, err := assembleChunks(entries, sidIDs, tplIDs, templates, 5*time.Minute)
	if err != nil {
		t.Fatalf("assembleChunks: %v", err)
	}

	resolver := &mockResolver{services: map[string]string{
		"api": "arn:cloud:api",
		"db":  "arn:cloud:db",
	}}
	resolutions := computeStreamResolutions(ctx, streams, resolver)
	if len(resolutions) < 2 {
		t.Fatalf("expected resolutions for api+db, got %v", resolutions)
	}

	proxyMap := make(map[string]string, len(resolutions))
	for _, r := range resolutions {
		proxyMap[r.LabelValue] = r.Account + ":" + r.ResourceID
	}

	checker := newFakeChecker([2]string{"arn:cloud:api", "arn:cloud:db"})
	correlations := findCorrelations(ctx, templates, chunks, streams, proxyMap, resolver, checker)
	hasConfirmed := false
	for _, c := range correlations {
		if c.StructurallyConfirmed {
			hasConfirmed = true
		}
	}
	if !hasConfirmed {
		t.Fatalf("expected confirmed api↔db correlation, got %+v", correlations)
	}
}

func TestPipeline_Summary_ContainsTopPatterns(t *testing.T) {
	base := time.Date(2026, 4, 13, 14, 0, 0, 0, time.UTC)
	entries := baseEntries(base)

	templates, tplIDs := processEntries(entries, DefaultDrainConfig())
	streams, sidIDs := buildStreams(entries, 0)
	chunks, err := assembleChunks(entries, sidIDs, tplIDs, templates, 5*time.Minute)
	if err != nil {
		t.Fatalf("assembleChunks: %v", err)
	}
	tmplByID := templatesByID(templates)
	agg := BuildAggregationSummary(streams, chunks, tmplByID)

	// Run correlations with a confirming checker so both sections appear.
	proxyMap := map[string]string{"api": "arn:api", "db": "arn:db", "worker": "arn:worker"}
	resolver := &mockResolver{services: map[string]string{
		"api":    "arn:api",
		"db":     "arn:db",
		"worker": "arn:worker",
	}}
	checker := newFakeChecker([2]string{"arn:api", "arn:db"})
	correlations := findCorrelations(
		context.Background(), templates, chunks, streams, proxyMap, resolver, checker,
	)

	query := wirelogs.Query{StartTime: base, EndTime: base.Add(time.Minute)}
	summary := buildSummary(templates, streams, chunks, correlations, agg, query)
	if summary == "" {
		t.Fatal("expected non-empty summary")
	}
	if !strings.Contains(summary, "Log Collection Summary") {
		t.Errorf("summary missing header: %s", summary)
	}
	if !strings.Contains(summary, "Top Error Patterns") {
		t.Errorf("summary missing error-pattern section: %s", summary)
	}
	if !strings.Contains(summary, "api") {
		t.Errorf("summary missing api service: %s", summary)
	}
	if len(summary) > summaryMaxChars {
		t.Errorf("summary exceeds cap (%d > %d)", len(summary), summaryMaxChars)
	}
	// Top patterns should surface a template alias (back-tick-wrapped)
	// for at least one of the rendered templates so operators can
	// reference patterns by their readable form.
	atLeastOne := false
	for _, t := range templates {
		alias := t.Alias
		if alias == "" {
			alias = TemplateAliasFor(t)
		}
		if alias != "" && strings.Contains(summary, "`"+alias+"`") {
			atLeastOne = true
			break
		}
	}
	if !atLeastOne {
		t.Errorf("summary should contain at least one template alias: %s", summary)
	}
}

func TestPipeline_Summary_NoCorrelationsSectionWhenEmpty(t *testing.T) {
	base := time.Date(2026, 4, 13, 14, 0, 0, 0, time.UTC)
	entries := baseEntries(base)

	templates, tplIDs := processEntries(entries, DefaultDrainConfig())
	streams, sidIDs := buildStreams(entries, 0)
	chunks, err := assembleChunks(entries, sidIDs, tplIDs, templates, 5*time.Minute)
	if err != nil {
		t.Fatalf("assembleChunks: %v", err)
	}
	tmplByID := templatesByID(templates)
	agg := BuildAggregationSummary(streams, chunks, tmplByID)

	summary := buildSummary(templates, streams, chunks, nil, agg, wirelogs.Query{})
	if strings.Contains(summary, "Correlations Found") {
		t.Errorf("empty correlations should not produce section: %s", summary)
	}
	if strings.Contains(summary, "Possibly Related") {
		t.Errorf("empty correlations should not produce possibly-related section: %s", summary)
	}
}

func TestPipeline_EmptyCollection(t *testing.T) {
	ctx := context.Background()

	// Zero entries through all stages.
	templates, tplIDs := processEntries(nil, DefaultDrainConfig())
	streams, sidIDs := buildStreams(nil, 0)
	chunks, err := assembleChunks(nil, sidIDs, tplIDs, templates, 5*time.Minute)
	if err != nil {
		t.Fatalf("assembleChunks: %v", err)
	}

	correlations := findCorrelations(ctx, templates, chunks, streams, nil, nil, nil)
	if len(correlations) != 0 {
		t.Errorf("expected no correlations from empty collection, got %d", len(correlations))
	}

	summary := buildSummary(templates, streams, chunks, correlations, nil, wirelogs.Query{})
	// Summary should still render a header.
	if !strings.Contains(summary, "Log Collection Summary") {
		t.Errorf("empty collection summary missing header: %s", summary)
	}
}
