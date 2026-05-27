// SPDX-License-Identifier: Apache-2.0

package logs

import (
	"sort"
	"testing"
	"time"

	wirelogs "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// labelIndexFixtures builds a small dataset of streams, chunks, and templates
// for LabelIndex testing. Three streams, three templates, four chunks.
func labelIndexFixtures() ([]*wirelogs.LogStream, []*wirelogs.LogChunk, []*wirelogs.LogTemplate, *StreamIDMap) {
	streams := []*wirelogs.LogStream{
		{
			ID:             "stream-a",
			LowCardLabels:  map[string]string{"service": "api", "namespace": "prod"},
			HighCardLabels: map[string]string{"pod": "api-pod-1"},
		},
		{
			ID:             "stream-b",
			LowCardLabels:  map[string]string{"service": "api", "namespace": "staging"},
			HighCardLabels: map[string]string{"pod": "api-pod-2"},
		},
		{
			ID:             "stream-c",
			LowCardLabels:  map[string]string{"service": "worker", "namespace": "prod"},
			HighCardLabels: map[string]string{"pod": "worker-pod-1"},
		},
	}

	templates := []*wirelogs.LogTemplate{
		{ID: "tmpl-err", Pattern: "connection refused <*>", Severity: wirelogs.SeverityError},
		{ID: "tmpl-info", Pattern: "request handled in <*>ms", Severity: wirelogs.SeverityInfo},
		{ID: "tmpl-warn", Pattern: "retry attempt <*>", Severity: wirelogs.SeverityWarn},
	}

	now := time.Now()
	chunks := []*wirelogs.LogChunk{
		{ID: "chunk-1", StreamID: "stream-a", TemplateID: "tmpl-err", StartTime: now, EndTime: now.Add(time.Hour)},
		{ID: "chunk-2", StreamID: "stream-a", TemplateID: "tmpl-warn", StartTime: now, EndTime: now.Add(time.Hour)},
		{ID: "chunk-3", StreamID: "stream-b", TemplateID: "tmpl-info", StartTime: now, EndTime: now.Add(time.Hour)},
		{ID: "chunk-4", StreamID: "stream-c", TemplateID: "tmpl-warn", StartTime: now, EndTime: now.Add(time.Hour)},
	}

	idMap := NewStreamIDMap()
	for _, s := range streams {
		idMap.Add(s.ID)
	}

	return streams, chunks, templates, idMap
}

func TestLabelIndex_Construction(t *testing.T) {
	streams, chunks, templates, idMap := labelIndexFixtures()
	li := NewLabelIndex(streams, chunks, templates, idMap)

	// Verify that label keys include both low-card and high-card labels plus severity.
	keys := li.LabelKeys()
	expected := []string{"namespace", "pod", "service", "severity"}
	if len(keys) != len(expected) {
		t.Fatalf("LabelKeys() = %v, want %v", keys, expected)
	}
	for i, k := range keys {
		if k != expected[i] {
			t.Fatalf("LabelKeys()[%d] = %q, want %q", i, k, expected[i])
		}
	}

	// Verify cardinality: service has 2 values (api, worker).
	svcVals := li.LabelValues("service")
	if len(svcVals) != 2 {
		t.Fatalf("LabelValues('service') has %d values, want 2", len(svcVals))
	}
}

func TestLabelIndex_SingleLabelMatch(t *testing.T) {
	streams, chunks, templates, idMap := labelIndexFixtures()
	li := NewLabelIndex(streams, chunks, templates, idMap)

	// service=api should match stream-a and stream-b.
	bm := li.MatchLabels(map[string]string{"service": "api"})
	ids := li.ResolveStreamIDs(bm)
	sort.Strings(ids)
	if len(ids) != 2 || ids[0] != "stream-a" || ids[1] != "stream-b" {
		t.Fatalf("MatchLabels(service=api) = %v, want [stream-a stream-b]", ids)
	}
}

func TestLabelIndex_MultiLabelIntersection(t *testing.T) {
	streams, chunks, templates, idMap := labelIndexFixtures()
	li := NewLabelIndex(streams, chunks, templates, idMap)

	// service=api AND namespace=prod should match only stream-a.
	bm := li.MatchLabels(map[string]string{"service": "api", "namespace": "prod"})
	ids := li.ResolveStreamIDs(bm)
	if len(ids) != 1 || ids[0] != "stream-a" {
		t.Fatalf("MatchLabels(service=api, namespace=prod) = %v, want [stream-a]", ids)
	}
}

func TestLabelIndex_NoMatch(t *testing.T) {
	streams, chunks, templates, idMap := labelIndexFixtures()
	li := NewLabelIndex(streams, chunks, templates, idMap)

	// Non-existent label value.
	bm := li.MatchLabels(map[string]string{"service": "unknown"})
	if bm.GetCardinality() != 0 {
		t.Fatalf("MatchLabels(service=unknown) cardinality = %d, want 0", bm.GetCardinality())
	}

	// Non-existent label key.
	bm = li.MatchLabels(map[string]string{"region": "us-east-1"})
	if bm.GetCardinality() != 0 {
		t.Fatalf("MatchLabels(region=us-east-1) cardinality = %d, want 0", bm.GetCardinality())
	}
}

func TestLabelIndex_SeverityRange(t *testing.T) {
	streams, chunks, templates, idMap := labelIndexFixtures()
	li := NewLabelIndex(streams, chunks, templates, idMap)

	// Severity >= WARN: stream-a (ERROR max), stream-c (WARN max).
	bm := li.SeverityRange(wirelogs.SeverityWarn)
	ids := li.ResolveStreamIDs(bm)
	sort.Strings(ids)
	if len(ids) != 2 || ids[0] != "stream-a" || ids[1] != "stream-c" {
		t.Fatalf("SeverityRange(WARN) = %v, want [stream-a stream-c]", ids)
	}

	// Severity >= ERROR: only stream-a.
	bm = li.SeverityRange(wirelogs.SeverityError)
	ids = li.ResolveStreamIDs(bm)
	if len(ids) != 1 || ids[0] != "stream-a" {
		t.Fatalf("SeverityRange(ERROR) = %v, want [stream-a]", ids)
	}

	// Severity >= TRACE: all three streams.
	bm = li.SeverityRange(wirelogs.SeverityTrace)
	ids = li.ResolveStreamIDs(bm)
	if len(ids) != 3 {
		t.Fatalf("SeverityRange(TRACE) = %v, want 3 streams", ids)
	}

	// Severity >= CRITICAL: none (no CRITICAL streams in fixtures).
	bm = li.SeverityRange(wirelogs.SeverityCritical)
	ids = li.ResolveStreamIDs(bm)
	if len(ids) != 0 {
		t.Fatalf("SeverityRange(CRITICAL) = %v, want empty", ids)
	}
}

func TestLabelIndex_LabelValues(t *testing.T) {
	streams, chunks, templates, idMap := labelIndexFixtures()
	li := NewLabelIndex(streams, chunks, templates, idMap)

	vals := li.LabelValues("namespace")
	expected := []string{"prod", "staging"}
	if len(vals) != len(expected) {
		t.Fatalf("LabelValues('namespace') = %v, want %v", vals, expected)
	}
	for i, v := range vals {
		if v != expected[i] {
			t.Fatalf("LabelValues('namespace')[%d] = %q, want %q", i, v, expected[i])
		}
	}

	// Non-existent key returns nil.
	vals = li.LabelValues("nonexistent")
	if vals != nil {
		t.Fatalf("LabelValues('nonexistent') = %v, want nil", vals)
	}
}

func TestLabelIndex_LabelKeys(t *testing.T) {
	streams, chunks, templates, idMap := labelIndexFixtures()
	li := NewLabelIndex(streams, chunks, templates, idMap)

	keys := li.LabelKeys()
	// Should be sorted: namespace, pod, service, severity.
	for i := 1; i < len(keys); i++ {
		if keys[i] < keys[i-1] {
			t.Fatalf("LabelKeys() not sorted: %v", keys)
		}
	}
	if len(keys) != 4 {
		t.Fatalf("LabelKeys() has %d keys, want 4", len(keys))
	}
}

func TestLabelIndex_StreamsForTemplate(t *testing.T) {
	streams, chunks, templates, idMap := labelIndexFixtures()
	li := NewLabelIndex(streams, chunks, templates, idMap)

	// tmpl-err is used by chunk-1 (stream-a only).
	bm := li.StreamsForTemplate("tmpl-err")
	ids := li.ResolveStreamIDs(bm)
	if len(ids) != 1 || ids[0] != "stream-a" {
		t.Fatalf("StreamsForTemplate('tmpl-err') = %v, want [stream-a]", ids)
	}

	// tmpl-warn is used by chunk-2 (stream-a) and chunk-4 (stream-c).
	bm = li.StreamsForTemplate("tmpl-warn")
	ids = li.ResolveStreamIDs(bm)
	sort.Strings(ids)
	if len(ids) != 2 || ids[0] != "stream-a" || ids[1] != "stream-c" {
		t.Fatalf("StreamsForTemplate('tmpl-warn') = %v, want [stream-a stream-c]", ids)
	}

	// Unknown template returns empty bitmap.
	bm = li.StreamsForTemplate("unknown-tmpl")
	if bm.GetCardinality() != 0 {
		t.Fatalf("StreamsForTemplate('unknown-tmpl') cardinality = %d, want 0", bm.GetCardinality())
	}
}

func TestLabelIndex_ResolveStreamIDs(t *testing.T) {
	streams, chunks, templates, idMap := labelIndexFixtures()
	li := NewLabelIndex(streams, chunks, templates, idMap)

	// Round-trip: match all service=api, resolve, verify original IDs.
	bm := li.MatchLabels(map[string]string{"service": "api"})
	ids := li.ResolveStreamIDs(bm)
	sort.Strings(ids)
	expected := []string{"stream-a", "stream-b"}
	if len(ids) != len(expected) {
		t.Fatalf("ResolveStreamIDs = %v, want %v", ids, expected)
	}
	for i, id := range ids {
		if id != expected[i] {
			t.Fatalf("ResolveStreamIDs[%d] = %q, want %q", i, id, expected[i])
		}
	}

	// Nil bitmap returns nil.
	if ids := li.ResolveStreamIDs(nil); ids != nil {
		t.Fatalf("ResolveStreamIDs(nil) = %v, want nil", ids)
	}
}

func TestLabelIndex_EmptyIndex(t *testing.T) {
	idMap := NewStreamIDMap()
	li := NewLabelIndex(nil, nil, nil, idMap)

	if keys := li.LabelKeys(); len(keys) != 0 {
		t.Fatalf("empty index LabelKeys() = %v, want empty", keys)
	}
	if vals := li.LabelValues("anything"); vals != nil {
		t.Fatalf("empty index LabelValues = %v, want nil", vals)
	}
	bm := li.MatchLabels(map[string]string{"k": "v"})
	if bm.GetCardinality() != 0 {
		t.Fatalf("empty index MatchLabels cardinality = %d, want 0", bm.GetCardinality())
	}
	bm = li.SeverityRange(wirelogs.SeverityWarn)
	if bm.GetCardinality() != 0 {
		t.Fatalf("empty index SeverityRange cardinality = %d, want 0", bm.GetCardinality())
	}
	bm = li.StreamsForTemplate("tmpl")
	if bm.GetCardinality() != 0 {
		t.Fatalf("empty index StreamsForTemplate cardinality = %d, want 0", bm.GetCardinality())
	}
}

func TestLabelIndex_MatchLabelsEmpty(t *testing.T) {
	streams, chunks, templates, idMap := labelIndexFixtures()
	li := NewLabelIndex(streams, chunks, templates, idMap)

	// Empty filter map returns empty bitmap.
	bm := li.MatchLabels(map[string]string{})
	if bm.GetCardinality() != 0 {
		t.Fatalf("MatchLabels({}) cardinality = %d, want 0", bm.GetCardinality())
	}
}

func TestLabelIndex_HighCardLabelsIndexed(t *testing.T) {
	streams, chunks, templates, idMap := labelIndexFixtures()
	li := NewLabelIndex(streams, chunks, templates, idMap)

	// HighCardLabels (pod) should be indexed too.
	bm := li.MatchLabels(map[string]string{"pod": "api-pod-1"})
	ids := li.ResolveStreamIDs(bm)
	if len(ids) != 1 || ids[0] != "stream-a" {
		t.Fatalf("MatchLabels(pod=api-pod-1) = %v, want [stream-a]", ids)
	}
}
