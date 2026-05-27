// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

func TestBuildListOptions_SeverityWarnAddsFieldSelector(t *testing.T) {
	q := logwire.Query{SeverityMin: logwire.SeverityWarn}
	opts := buildListOptions(q)
	assert.Contains(t, opts.FieldSelector, "type=Warning")
}

func TestBuildListOptions_SeverityErrorAddsFieldSelector(t *testing.T) {
	q := logwire.Query{SeverityMin: logwire.SeverityError}
	opts := buildListOptions(q)
	assert.Contains(t, opts.FieldSelector, "type=Warning")
}

func TestBuildListOptions_SeverityCriticalAddsFieldSelector(t *testing.T) {
	q := logwire.Query{SeverityMin: logwire.SeverityCritical}
	opts := buildListOptions(q)
	assert.Contains(t, opts.FieldSelector, "type=Warning")
}

func TestBuildListOptions_SeverityInfoOmitsFieldSelector(t *testing.T) {
	q := logwire.Query{SeverityMin: logwire.SeverityInfo}
	opts := buildListOptions(q)
	assert.NotContains(t, opts.FieldSelector, "type=")
}

func TestBuildListOptions_SeverityDebugOmitsFieldSelector(t *testing.T) {
	q := logwire.Query{SeverityMin: logwire.SeverityDebug}
	opts := buildListOptions(q)
	assert.NotContains(t, opts.FieldSelector, "type=")
}

func TestBuildListOptions_EmptySeverityOmitsFieldSelector(t *testing.T) {
	q := logwire.Query{SeverityMin: ""}
	opts := buildListOptions(q)
	assert.Empty(t, opts.FieldSelector)
}

func TestBuildListOptions_FieldFiltersToRegardingSelectors(t *testing.T) {
	q := logwire.Query{FieldFilters: map[string]string{
		"namespace": "prod",
		"kind":      "Pod",
		"reason":    "OOMKilled",
		"type":      "Warning",
	}}
	opts := buildListOptions(q)
	// Deterministic sort order in fieldSelectorClauses means we can check
	// the full string, but for robustness we check substring presence.
	assert.Contains(t, opts.FieldSelector, "regarding.namespace=prod")
	assert.Contains(t, opts.FieldSelector, "regarding.kind=Pod")
	assert.Contains(t, opts.FieldSelector, "reason=OOMKilled")
	assert.Contains(t, opts.FieldSelector, "type=Warning")
}

func TestBuildListOptions_UnknownFieldsAreDropped(t *testing.T) {
	q := logwire.Query{FieldFilters: map[string]string{
		"unknown":   "value",
		"namespace": "prod",
	}}
	opts := buildListOptions(q)
	assert.Contains(t, opts.FieldSelector, "regarding.namespace=prod")
	assert.NotContains(t, opts.FieldSelector, "unknown")
}

func TestBuildListOptions_PageSizeSet(t *testing.T) {
	opts := buildListOptions(logwire.Query{})
	assert.Equal(t, int64(listPageSize), opts.Limit)
}

func TestBuildListOptions_SeverityAndFieldsCombined(t *testing.T) {
	q := logwire.Query{
		SeverityMin:  logwire.SeverityWarn,
		FieldFilters: map[string]string{"namespace": "prod"},
	}
	opts := buildListOptions(q)
	assert.Contains(t, opts.FieldSelector, "type=Warning")
	assert.Contains(t, opts.FieldSelector, "regarding.namespace=prod")
	// Clauses joined by comma (server-side AND).
	assert.Equal(t, 2, strings.Count(opts.FieldSelector, "=")-
		strings.Count(opts.FieldSelector, "=="),
		"expected two selector clauses, got %q", opts.FieldSelector)
}

// --- passesClientFilter --- //

func entry(ts time.Time, sev, msg string, labels map[string]string) logwire.LogEntry {
	if labels == nil {
		labels = map[string]string{}
	}
	return logwire.LogEntry{
		Timestamp: ts,
		Severity:  sev,
		Message:   msg,
		Labels:    labels,
	}
}

func TestPassesClientFilter_TimeRange_Before(t *testing.T) {
	start := time.Date(2026, 4, 14, 10, 0, 0, 0, time.UTC)
	end := start.Add(1 * time.Hour)
	e := entry(start.Add(-5*time.Minute), logwire.SeverityInfo, "msg", nil)
	q := logwire.Query{StartTime: start, EndTime: end}
	assert.False(t, passesClientFilter(e, q))
}

func TestPassesClientFilter_TimeRange_After(t *testing.T) {
	start := time.Date(2026, 4, 14, 10, 0, 0, 0, time.UTC)
	end := start.Add(1 * time.Hour)
	e := entry(end.Add(5*time.Minute), logwire.SeverityInfo, "msg", nil)
	q := logwire.Query{StartTime: start, EndTime: end}
	assert.False(t, passesClientFilter(e, q))
}

func TestPassesClientFilter_TimeRange_Within(t *testing.T) {
	start := time.Date(2026, 4, 14, 10, 0, 0, 0, time.UTC)
	end := start.Add(1 * time.Hour)
	e := entry(start.Add(30*time.Minute), logwire.SeverityInfo, "msg", nil)
	q := logwire.Query{StartTime: start, EndTime: end}
	assert.True(t, passesClientFilter(e, q))
}

