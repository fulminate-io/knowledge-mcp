// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/logs"
	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// integrationQueryID returns a unique queryID for this run. The pipeline is a
// pure transform (CollectFromEntries → CollectResult), so no store engine is
// needed — the previously-discarded store.DB return value is gone.
func integrationQueryID() string {
	return fmt.Sprintf("query-k8s-integration-%d", time.Now().UnixNano())
}

// buildIntegrationEvents returns ~30 events across three reasons and two
// namespaces. Reason lands in the message prefix, so Drain MUST produce
// one template per reason (three total) even though Drain ignores label
// differences.
func buildIntegrationEvents(base time.Time) []*eventsv1.Event {
	events := make([]*eventsv1.Event, 0, 30)

	// 10 OOMKilled Warning events on Pods in "default".
	for i := range 10 {
		events = append(events, makeIntegrationEvent(
			fmt.Sprintf("oom-%02d", i),
			"default",
			"OOMKilled", "memory cgroup out of memory", "Warning",
			fmt.Sprintf("api-%02d", i),
			base.Add(time.Duration(i)*time.Second),
		))
	}
	// 10 FailedScheduling Warning events on Pods in "kube-system".
	for i := range 10 {
		events = append(events, makeIntegrationEvent(
			fmt.Sprintf("sched-%02d", i),
			"kube-system",
			"FailedScheduling", "no nodes available", "Warning",
			fmt.Sprintf("coredns-%02d", i),
			base.Add(time.Duration(10+i)*time.Second),
		))
	}
	// 10 Scheduled Normal events — 5 in default, 5 in kube-system.
	for i := range 10 {
		ns := "default"
		name := fmt.Sprintf("api-%02d", i)
		if i >= 5 {
			ns = "kube-system"
			name = fmt.Sprintf("coredns-%02d", i)
		}
		events = append(events, makeIntegrationEvent(
			fmt.Sprintf("sched-ok-%02d", i),
			ns,
			"Scheduled", "successfully assigned", "Normal",
			name,
			base.Add(time.Duration(20+i)*time.Second),
		))
	}
	return events
}

func makeIntegrationEvent(name, ns, reason, note, evType, podName string,
	at time.Time) *eventsv1.Event {
	return &eventsv1.Event{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Reason:     reason,
		Note:       note,
		Type:       evType,
		EventTime:  metav1.MicroTime{Time: at},
		Regarding: corev1.ObjectReference{
			Kind:      "Pod",
			Namespace: ns,
			Name:      podName,
		},
		ReportingController: "kubelet",
	}
}

func TestK8sProvider_IntegrationWithPipeline(t *testing.T) {
	queryID := integrationQueryID()

	base := time.Date(2026, 4, 14, 10, 0, 0, 0, time.UTC)
	events := buildIntegrationEvents(base)

	cs := fake.NewSimpleClientset(makeRuntimeEvents(events)...)
	provider := &k8sProvider{}
	provider.setClientset(cs)

	pipeline := logs.NewPipeline(provider, queryID)

	ctx := context.Background()
	q := logwire.Query{}
	entries, err := logs.CollectEntries(ctx, provider, q)
	require.NoError(t, err)
	result, err := pipeline.CollectFromEntries(ctx, logs.ReclassifySeverity(entries), q)
	require.NoError(t, err)
	require.NotNil(t, result)

	// All 30 events emit one entry each (no series events in fixture).
	assert.Equal(t, 30, result.TotalEntries,
		"TotalEntries should match event count (1-per non-series)")

	// Drain clusters by Reason prefix → at least three templates.
	assert.GreaterOrEqual(t, len(result.Templates), 3,
		"expected >=3 templates (one per reason), got %d", len(result.Templates))

	// Validate severity classifications are preserved through clustering.
	byReason := map[string]*logwire.LogTemplate{}
	for _, tpl := range result.Templates {
		switch {
		case containsWord(tpl.Pattern, "OOMKilled"):
			byReason["OOMKilled"] = tpl
		case containsWord(tpl.Pattern, "FailedScheduling"):
			byReason["FailedScheduling"] = tpl
		case containsWord(tpl.Pattern, "Scheduled"):
			byReason["Scheduled"] = tpl
		}
	}
	require.Contains(t, byReason, "OOMKilled")
	require.Contains(t, byReason, "FailedScheduling")
	require.Contains(t, byReason, "Scheduled")

	assert.Equal(t, logwire.SeverityError, byReason["OOMKilled"].Severity,
		"Warning events must cluster as ERROR")
	assert.Equal(t, logwire.SeverityError, byReason["FailedScheduling"].Severity)
	assert.Equal(t, logwire.SeverityInfo, byReason["Scheduled"].Severity,
		"Normal events must cluster as INFO")

	// Streams should exist — one per unique (namespace, service) label pair.
	assert.NotEmpty(t, result.Streams, "expected streams from pipeline")

	// Time range must bracket the synthetic events.
	assert.False(t, result.TimeRange.Start.IsZero())
	assert.False(t, result.TimeRange.End.IsZero())
	assert.False(t, result.TimeRange.Start.After(base),
		"Start should be <= earliest event time")
	assert.False(t, result.TimeRange.End.Before(base.Add(29*time.Second)),
		"End should be >= latest event time")
}

// containsWord is a trivial helper that avoids importing strings for a
// one-liner in a single test. Case-sensitive on purpose — reason tokens
// are exact casing.
func containsWord(s, word string) bool {
	for i := 0; i+len(word) <= len(s); i++ {
		if s[i:i+len(word)] == word {
			return true
		}
	}
	return false
}
