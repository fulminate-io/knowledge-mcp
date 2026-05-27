// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// microTime is a tiny helper for constructing a non-zero metav1.MicroTime
// from a time.Time. Used in place of literal struct construction to keep
// call sites readable.
func microTime(t time.Time) metav1.MicroTime { return metav1.MicroTime{Time: t} }

// baseEvent returns a minimal Warning Event fixture at the supplied
// EventTime. Tests mutate the returned value before asserting on
// normalizeEvent output.
func baseEvent(at time.Time) *eventsv1.Event {
	return &eventsv1.Event{
		ObjectMeta: metav1.ObjectMeta{Name: "evt", Namespace: "prod"},
		Reason:     "OOMKilled",
		Note:       "Memory cgroup out of memory",
		Type:       "Warning",
		EventTime:  microTime(at),
		Regarding: corev1.ObjectReference{
			Kind:      "Pod",
			Namespace: "prod",
			Name:      "api-x",
		},
		ReportingController: "kubelet",
		ReportingInstance:   "node-1",
	}
}

func TestNormalizeEvent_NonSeries_SingleEntryAtEventTime(t *testing.T) {
	ts := time.Date(2026, 4, 14, 10, 30, 0, 0, time.UTC)
	e := baseEvent(ts)
	entries := normalizeEvent(e, "")
	require.Len(t, entries, 1, "non-series event should emit one entry")
	assert.True(t, entries[0].Timestamp.Equal(ts), "timestamp mismatch: %v != %v",
		entries[0].Timestamp, ts)
}

func TestNormalizeEvent_Series_TwoEntries_FirstAndLastObservedTime(t *testing.T) {
	first := time.Date(2026, 4, 14, 10, 0, 0, 0, time.UTC)
	last := first.Add(5 * time.Minute)
	e := baseEvent(first)
	e.Series = &eventsv1.EventSeries{
		Count:            50,
		LastObservedTime: microTime(last),
	}
	entries := normalizeEvent(e, "")
	require.Len(t, entries, 2, "series event should emit two entries")
	assert.True(t, entries[0].Timestamp.Equal(first))
	assert.True(t, entries[1].Timestamp.Equal(last))
	// Same message — Drain must cluster them into the same template.
	assert.Equal(t, entries[0].Message, entries[1].Message)
	// Both carry the count label.
	assert.Equal(t, "50", entries[0].Labels["count"])
	assert.Equal(t, "50", entries[1].Labels["count"])
	// Labels are distinct map instances so downstream mutation cannot
	// perturb the sibling entry.
	assert.NotSame(t, &entries[0].Labels, &entries[1].Labels)
}

func TestNormalizeEvent_WarningMapsToError(t *testing.T) {
	e := baseEvent(time.Now())
	e.Type = "Warning"
	entries := normalizeEvent(e, "")
	require.Len(t, entries, 1)
	assert.Equal(t, logwire.SeverityError, entries[0].Severity)
}

func TestNormalizeEvent_NormalMapsToInfo(t *testing.T) {
	e := baseEvent(time.Now())
	e.Type = "Normal"
	entries := normalizeEvent(e, "")
	require.Len(t, entries, 1)
	assert.Equal(t, logwire.SeverityInfo, entries[0].Severity)
}

func TestNormalizeEvent_UnknownTypeMapsToInfo(t *testing.T) {
	e := baseEvent(time.Now())
	e.Type = "" // API-shimmed events sometimes drop Type
	entries := normalizeEvent(e, "")
	require.Len(t, entries, 1)
	assert.Equal(t, logwire.SeverityInfo, entries[0].Severity)
}

func TestNormalizeEvent_MessageIsReasonColonNote(t *testing.T) {
	e := baseEvent(time.Now())
	e.Reason = "OOMKilled"
	e.Note = "Memory cgroup out of memory (UID 0)"
	entries := normalizeEvent(e, "")
	require.Len(t, entries, 1)
	assert.Equal(t, "OOMKilled: Memory cgroup out of memory (UID 0)", entries[0].Message)
}