func TestPassesClientFilter_TimeRange_Boundary(t *testing.T) {
	start := time.Date(2026, 4, 14, 10, 0, 0, 0, time.UTC)
	end := start.Add(1 * time.Hour)
	q := logwire.Query{StartTime: start, EndTime: end}
	// Entry exactly at start — Before(start) is false, so it passes.
	e := entry(start, logwire.SeverityInfo, "msg", nil)
	assert.True(t, passesClientFilter(e, q))
	// Entry exactly at end — After(end) is false, so it passes.
	e = entry(end, logwire.SeverityInfo, "msg", nil)
	assert.True(t, passesClientFilter(e, q))
}

func TestPassesClientFilter_EmptyBoundsPassEverything(t *testing.T) {
	e := entry(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC),
		logwire.SeverityInfo, "msg", nil)
	q := logwire.Query{} // zero Start and End
	assert.True(t, passesClientFilter(e, q))
}

func TestPassesClientFilter_TextFilterCaseInsensitive(t *testing.T) {
	e := entry(time.Now(), logwire.SeverityError,
		"OOMKilled: Memory cgroup out of memory", nil)
	q := logwire.Query{TextFilter: "OOMKILLED"}
	assert.True(t, passesClientFilter(e, q))
	q = logwire.Query{TextFilter: "oomkilled"}
	assert.True(t, passesClientFilter(e, q))
	q = logwire.Query{TextFilter: "Memory"}
	assert.True(t, passesClientFilter(e, q))
}

func TestPassesClientFilter_TextFilterMiss(t *testing.T) {
	e := entry(time.Now(), logwire.SeverityInfo, "Started", nil)
	q := logwire.Query{TextFilter: "OOMKilled"}
	assert.False(t, passesClientFilter(e, q))
}

func TestPassesClientFilter_EmptyTextFilterPasses(t *testing.T) {
	e := entry(time.Now(), logwire.SeverityInfo, "anything", nil)
	q := logwire.Query{TextFilter: ""}
	assert.True(t, passesClientFilter(e, q))
}

func TestPassesClientFilter_SeverityBelowMin(t *testing.T) {
	e := entry(time.Now(), logwire.SeverityInfo, "msg", nil)
	q := logwire.Query{SeverityMin: logwire.SeverityError}
	assert.False(t, passesClientFilter(e, q))
}

func TestPassesClientFilter_SeverityAtOrAboveMin(t *testing.T) {
	q := logwire.Query{SeverityMin: logwire.SeverityError}
	for _, sev := range []string{logwire.SeverityError, logwire.SeverityCritical} {
		e := entry(time.Now(), sev, "msg", nil)
		assert.True(t, passesClientFilter(e, q), "severity %s should pass", sev)
	}
}

func TestPassesClientFilter_FieldFilterServiceMatches(t *testing.T) {
	e := entry(time.Now(), logwire.SeverityInfo, "msg",
		map[string]string{"service": "api"})
	q := logwire.Query{FieldFilters: map[string]string{"service": "api"}}
	assert.True(t, passesClientFilter(e, q))
}

func TestPassesClientFilter_FieldFilterPodMapsToLabel(t *testing.T) {
	e := entry(time.Now(), logwire.SeverityInfo, "msg",
		map[string]string{"pod_name": "api-x"})
	q := logwire.Query{FieldFilters: map[string]string{"pod": "api-x"}}
	assert.True(t, passesClientFilter(e, q))
}

func TestPassesClientFilter_FieldFilterMismatch(t *testing.T) {
	e := entry(time.Now(), logwire.SeverityInfo, "msg",
		map[string]string{"service": "api"})
	q := logwire.Query{FieldFilters: map[string]string{"service": "other"}}
	assert.False(t, passesClientFilter(e, q))
}

func TestPassesClientFilter_FieldFilterEmptyValueSkipped(t *testing.T) {
	// Sanitize strips to empty → treated as absent → passes.
	e := entry(time.Now(), logwire.SeverityInfo, "msg",
		map[string]string{"service": "api"})
	q := logwire.Query{FieldFilters: map[string]string{"service": ""}}
	assert.True(t, passesClientFilter(e, q))
}

// --- isFieldSelectorRejection --- //

func TestIsFieldSelectorRejection_FieldSelectorText(t *testing.T) {
	assert.True(t, isFieldSelectorRejection(
		errors.New(`unable to parse requirement: "fieldSelector" not allowed`)))
}

func TestIsFieldSelectorRejection_FieldSpacedText(t *testing.T) {
	assert.True(t, isFieldSelectorRejection(
		errors.New(`invalid field selector: type=Warning`)))
}

func TestIsFieldSelectorRejection_UnknownFieldLabel(t *testing.T) {
	assert.True(t, isFieldSelectorRejection(
		errors.New(`field label "regarding.namespace" not supported for events.k8s.io/v1, Event`)))
}

func TestIsFieldSelectorRejection_NotAKnownField(t *testing.T) {
	assert.True(t, isFieldSelectorRejection(
		errors.New(`"regarding.namespace" is not a known field selector`)))
}

func TestIsFieldSelectorRejection_CaseInsensitive(t *testing.T) {
	// Implementation lowercases — confirm that mixed-case apiserver responses
	// still match.
	assert.True(t, isFieldSelectorRejection(
		errors.New("Unknown FIELDSELECTOR rejected")))
}

func TestIsFieldSelectorRejection_OtherErrorReturnsFalse(t *testing.T) {
	assert.False(t, isFieldSelectorRejection(
		errors.New("connection refused")))
	assert.False(t, isFieldSelectorRejection(
		errors.New("context deadline exceeded")))
}

func TestIsFieldSelectorRejection_NilReturnsFalse(t *testing.T) {
	assert.False(t, isFieldSelectorRejection(nil))
}
