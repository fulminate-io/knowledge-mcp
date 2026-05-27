// SPDX-License-Identifier: Apache-2.0

// Package tools — log graph timeline mode tests.
//
// The synthetic pipeline fixture in tools_logs_query_test.go gives us
// three services with distinct FirstSeen timestamps, which is enough
// to exercise T0 anchoring, offset ordering, and both the flat and
// bucketed rendering paths. Unit-level tests cover the edge cases
// (no timestamps, invalid bucket, disjoint timing) without going
// through the pipeline.
package tools

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/logs"
	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// TestLogsQuery_TimelineFlat asserts the default (no bucket) shape
// renders the templates ordered by FirstSeen with T+offset labels.
func TestLogsQuery_TimelineFlat(t *testing.T) {
	queryID := "q-timeline-flat"
	h := setupLogTestHandler(t, queryID)
	result := h.handleLogsQuery(context.Background(), queryArgs{
		Graph: "logs", Name: queryID, Mode: "timeline",
	})
	require.False(t, result.IsError, "timeline: %s", resultText(result))
	text := resultText(result)

	assert.Contains(t, text, "Log timeline", "header should render")
	assert.Contains(t, text, "T0:", "T0 anchor should be shown")
	assert.Contains(t, text, "T+0s", "earliest template should be at T+0s")
	assert.Contains(t, text, "ERR", "severity column should render short form")
}

// TestLogsQuery_TimelineBucketed asserts bucket="5s" collapses rows
// into windows and renders the window-level totals.
func TestLogsQuery_TimelineBucketed(t *testing.T) {
	queryID := "q-timeline-bucket"
	h := setupLogTestHandler(t, queryID)
	result := h.handleLogsQuery(context.Background(), queryArgs{
		Graph: "logs", Name: queryID, Mode: "timeline",
		Extra: map[string]string{"bucket": "5s"},
	})
	require.False(t, result.IsError, "timeline bucketed: %s", resultText(result))
	text := resultText(result)

	assert.Contains(t, text, "Log timeline (bucketed)", "bucketed header should render")
	assert.Contains(t, text, "bucket:", "bucket size should be printed")
	// Bucket rows look like "T+Xs–T+Ys" — the en-dash between offsets is stable.
	assert.Contains(t, text, "T+0s–T+5s", "first bucket row should start at T+0s")
}

// TestLogsQuery_TimelineInvalidBucket asserts a malformed bucket string
// produces a helpful error, not a crash.
func TestLogsQuery_TimelineInvalidBucket(t *testing.T) {
	queryID := "q-timeline-bad-bucket"
	h := setupLogTestHandler(t, queryID)
	result := h.handleLogsQuery(context.Background(), queryArgs{
		Graph: "logs", Name: queryID, Mode: "timeline",
		Extra: map[string]string{"bucket": "not-a-duration"},
	})
	require.True(t, result.IsError, "invalid bucket should error")
	assert.Contains(t, resultText(result), "invalid bucket")
}

// TestBuildTimelineRows_OrderingAndSpan verifies the core math: offsets
// are relative to T0, rows are sorted by FirstSeen, spans come from
// LastSeen−FirstSeen.
func TestBuildTimelineRows_OrderingAndSpan(t *testing.T) {
	base := time.Date(2026, 4, 15, 1, 0, 0, 0, time.UTC)
	templates := []*logwire.LogTemplate{
		{ID: "b", Alias: "b@err", FirstSeen: base.Add(5 * time.Second), LastSeen: base.Add(15 * time.Second), Count: 3, Severity: logwire.SeverityError},
		{ID: "a", Alias: "a@info", FirstSeen: base, LastSeen: base.Add(2 * time.Second), Count: 1, Severity: logwire.SeverityInfo},
		{ID: "c", Alias: "c@warn", FirstSeen: base.Add(30 * time.Second), LastSeen: base.Add(30 * time.Second), Count: 5, Severity: logwire.SeverityWarn},
	}
	timed, untimed := partitionTemplatesByTiming(templates)
	assert.Len(t, timed, 3)
	assert.Empty(t, untimed)

	sortTemplatesByFirstSeen(timed)
	assert.Equal(t, "a", timed[0].ID, "earliest FirstSeen wins")
	assert.Equal(t, "b", timed[1].ID)
	assert.Equal(t, "c", timed[2].ID)

	rows := buildTimelineRows(timed, timed[0].FirstSeen)
	require.Len(t, rows, 3)
	assert.Equal(t, time.Duration(0), rows[0].Offset, "T0 row offset is 0")
	assert.Equal(t, 5*time.Second, rows[1].Offset, "second row offset is +5s")
	assert.Equal(t, 30*time.Second, rows[2].Offset, "third row offset is +30s")
	assert.Equal(t, 2*time.Second, rows[0].Span, "span = LastSeen − FirstSeen")
	assert.Equal(t, time.Duration(0), rows[2].Span, "zero-duration span stays zero")
}

// TestGroupRowsByBucket asserts bucketing floors offsets to the
// nearest bucket boundary and returns groups ordered ascending.
func TestGroupRowsByBucket(t *testing.T) {
	rows := []timelineRow{
		{Template: &logwire.LogTemplate{Alias: "a"}, Offset: 0 * time.Second},
		{Template: &logwire.LogTemplate{Alias: "b"}, Offset: 3 * time.Second},
		{Template: &logwire.LogTemplate{Alias: "c"}, Offset: 12 * time.Second},
		{Template: &logwire.LogTemplate{Alias: "d"}, Offset: 14 * time.Second},
		{Template: &logwire.LogTemplate{Alias: "e"}, Offset: 25 * time.Second},
	}
	groups := groupRowsByBucket(rows, 10*time.Second)
	require.Len(t, groups, 3, "buckets at T+0, T+10, T+20")
	assert.Equal(t, 0*time.Second, groups[0].offset)
	assert.Len(t, groups[0].rows, 2, "T+0 bucket holds a and b")
	assert.Equal(t, 10*time.Second, groups[1].offset)
	assert.Len(t, groups[1].rows, 2, "T+10 bucket holds c and d")
	assert.Equal(t, 20*time.Second, groups[2].offset)
	assert.Len(t, groups[2].rows, 1, "T+20 bucket holds e")
}

// TestGroupRowsByBucket_ZeroBucket asserts a zero or negative bucket
// returns nil rather than panicking with a divide-by-zero.
func TestGroupRowsByBucket_ZeroBucket(t *testing.T) {
	rows := []timelineRow{{Offset: 5 * time.Second}}
	assert.Nil(t, groupRowsByBucket(rows, 0))
	assert.Nil(t, groupRowsByBucket(rows, -1*time.Second))
}

// TestHandleLogsTimeline_AllUntimed asserts the degraded-but-useful
// path when no template carries a FirstSeen timestamp.
func TestHandleLogsTimeline_AllUntimed(t *testing.T) {
	tpl := &logwire.LogTemplate{ID: "t", Pattern: "x", Severity: logwire.SeverityInfo}
	engine := logs.NewQueryEngine(nil, nil, []*logwire.LogTemplate{tpl})

	result := handleLogsTimeline("q-untimed", engine, "")
	require.False(t, result.IsError)
	text := resultText(result)
	assert.Contains(t, text, "No templates carry FirstSeen")
	assert.Contains(t, text, "Untimed templates")
}
