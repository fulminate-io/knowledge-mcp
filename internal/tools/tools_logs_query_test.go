// SPDX-License-Identifier: Apache-2.0

// Package tools — log graph query tests.
//
// These exercise handleLogsQuery end-to-end against a fakeLogGraphCaller
// seeded with a store-FREE corpus (buildLogCorpus). The synthetic provider
// emits a handful of entries across multiple services/severities so the
// QueryEngine has something non-trivial to index and rank — but no real
// store engine is linked; the handler reads everything over the fake's
// Execute carrier seam.
package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/logs"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// fakeLogsProvider is an in-memory logwire.Provider that emits a fixed entry
// batch. We define it in the tools package so the tests can drive
// logs.Pipeline.Collect without depending on a real backend.
type fakeLogsProvider struct {
	entries []logwire.LogEntry
}

func (f *fakeLogsProvider) Configure(map[string]string) error { return nil }
func (f *fakeLogsProvider) ListSources(context.Context, string) ([]logwire.Source, error) {
	return nil, nil
}
func (f *fakeLogsProvider) Collect(
	_ context.Context, _ logwire.Query, emit func([]logwire.LogEntry) error,
) error {
	if len(f.entries) == 0 {
		return nil
	}
	return emit(f.entries)
}

// syntheticLogEntries returns a small but realistic mix of entries: api
// ERRORs (connection refused), db ERRORs (pool exhausted), and worker
// INFO messages. Spread across a one-minute window so chunk assembly
// produces a handful of distinct (stream, template, window) buckets.
func syntheticLogEntries(base time.Time) []logwire.LogEntry {
	var entries []logwire.LogEntry
	for i := range 10 {
		entries = append(entries, logwire.LogEntry{
			Timestamp: base.Add(time.Duration(i) * time.Second),
			Severity:  logwire.SeverityError,
			Message:   fmt.Sprintf("connection refused host-%d", i),
			Labels:    map[string]string{"service": "api", "pod": fmt.Sprintf("api-%d", i%2)},
		})
	}
	for i := range 6 {
		entries = append(entries, logwire.LogEntry{
			Timestamp: base.Add(time.Duration(10+i) * time.Second),
			Severity:  logwire.SeverityError,
			Message:   fmt.Sprintf("pool exhausted client-%d", i),
			Labels:    map[string]string{"service": "db"},
		})
	}
	for i := range 4 {
		entries = append(entries, logwire.LogEntry{
			Timestamp: base.Add(time.Duration(20+i) * time.Second),
			Severity:  logwire.SeverityInfo,
			Message:   fmt.Sprintf("processed job %d", i),
			Labels:    map[string]string{"service": "worker"},
		})
	}
	return entries
}

// buildLogStateFromCorpus builds the *logState the log handlers' internal
// formatters consume directly, partitioning a store-FREE corpus (from
// buildLogCorpus, optionally augmented with seeded edges) by node type and
// feeding newLogState. This replaces the old store-backed state adapter — it
// reads from value-type slices instead of querying a live store.DB.
func buildLogStateFromCorpus(nodes []*knowledgev1.Node, edges []*knowledgev1.Edge) *logState {
	var templates, streams, chunks, labels, proxies []*knowledgev1.Node
	for _, n := range nodes {
		switch kgtypes.NodeType(n.Type) {
		case kgtypes.NodeLogTemplate:
			templates = append(templates, n)
		case kgtypes.NodeLogStream:
			streams = append(streams, n)
		case kgtypes.NodeLogChunk:
			chunks = append(chunks, n)
		case kgtypes.NodeLogLabel:
			labels = append(labels, n)
		case kgtypes.NodeProxy:
			proxies = append(proxies, n)
		}
	}
	// newLogState consumes a value slice (it copies each element via copyEdge into
	// the per-node OutEdges/InEdges maps); deref the pointer carrier here.
	valEdges := make([]knowledgev1.Edge, len(edges))
	for i, e := range edges {
		valEdges[i] = copyEdge(e)
	}
	return newLogState(templates, streams, chunks, labels, proxies, valEdges)
}

// firstNodeIDOfType returns the ID of the first corpus node of type t. Tests
// that need a real seeded ID (e.g. a template ID to feed the template-detail
// handler) use it instead of reading IDs back out of a store.DB.
func firstNodeIDOfType(t *testing.T, nodes []*knowledgev1.Node, nt kgtypes.NodeType) string {
	t.Helper()
	for _, n := range nodes {
		if kgtypes.NodeType(n.Type) == nt {
			return n.Id
		}
	}
	t.Fatalf("no node of type %s in corpus", nt)
	return ""
}