func TestNormalizeEvent_EmptyReasonOmitsPrefix(t *testing.T) {
	e := baseEvent(time.Now())
	e.Reason = ""
	e.Note = "Some raw note"
	entries := normalizeEvent(e, "")
	require.Len(t, entries, 1)
	assert.Equal(t, "Some raw note", entries[0].Message)
}

func TestNormalizeEvent_EmptyNoteOmitsColon(t *testing.T) {
	e := baseEvent(time.Now())
	e.Reason = "Started"
	e.Note = ""
	entries := normalizeEvent(e, "")
	require.Len(t, entries, 1)
	assert.Equal(t, "Started", entries[0].Message)
}

func TestNormalizeEvent_BothEmpty(t *testing.T) {
	e := baseEvent(time.Now())
	e.Reason = ""
	e.Note = ""
	entries := normalizeEvent(e, "")
	require.Len(t, entries, 1)
	assert.Empty(t, entries[0].Message)
}

func TestNormalizeEvent_LabelsIncludeServiceNamespaceKind(t *testing.T) {
	e := baseEvent(time.Now())
	entries := normalizeEvent(e, "")
	require.Len(t, entries, 1)
	lbl := entries[0].Labels

	assert.Equal(t, "api-x", lbl["service"])
	assert.Equal(t, "prod", lbl["namespace"])
	assert.Equal(t, "Pod", lbl["kind"])
	assert.Equal(t, "api-x", lbl["pod_name"])
	assert.Equal(t, "OOMKilled", lbl["reason"])
	assert.Equal(t, "Warning", lbl["type"])
	assert.Equal(t, "kubelet", lbl["reporting_controller"])
	assert.Equal(t, "node-1", lbl["reporting_instance"])
}

func TestNormalizeEvent_ClusterScopedEventLabels(t *testing.T) {
	e := baseEvent(time.Now())
	e.Regarding = corev1.ObjectReference{Kind: "Node", Name: "node-1"}
	entries := normalizeEvent(e, "")
	require.Len(t, entries, 1)
	lbl := entries[0].Labels

	assert.Equal(t, "node-1", lbl["service"])
	assert.Equal(t, "Node", lbl["kind"])
	_, hasNS := lbl["namespace"]
	assert.False(t, hasNS, "cluster-scoped event must not carry namespace label")
	_, hasPod := lbl["pod_name"]
	assert.False(t, hasPod, "non-Pod event must not carry pod_name label")
}

func TestNormalizeEvent_RelatedObjectLabels(t *testing.T) {
	e := baseEvent(time.Now())
	e.Related = &corev1.ObjectReference{Kind: "ReplicaSet", Name: "api-rs-1"}
	entries := normalizeEvent(e, "")
	require.Len(t, entries, 1)
	lbl := entries[0].Labels

	assert.Equal(t, "ReplicaSet", lbl["related_kind"])
	assert.Equal(t, "api-rs-1", lbl["related_name"])
}

func TestNormalizeEvent_NoRelatedOmitsLabels(t *testing.T) {
	e := baseEvent(time.Now())
	e.Related = nil
	entries := normalizeEvent(e, "")
	require.Len(t, entries, 1)
	_, hasK := entries[0].Labels["related_kind"]
	_, hasN := entries[0].Labels["related_name"]
	assert.False(t, hasK)
	assert.False(t, hasN)
}

func TestNormalizeEvent_ReportingControllerInstanceLabels(t *testing.T) {
	e := baseEvent(time.Now())
	e.ReportingController = "kube-scheduler"
	e.ReportingInstance = "scheduler-abc"
	entries := normalizeEvent(e, "")
	require.Len(t, entries, 1)
	assert.Equal(t, "kube-scheduler", entries[0].Labels["reporting_controller"])
	assert.Equal(t, "scheduler-abc", entries[0].Labels["reporting_instance"])
}

