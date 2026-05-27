// SPDX-License-Identifier: Apache-2.0

package logs

import (
	"testing"
	"time"

	wirelogs "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// aggTestFixtures builds a consistent set of streams, chunks, and templates
// for aggregation tests.
func aggTestFixtures() ([]*wirelogs.LogStream, []*wirelogs.LogChunk, map[string]*wirelogs.LogTemplate) {
	t0 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	streams := []*wirelogs.LogStream{
		{ID: "s1", Labels: map[string]string{"service": "api", "namespace": "prod"}},
		{ID: "s2", Labels: map[string]string{"service": "api", "namespace": "staging"}},
		{ID: "s3", Labels: map[string]string{"service": "worker", "namespace": "prod"}},
	}

	templates := map[string]*wirelogs.LogTemplate{
		"tmpl-err":  {ID: "tmpl-err", Severity: wirelogs.SeverityError},
		"tmpl-warn": {ID: "tmpl-warn", Severity: wirelogs.SeverityWarn},
		"tmpl-info": {ID: "tmpl-info", Severity: wirelogs.SeverityInfo},
	}

	chunks := []*wirelogs.LogChunk{
		{ID: "c1", StreamID: "s1", TemplateID: "tmpl-err", EntryCount: 10,
			StartTime: t0.Add(1 * time.Hour), EndTime: t0.Add(2 * time.Hour)},
		{ID: "c2", StreamID: "s1", TemplateID: "tmpl-warn", EntryCount: 5,
			StartTime: t0.Add(30 * time.Minute), EndTime: t0.Add(90 * time.Minute)},
		{ID: "c3", StreamID: "s2", TemplateID: "tmpl-info", EntryCount: 20,
			StartTime: t0.Add(2 * time.Hour), EndTime: t0.Add(3 * time.Hour)},
		{ID: "c4", StreamID: "s3", TemplateID: "tmpl-err", EntryCount: 3,
			StartTime: t0, EndTime: t0.Add(30 * time.Minute)},
		{ID: "c5", StreamID: "s3", TemplateID: "tmpl-info", EntryCount: 15,
			StartTime: t0.Add(3 * time.Hour), EndTime: t0.Add(4 * time.Hour)},
	}

	return streams, chunks, templates
}

func TestAggregationSummary_ErrorCounting(t *testing.T) {
	streams, chunks, templates := aggTestFixtures()
	agg := BuildAggregationSummary(streams, chunks, templates)

	// service=api: 10 error + 5 warn + 20 info = 35 total
	s := agg.StatsFor("service", "api")
	if s == nil {
		t.Fatal("expected stats for service=api")
	}
	if s.TotalCount != 35 {
		t.Fatalf("service=api TotalCount = %d, want 35", s.TotalCount)
	}
	if s.ErrorCount != 10 {
		t.Fatalf("service=api ErrorCount = %d, want 10", s.ErrorCount)
	}
	if s.WarnCount != 5 {
		t.Fatalf("service=api WarnCount = %d, want 5", s.WarnCount)
	}
	if s.InfoCount != 20 {
		t.Fatalf("service=api InfoCount = %d, want 20", s.InfoCount)
	}

	// service=worker: 3 error + 15 info = 18 total
	w := agg.StatsFor("service", "worker")
	if w == nil {
		t.Fatal("expected stats for service=worker")
	}
	if w.TotalCount != 18 {
		t.Fatalf("service=worker TotalCount = %d, want 18", w.TotalCount)
	}
	if w.ErrorCount != 3 {
		t.Fatalf("service=worker ErrorCount = %d, want 3", w.ErrorCount)
	}
	if w.WarnCount != 0 {
		t.Fatalf("service=worker WarnCount = %d, want 0", w.WarnCount)
	}
	if w.InfoCount != 15 {
		t.Fatalf("service=worker InfoCount = %d, want 15", w.InfoCount)
	}
}

func TestAggregationSummary_MultiLabelAccumulation(t *testing.T) {
	streams, chunks, templates := aggTestFixtures()
	agg := BuildAggregationSummary(streams, chunks, templates)

	// namespace=prod includes stream s1 (10+5) and s3 (3+15)
	s := agg.StatsFor("namespace", "prod")
	if s == nil {
		t.Fatal("expected stats for namespace=prod")
	}
	if s.TotalCount != 33 {
		t.Fatalf("namespace=prod TotalCount = %d, want 33", s.TotalCount)
	}
	if s.ErrorCount != 13 {
		t.Fatalf("namespace=prod ErrorCount = %d, want 13", s.ErrorCount)
	}
	if s.WarnCount != 5 {
		t.Fatalf("namespace=prod WarnCount = %d, want 5", s.WarnCount)
	}
	if s.InfoCount != 15 {
		t.Fatalf("namespace=prod InfoCount = %d, want 15", s.InfoCount)
	}

	// namespace=staging includes only s2 (20 info)
	st := agg.StatsFor("namespace", "staging")
	if st == nil {
		t.Fatal("expected stats for namespace=staging")
	}
	if st.TotalCount != 20 {
		t.Fatalf("namespace=staging TotalCount = %d, want 20", st.TotalCount)
	}
	if st.ErrorCount != 0 {
		t.Fatalf("namespace=staging ErrorCount = %d, want 0", st.ErrorCount)
	}
}

func TestAggregationSummary_TimeRanges(t *testing.T) {
	streams, chunks, templates := aggTestFixtures()
	agg := BuildAggregationSummary(streams, chunks, templates)
	t0 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	// service=api: earliest chunk start is t0+30m (c2), latest end is t0+3h (c3)
	s := agg.StatsFor("service", "api")
	if s == nil {
		t.Fatal("expected stats for service=api")
	}
	wantFirst := t0.Add(30 * time.Minute)
	wantLast := t0.Add(3 * time.Hour)
	if !s.FirstSeen.Equal(wantFirst) {
		t.Fatalf("service=api FirstSeen = %v, want %v", s.FirstSeen, wantFirst)
	}
	if !s.LastSeen.Equal(wantLast) {
		t.Fatalf("service=api LastSeen = %v, want %v", s.LastSeen, wantLast)
	}

	// service=worker: earliest is t0 (c4), latest is t0+4h (c5)
	w := agg.StatsFor("service", "worker")
	if !w.FirstSeen.Equal(t0) {
		t.Fatalf("service=worker FirstSeen = %v, want %v", w.FirstSeen, t0)
	}
	wantWorkerLast := t0.Add(4 * time.Hour)
	if !w.LastSeen.Equal(wantWorkerLast) {
		t.Fatalf("service=worker LastSeen = %v, want %v", w.LastSeen, wantWorkerLast)
	}
}

func TestAggregationSummary_TemplateIDs(t *testing.T) {
	streams, chunks, templates := aggTestFixtures()
	agg := BuildAggregationSummary(streams, chunks, templates)

	s := agg.StatsFor("service", "api")
	if s == nil {
		t.Fatal("expected stats for service=api")
	}
	// api streams see tmpl-err, tmpl-warn (from s1) and tmpl-info (from s2)
	want := map[string]bool{"tmpl-err": true, "tmpl-warn": true, "tmpl-info": true}
	got := make(map[string]bool, len(s.TemplateIDs))
	for _, id := range s.TemplateIDs {
		got[id] = true
	}
	if len(got) != len(want) {
		t.Fatalf("service=api TemplateIDs = %v, want %v", s.TemplateIDs, want)
	}
	for id := range want {
		if !got[id] {
			t.Fatalf("service=api missing template ID %q", id)
		}
	}
}

func TestAggregationSummary_TopK(t *testing.T) {
	streams, chunks, templates := aggTestFixtures()
	agg := BuildAggregationSummary(streams, chunks, templates)

	// TopK by error_count for "service": api(10) > worker(3)
	top := agg.TopK("service", 10, "error_count")
	if len(top) != 2 {
		t.Fatalf("TopK service len = %d, want 2", len(top))
	}
	if top[0].Value != "api" {
		t.Fatalf("TopK[0].Value = %q, want api", top[0].Value)
	}
	if top[1].Value != "worker" {
		t.Fatalf("TopK[1].Value = %q, want worker", top[1].Value)
	}

	// TopK by total_count for "service": api(35) > worker(18)
	topTotal := agg.TopK("service", 1, "total_count")
	if len(topTotal) != 1 {
		t.Fatalf("TopK total len = %d, want 1", len(topTotal))
	}
	if topTotal[0].Value != "api" {
		t.Fatalf("TopK total[0].Value = %q, want api", topTotal[0].Value)
	}
}

func TestAggregationSummary_TopK_MoreThanAvailable(t *testing.T) {
	streams, chunks, templates := aggTestFixtures()
	agg := BuildAggregationSummary(streams, chunks, templates)

	top := agg.TopK("service", 100, "error_count")
	if len(top) != 2 {
		t.Fatalf("TopK with n>available returned %d, want 2", len(top))
	}
}

func TestAggregationSummary_KeysAndValues(t *testing.T) {
	streams, chunks, templates := aggTestFixtures()
	agg := BuildAggregationSummary(streams, chunks, templates)

	keys := agg.Keys()
	if len(keys) != 2 {
		t.Fatalf("Keys() len = %d, want 2", len(keys))
	}
	if keys[0] != "namespace" || keys[1] != "service" {
		t.Fatalf("Keys() = %v, want [namespace service]", keys)
	}

	vals := agg.Values("service")
	if len(vals) != 2 {
		t.Fatalf("Values(service) len = %d, want 2", len(vals))
	}
	if vals[0] != "api" || vals[1] != "worker" {
		t.Fatalf("Values(service) = %v, want [api worker]", vals)
	}
}

func TestAggregationSummary_Empty(t *testing.T) {
	agg := BuildAggregationSummary(nil, nil, nil)

	if s := agg.StatsFor("any", "thing"); s != nil {
		t.Fatal("expected nil stats for empty aggregation")
	}
	if keys := agg.Keys(); len(keys) != 0 {
		t.Fatalf("expected empty keys, got %v", keys)
	}
	if vals := agg.Values("x"); vals != nil {
		t.Fatalf("expected nil values, got %v", vals)
	}
	if top := agg.TopK("x", 5, "error_count"); top != nil {
		t.Fatalf("expected nil TopK, got %v", top)
	}
}

func TestAggregationSummary_MissingTemplate(t *testing.T) {
	streams := []*wirelogs.LogStream{
		{ID: "s1", Labels: map[string]string{"env": "prod"}},
	}
	chunks := []*wirelogs.LogChunk{
		{ID: "c1", StreamID: "s1", TemplateID: "missing", EntryCount: 5},
	}
	agg := BuildAggregationSummary(streams, chunks, map[string]*wirelogs.LogTemplate{})

	// Chunk with missing template should be skipped.
	if s := agg.StatsFor("env", "prod"); s != nil {
		t.Fatalf("expected nil stats when template is missing, got %+v", s)
	}
}