// templateNodeIDs returns every corpus log-template node ID, in corpus order.
// Tests that need multiple seeded template IDs (e.g. distinct template pairs for
// seeded correlation edges) use it instead of querying a store.DB.
func templateNodeIDs(nodes []*knowledgev1.Node) []string {
	var ids []string
	for _, n := range nodes {
		if kgtypes.NodeType(n.Type) == kgtypes.NodeLogTemplate {
			ids = append(ids, n.Id)
		}
	}
	return ids
}

// TestLogsQuery_Overview asserts the overview shape surfaces label keys
// plus their ranked values with error/warn/info counts.
func TestLogsQuery_Overview(t *testing.T) {
	queryID := "q-overview"
	h := setupLogTestHandler(t, queryID)
	result := h.handleLogsQuery(context.Background(), queryArgs{Graph: "logs", Name: queryID})
	require.False(t, result.IsError, "overview: %s", resultText(result))
	text := resultText(result)

	assert.Contains(t, text, "Log overview", "response should carry the overview header")
	assert.Contains(t, text, queryID, "response should mention the queryID")
	assert.Contains(t, text, "service", "service label key should appear")
	assert.Contains(t, text, "api", "api label value should surface")
	assert.Contains(t, text, "db", "db label value should surface")
}

// TestLogsQuery_LabelDrillDown asserts that an AND filter narrows the
// stream list to the matching service and includes at least one template.
func TestLogsQuery_LabelDrillDown(t *testing.T) {
	queryID := "q-drilldown"
	h := setupLogTestHandler(t, queryID)
	result := h.handleLogsQuery(context.Background(), queryArgs{
		Graph: "logs", Name: queryID, Text: "service=api",
	})
	require.False(t, result.IsError, "drill-down: %s", resultText(result))
	text := resultText(result)

	assert.Contains(t, text, "Log drill-down", "drill-down header should appear")
	assert.Contains(t, text, "service=api", "filter expression should echo")
	assert.Contains(t, text, "Templates", "templates section should render")
	// The worker service should NOT show up — its streams don't carry service=api.
	assert.NotContains(t, text, "service=worker",
		"drill-down for service=api must exclude worker streams")
}

// TestLogsQuery_SeverityRange asserts severity>=WARN picks up the ERROR
// streams (api + db) and drops the INFO-only worker stream.
func TestLogsQuery_SeverityRange(t *testing.T) {
	queryID := "q-severity"
	h := setupLogTestHandler(t, queryID)
	result := h.handleLogsQuery(context.Background(), queryArgs{
		Graph: "logs", Name: queryID, Text: "severity>=WARN",
	})
	require.False(t, result.IsError, "severity drill-down: %s", resultText(result))
	text := resultText(result)

	assert.Contains(t, text, "severity >= WARN",
		"rendered filter should show normalized severity range")
	// Worker is INFO-only, so its service label must not surface in either
	// the streams section or the templates section.
	assert.NotContains(t, text, "service=worker",
		"INFO-only worker streams must not match severity>=WARN")
}

// TestLogsQuery_TemplateDetail asserts a template lookup by ID renders
// decompressed example entries (via logs.DecodeChunk) and the affected
// label values.
func TestLogsQuery_TemplateDetail(t *testing.T) {
	queryID := "q-template"
	nodes, edges := buildLogCorpus(t, queryID)
	fake := newFakeLogGraphCaller()
	fake.seedLogGraph(queryID, nodes, edges)
	h := &Handler{graphCallerOverride: fake}

	// Pick a real template ID out of the corpus. Any one works — the
	// pipeline emits at least one template per distinct cluster.
	templateID := firstNodeIDOfType(t, nodes, kgtypes.NodeLogTemplate)

	result := h.handleLogsQuery(context.Background(), queryArgs{
		Graph: "logs", Name: queryID, ID: templateID,
	})
	require.False(t, result.IsError, "template detail: %s", resultText(result))
	text := resultText(result)

	assert.Contains(t, text, "Log template", "template header should render")
	assert.Contains(t, text, templateID, "template ID should appear")
	assert.Contains(t, text, "Affected labels", "affected labels section should render")
	assert.Contains(t, text, "Examples", "examples section should render for real chunk data")
	assert.Contains(t, text, "**Alias:**", "template header must include readable alias")
}