func TestNormalizeEvent_EmptyLabelValuesSkipped(t *testing.T) {
	e := baseEvent(time.Now())
	e.ReportingController = ""
	e.ReportingInstance = ""
	e.Type = ""
	e.Regarding.Namespace = ""
	entries := normalizeEvent(e, "")
	require.Len(t, entries, 1)
	lbl := entries[0].Labels

	for _, k := range []string{
		"reporting_controller", "reporting_instance", "type", "namespace",
	} {
		_, present := lbl[k]
		assert.Falsef(t, present, "empty-valued label %q leaked into labels", k)
	}
}

func TestNormalizeEvent_TimestampFallback_LastTimestamp(t *testing.T) {
	last := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	e := &eventsv1.Event{
		ObjectMeta:              metav1.ObjectMeta{Name: "evt"},
		Reason:                  "Started",
		Type:                    "Normal",
		DeprecatedLastTimestamp: metav1.Time{Time: last},
	}
	entries := normalizeEvent(e, "")
	require.Len(t, entries, 1)
	assert.True(t, entries[0].Timestamp.Equal(last))
}

func TestNormalizeEvent_TimestampFallback_FirstTimestamp(t *testing.T) {
	first := time.Date(2025, 12, 31, 23, 59, 59, 0, time.UTC)
	e := &eventsv1.Event{
		ObjectMeta:               metav1.ObjectMeta{Name: "evt"},
		Reason:                   "Started",
		Type:                     "Normal",
		DeprecatedFirstTimestamp: metav1.Time{Time: first},
	}
	entries := normalizeEvent(e, "")
	require.Len(t, entries, 1)
	assert.True(t, entries[0].Timestamp.Equal(first))
}

func TestNormalizeEvent_TimestampFallback_NeverZero(t *testing.T) {
	e := &eventsv1.Event{
		ObjectMeta: metav1.ObjectMeta{Name: "evt"},
		Reason:     "Started",
		Type:       "Normal",
	}
	before := time.Now().Add(-time.Second)
	entries := normalizeEvent(e, "")
	require.Len(t, entries, 1)
	assert.False(t, entries[0].Timestamp.IsZero(), "fallback must not emit zero Timestamp")
	assert.False(t, entries[0].Timestamp.Before(before),
		"fallback Timestamp should be ~time.Now(): got %v", entries[0].Timestamp)
}

func TestNormalizeEvent_NilEventReturnsNil(t *testing.T) {
	assert.Nil(t, normalizeEvent(nil, ""))
}

func TestNormalizeEvent_GKEContextPopulatesLabels(t *testing.T) {
	e := baseEvent(time.Now())
	entries := normalizeEvent(e, "gke_myproj_us-central1_prod")
	require.Len(t, entries, 1)
	lbl := entries[0].Labels
	assert.Equal(t, "myproj", lbl["project_id"])
	assert.Equal(t, "prod", lbl["cluster_name"])
}

func TestNormalizeEvent_NonGKEContextOmitsProjectLabels(t *testing.T) {
	e := baseEvent(time.Now())
	entries := normalizeEvent(e, "arn:aws:eks:us-east-1:123:cluster/prod")
	require.Len(t, entries, 1)
	lbl := entries[0].Labels
	_, hasP := lbl["project_id"]
	_, hasC := lbl["cluster_name"]
	assert.False(t, hasP, "non-GKE context must not populate project_id")
	assert.False(t, hasC, "non-GKE context must not populate cluster_name")
}

func TestNormalizeEvent_SeriesZeroCountOmitsLabel(t *testing.T) {
	e := baseEvent(time.Now())
	e.Series = &eventsv1.EventSeries{Count: 0, LastObservedTime: microTime(time.Now())}
	entries := normalizeEvent(e, "")
	// Still two entries (LastObservedTime is set), but count label omitted.
	require.Len(t, entries, 2)
	_, hasCount := entries[0].Labels["count"]
	assert.False(t, hasCount)
}
