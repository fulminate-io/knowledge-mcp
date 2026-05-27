// SPDX-License-Identifier: Apache-2.0

package logs

import (
	"sort"
	"testing"
	"time"

	wirelogs "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

func queryEngineFixtures() ([]*wirelogs.LogStream, []*wirelogs.LogChunk, []*wirelogs.LogTemplate) {
	t0 := time.Date(2025, 6, 15, 10, 0, 0, 0, time.UTC)
	streams := []*wirelogs.LogStream{
		{ID: "s-api-prod", Labels: map[string]string{"service": "api", "namespace": "prod"},
			LowCardLabels:  map[string]string{"service": "api", "namespace": "prod"},
			HighCardLabels: map[string]string{"pod": "api-pod-1"}},
		{ID: "s-api-staging", Labels: map[string]string{"service": "api", "namespace": "staging"},
			LowCardLabels:  map[string]string{"service": "api", "namespace": "staging"},
			HighCardLabels: map[string]string{"pod": "api-pod-2"}},
		{ID: "s-worker-prod", Labels: map[string]string{"service": "worker", "namespace": "prod"},
			LowCardLabels:  map[string]string{"service": "worker", "namespace": "prod"},
			HighCardLabels: map[string]string{"pod": "worker-pod-1"}},
	}
	templates := []*wirelogs.LogTemplate{
		{ID: "t-conn-err", Pattern: "connection refused <*>", Severity: wirelogs.SeverityError},
		{ID: "t-req-info", Pattern: "request handled in <*>ms", Severity: wirelogs.SeverityInfo},
		{ID: "t-retry-warn", Pattern: "retry attempt <*>", Severity: wirelogs.SeverityWarn},
	}
	chunks := []*wirelogs.LogChunk{
		{ID: "c1", StreamID: "s-api-prod", TemplateID: "t-conn-err", EntryCount: 10,
			StartTime: t0, EndTime: t0.Add(1 * time.Hour)},
		{ID: "c2", StreamID: "s-api-prod", TemplateID: "t-retry-warn", EntryCount: 5,
			StartTime: t0.Add(1 * time.Hour), EndTime: t0.Add(2 * time.Hour)},
		{ID: "c3", StreamID: "s-api-staging", TemplateID: "t-req-info", EntryCount: 20,
			StartTime: t0.Add(2 * time.Hour), EndTime: t0.Add(3 * time.Hour)},
		{ID: "c4", StreamID: "s-worker-prod", TemplateID: "t-conn-err", EntryCount: 3,
			StartTime: t0.Add(3 * time.Hour), EndTime: t0.Add(4 * time.Hour)},
		{ID: "c5", StreamID: "s-worker-prod", TemplateID: "t-req-info", EntryCount: 15,
			StartTime: t0.Add(30 * time.Minute), EndTime: t0.Add(90 * time.Minute)},
	}
	return streams, chunks, templates
}

func TestQueryEngine_Overview(t *testing.T) {
	streams, chunks, templates := queryEngineFixtures()
	qe := NewQueryEngine(streams, chunks, templates)

	overview := qe.Overview()
	if len(overview) == 0 {
		t.Fatal("Overview() returned empty map")
	}

	// "service" key should exist with ranked values.
	svcRanked, ok := overview["service"]
	if !ok {
		t.Fatal("Overview() missing 'service' key")
	}
	if len(svcRanked) < 2 {
		t.Fatalf("service ranked has %d entries, want >= 2", len(svcRanked))
	}
	// api has more errors (10) than worker (3), so api should rank first.
	if svcRanked[0].Value != "api" {
		t.Fatalf("service top value = %q, want api", svcRanked[0].Value)
	}
}

func TestQueryEngine_QueryLabels(t *testing.T) {
	streams, chunks, templates := queryEngineFixtures()
	qe := NewQueryEngine(streams, chunks, templates)

	// Single filter: service=api -> two streams.
	got := qe.QueryLabels(map[string]string{"service": "api"})
	if len(got) != 2 {
		t.Fatalf("QueryLabels(service=api) returned %d streams, want 2", len(got))
	}
	ids := streamIDs(got)
	sort.Strings(ids)
	if ids[0] != "s-api-prod" || ids[1] != "s-api-staging" {
		t.Fatalf("QueryLabels(service=api) IDs = %v, want [s-api-prod s-api-staging]", ids)
	}

	// Multi-filter: service=api AND namespace=prod -> one stream.
	got = qe.QueryLabels(map[string]string{"service": "api", "namespace": "prod"})
	if len(got) != 1 || got[0].ID != "s-api-prod" {
		t.Fatalf("QueryLabels(service=api, namespace=prod) = %v, want [s-api-prod]", streamIDs(got))
	}

	// No match.
	got = qe.QueryLabels(map[string]string{"service": "unknown"})
	if got != nil {
		t.Fatalf("QueryLabels(service=unknown) = %v, want nil", streamIDs(got))
	}
}

func TestQueryEngine_SeverityRange(t *testing.T) {
	streams, chunks, templates := queryEngineFixtures()
	qe := NewQueryEngine(streams, chunks, templates)

	// WARN+ should include s-api-prod (ERROR, WARN) and s-worker-prod (ERROR).
	got := qe.QuerySeverityRange(wirelogs.SeverityWarn)
	ids := streamIDs(got)
	sort.Strings(ids)
	if len(ids) != 2 || ids[0] != "s-api-prod" || ids[1] != "s-worker-prod" {
		t.Fatalf("SeverityRange(WARN) IDs = %v, want [s-api-prod s-worker-prod]", ids)
	}

	// ERROR+ should include s-api-prod and s-worker-prod (both have ERROR chunks).
	got = qe.QuerySeverityRange(wirelogs.SeverityError)
	ids = streamIDs(got)
	sort.Strings(ids)
	if len(ids) != 2 || ids[0] != "s-api-prod" || ids[1] != "s-worker-prod" {
		t.Fatalf("SeverityRange(ERROR) IDs = %v, want [s-api-prod s-worker-prod]", ids)
	}

	// CRITICAL: none.
	got = qe.QuerySeverityRange(wirelogs.SeverityCritical)
	if got != nil {
		t.Fatalf("SeverityRange(CRITICAL) = %v, want nil", streamIDs(got))
	}
}

func TestQueryEngine_LabelsForTemplate(t *testing.T) {
	streams, chunks, templates := queryEngineFixtures()
	qe := NewQueryEngine(streams, chunks, templates)

	// t-conn-err is used by s-api-prod and s-worker-prod.
	labels := qe.LabelsForTemplate("t-conn-err")
	if labels == nil {
		t.Fatal("LabelsForTemplate(t-conn-err) returned nil")
	}
	svcVals := labels["service"]
	sort.Strings(svcVals)
	if len(svcVals) != 2 || svcVals[0] != "api" || svcVals[1] != "worker" {
		t.Fatalf("service values = %v, want [api worker]", svcVals)
	}
	nsVals := labels["namespace"]
	if len(nsVals) != 1 || nsVals[0] != "prod" {
		t.Fatalf("namespace values = %v, want [prod]", nsVals)
	}

	// Unknown template returns empty map.
	labels = qe.LabelsForTemplate("unknown-tmpl")
	if len(labels) != 0 {
		t.Fatalf("LabelsForTemplate(unknown) = %v, want empty", labels)
	}
}

func TestQueryEngine_FilterByTimeRange(t *testing.T) {
	streams, chunks, templates := queryEngineFixtures()
	qe := NewQueryEngine(streams, chunks, templates)
	t0 := time.Date(2025, 6, 15, 10, 0, 0, 0, time.UTC)

	// First hour: overlaps c1 [t0, t0+1h], c2 [t0+1h, t0+2h] (boundary), c5 [t0+30m, t0+90m].
	got := qe.FilterByTimeRange(chunks, t0, t0.Add(1*time.Hour))
	gotIDs := chunkIDs(got)
	sort.Strings(gotIDs)
	if len(gotIDs) != 3 {
		t.Fatalf("FilterByTimeRange first hour got %v, want 3 chunks", gotIDs)
	}

	// Non-overlapping: before all chunks.
	got = qe.FilterByTimeRange(chunks, t0.Add(-2*time.Hour), t0.Add(-1*time.Hour))
	if len(got) != 0 {
		t.Fatalf("FilterByTimeRange before all got %d, want 0", len(got))
	}

	// Exact match on a single chunk boundary.
	got = qe.FilterByTimeRange(chunks, t0.Add(3*time.Hour), t0.Add(4*time.Hour))
	gotIDs = chunkIDs(got)
	// Should include c3 (ends at t0+3h, boundary overlap) and c4 [t0+3h, t0+4h].
	if len(gotIDs) < 1 {
		t.Fatalf("FilterByTimeRange exact match got %v, want >= 1", gotIDs)
	}

	// Empty input returns nil.
	got = qe.FilterByTimeRange(nil, t0, t0.Add(time.Hour))
	if got != nil {
		t.Fatalf("FilterByTimeRange(nil) = %v, want nil", got)
	}
}

func TestQueryEngine_TemplatesForLabels(t *testing.T) {
	streams, chunks, templates := queryEngineFixtures()
	qe := NewQueryEngine(streams, chunks, templates)

	// service=api -> streams s-api-prod (c1:t-conn-err, c2:t-retry-warn) and
	// s-api-staging (c3:t-req-info). Should yield all 3 templates.
	got := qe.TemplatesForLabels(map[string]string{"service": "api"})
	gotIDs := templateIDs(got)
	sort.Strings(gotIDs)
	if len(gotIDs) != 3 {
		t.Fatalf("TemplatesForLabels(service=api) = %v, want 3 templates", gotIDs)
	}

	// namespace=staging -> only s-api-staging (c3:t-req-info).
	got = qe.TemplatesForLabels(map[string]string{"namespace": "staging"})
	if len(got) != 1 || got[0].ID != "t-req-info" {
		t.Fatalf("TemplatesForLabels(namespace=staging) = %v, want [t-req-info]", templateIDs(got))
	}

	// No match returns nil.
	got = qe.TemplatesForLabels(map[string]string{"service": "nonexistent"})
	if got != nil {
		t.Fatalf("TemplatesForLabels(nonexistent) = %v, want nil", templateIDs(got))
	}
}

func TestQueryEngine_StatsFor(t *testing.T) {
	streams, chunks, templates := queryEngineFixtures()
	qe := NewQueryEngine(streams, chunks, templates)

	s := qe.StatsFor("service", "api")
	if s == nil {
		t.Fatal("StatsFor(service, api) returned nil")
	}
	// api: 10 error (c1) + 5 warn (c2) + 20 info (c3) = 35
	if s.TotalCount != 35 {
		t.Fatalf("service=api TotalCount = %d, want 35", s.TotalCount)
	}
	if s.ErrorCount != 10 {
		t.Fatalf("service=api ErrorCount = %d, want 10", s.ErrorCount)
	}

	// Non-existent returns nil.
	if qe.StatsFor("missing", "key") != nil {
		t.Fatal("StatsFor(missing, key) should be nil")
	}
}

func TestQueryEngine_TopK(t *testing.T) {
	streams, chunks, templates := queryEngineFixtures()
	qe := NewQueryEngine(streams, chunks, templates)

	top := qe.TopK("service", 1)
	if len(top) != 1 {
		t.Fatalf("TopK(service, 1) len = %d, want 1", len(top))
	}
	// api has 10 errors > worker has 3 errors.
	if top[0].Value != "api" {
		t.Fatalf("TopK(service, 1)[0] = %q, want api", top[0].Value)
	}
}

func TestQueryEngine_Empty(t *testing.T) {
	qe := NewQueryEngine(nil, nil, nil)

	overview := qe.Overview()
	if len(overview) != 0 {
		t.Fatalf("empty Overview() = %v, want empty", overview)
	}
	if got := qe.QueryLabels(map[string]string{"k": "v"}); got != nil {
		t.Fatalf("empty QueryLabels = %v, want nil", got)
	}
	if got := qe.QuerySeverityRange(wirelogs.SeverityWarn); got != nil {
		t.Fatalf("empty SeverityRange = %v, want nil", got)
	}
	if labels := qe.LabelsForTemplate("t"); len(labels) != 0 {
		t.Fatalf("empty LabelsForTemplate = %v, want empty", labels)
	}
	if got := qe.FilterByTimeRange(nil, time.Now(), time.Now()); got != nil {
		t.Fatalf("empty FilterByTimeRange = %v, want nil", got)
	}
	if got := qe.TemplatesForLabels(map[string]string{"k": "v"}); got != nil {
		t.Fatalf("empty TemplatesForLabels = %v, want nil", got)
	}
	if s := qe.StatsFor("k", "v"); s != nil {
		t.Fatalf("empty StatsFor = %v, want nil", s)
	}
	if top := qe.TopK("k", 5); top != nil {
		t.Fatalf("empty TopK = %v, want nil", top)
	}
}

func streamIDs(ss []*wirelogs.LogStream) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.ID
	}
	return out
}

func chunkIDs(cs []*wirelogs.LogChunk) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.ID
	}
	return out
}

func templateIDs(ts []*wirelogs.LogTemplate) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.ID
	}
	return out
}