// TestLogsQuery_EngineRebuild confirms a cold engine (no registry entry)
// rebuilds from the persisted graph so queries still work post-restart.
func TestLogsQuery_EngineRebuild(t *testing.T) {
	queryID := "q-rebuild"
	h := setupLogTestHandler(t, queryID)

	// BCN11.3 removed the engine cache (reviewer T2-B): handlers always
	// refetch + rebuild via getOrFetchLogState. UnregisterEngine + the
	// registry assertion are no-ops now — the test still exercises the
	// wire-fetch + rebuild path against the seeded corpus.
	logs.UnregisterEngine(queryID)

	result := h.handleLogsQuery(context.Background(), queryArgs{Graph: "logs", Name: queryID})
	require.False(t, result.IsError, "rebuild overview: %s", resultText(result))
	text := resultText(result)
	assert.Contains(t, text, "Log overview", "rebuild path must still render overview")
	assert.Contains(t, text, queryID)
}

// TestLogsQuery_RequiresName enforces the contract: name is mandatory for
// log-graph queries because there is no sensible default graph.
func TestLogsQuery_RequiresName(t *testing.T) {
	h := &Handler{graphCallerOverride: newFakeLogGraphCaller()}
	result := h.handleLogsQuery(context.Background(), queryArgs{Graph: "logs"})
	require.True(t, result.IsError, "missing name should error")
	assert.Contains(t, resultText(result), "name")
}

// TestLogsQuery_UnknownTemplate verifies the template-detail path returns
// an error when the requested ID isn't in the engine.
func TestLogsQuery_UnknownTemplate(t *testing.T) {
	queryID := "q-unknown-tpl"
	h := setupLogTestHandler(t, queryID)
	result := h.handleLogsQuery(context.Background(), queryArgs{
		Graph: "logs", Name: queryID, ID: "log-template:does-not-exist",
	})
	require.True(t, result.IsError, "unknown template should error")
	assert.Contains(t, resultText(result), "not found")
}

// TestLogsQuery_UnknownGraph asserts the handler treats a completely cold
// queryID as empty rather than erroring — the caller may have just typo'd
// a name, and we want the response to signal "no data yet" instead of a
// cryptic Retrieve failure.
func TestLogsQuery_UnknownGraph(t *testing.T) {
	// A queryID with no seeded corpus → the fake returns an empty node set →
	// getOrFetchLogState yields a nil engine → the handler signals "no
	// persisted graph" rather than rendering an empty overview.
	h := &Handler{graphCallerOverride: newFakeLogGraphCaller()}
	result := h.handleLogsQuery(context.Background(), queryArgs{
		Graph: "logs", Name: "q-does-not-exist",
	})
	require.True(t, result.IsError, "cold unknown queryID should error (no engine, no graph)")
	txt := resultText(result)
	assert.True(t,
		strings.Contains(txt, "no engine") || strings.Contains(txt, "not found") ||
			strings.Contains(txt, "no persisted graph"),
		"error should signal missing graph, got: %s", txt,
	)
}

// TestParseLogFilters_ValidForms covers the positive cases of the filter
// grammar: plain key=value, severity>=LEVEL, and a mix separated by
// whitespace. Asserts both the label map and the normalized minSeverity.
func TestParseLogFilters_ValidForms(t *testing.T) {
	tests := []struct {
		in          string
		wantLabels  map[string]string
		wantMinSev  string
		wantErrText string
	}{
		{
			in:         "service=api",
			wantLabels: map[string]string{"service": "api"},
		},
		{
			in:         "severity>=WARN",
			wantMinSev: logwire.SeverityWarn,
		},
		{
			in:         "service=api severity>=ERROR",
			wantLabels: map[string]string{"service": "api"},
			wantMinSev: logwire.SeverityError,
		},
		{
			in:         "severity>INFO",
			wantMinSev: logwire.SeverityWarn, // "> INFO" == ">= WARN"
		},
		{
			in:         "severity=CRITICAL",
			wantMinSev: logwire.SeverityCritical,
		},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			labels, minSev, err := parseLogFilters(tc.in)
			require.NoError(t, err, "input %q should parse", tc.in)
			if tc.wantLabels == nil {
				assert.Empty(t, labels)
			} else {
				assert.Equal(t, tc.wantLabels, labels)
			}
			assert.Equal(t, tc.wantMinSev, minSev)
		})
	}
}

// TestParseLogFilters_Errors covers the negative cases: unsupported "<"
// comparator, malformed tokens, unknown severity levels.
func TestParseLogFilters_Errors(t *testing.T) {
	bad := []string{
		"severity<INFO",
		"severity<=WARN",
		"severity>=NOT_A_LEVEL",
		"garbage",
		"key=",
		"=value",
		"severity>CRITICAL", // nothing above critical
	}
	for _, tc := range bad {
		t.Run(tc, func(t *testing.T) {
			_, _, err := parseLogFilters(tc)
			assert.Error(t, err, "expected error for %q", tc)
		})
	}
}
