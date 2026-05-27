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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// makeEvent constructs an events.k8s.io/v1 Event shaped like one emitted by
// the kubelet for a Pod-scoped failure. Tests mutate the returned value
// before handing it to fake.NewSimpleClientset.
func makeEvent(name, reason, note, evType string, at time.Time) *eventsv1.Event {
	return &eventsv1.Event{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "prod"},
		Reason:     reason,
		Note:       note,
		Type:       evType,
		EventTime:  metav1.MicroTime{Time: at},
		Regarding: corev1.ObjectReference{
			Kind:      "Pod",
			Namespace: "prod",
			Name:      "api-" + name,
		},
		ReportingController: "kubelet",
	}
}

// makeRuntimeEvents materializes []*eventsv1.Event as runtime.Object
// slice suitable for fake.NewSimpleClientset variadic arg.
func makeRuntimeEvents(events []*eventsv1.Event) []runtime.Object {
	out := make([]runtime.Object, len(events))
	for i, e := range events {
		out[i] = e
	}
	return out
}

// collectAll drives p.Collect with an emit callback that accumulates every
// emitted entry. Ignores batching boundaries — tests that want to see batch
// counts use collectBatched.
func collectAll(t *testing.T, p *k8sProvider, q logwire.Query) []logwire.LogEntry {
	t.Helper()
	var got []logwire.LogEntry
	err := p.Collect(context.Background(), q, func(batch []logwire.LogEntry) error {
		got = append(got, batch...)
		return nil
	})
	require.NoError(t, err)
	return got
}

func TestCollect_BasicList_EmitsOneEntryPerNonSeriesEvent(t *testing.T) {
	now := time.Date(2026, 4, 14, 10, 0, 0, 0, time.UTC)
	events := []*eventsv1.Event{
		makeEvent("e1", "OOMKilled", "out of memory", "Warning", now),
		makeEvent("e2", "Started", "container started", "Normal", now.Add(time.Second)),
		makeEvent("e3", "BackOff", "CrashLoopBackOff", "Warning", now.Add(2*time.Second)),
	}
	cs := fake.NewSimpleClientset(makeRuntimeEvents(events)...)

	p := &k8sProvider{}
	p.setClientset(cs)

	got := collectAll(t, p, logwire.Query{})
	require.Len(t, got, 3)
}

func TestCollect_SeriesEventEmitsTwoEntries(t *testing.T) {
	first := time.Date(2026, 4, 14, 10, 0, 0, 0, time.UTC)
	last := first.Add(5 * time.Minute)
	e := makeEvent("e1", "BackOff", "CrashLoopBackOff", "Warning", first)
	e.Series = &eventsv1.EventSeries{
		Count:            42,
		LastObservedTime: metav1.MicroTime{Time: last},
	}
	cs := fake.NewSimpleClientset(e)

	p := &k8sProvider{}
	p.setClientset(cs)

	got := collectAll(t, p, logwire.Query{})
	require.Len(t, got, 2, "series event should produce two log entries")
	assert.Equal(t, "42", got[0].Labels["count"])
	assert.Equal(t, "42", got[1].Labels["count"])
}

func TestCollect_MaxEntriesZeroIsUnbounded(t *testing.T) {
	// Well above the old 500 default cap to prove the unbounded path.
	const n = 600
	base := time.Date(2026, 4, 14, 10, 0, 0, 0, time.UTC)
	events := make([]*eventsv1.Event, 0, n)
	for i := range n {
		events = append(events, makeEvent(
			fmt.Sprintf("e%04d", i),
			"Started", "container started", "Normal",
			base.Add(time.Duration(i)*time.Millisecond),
		))
	}
	cs := fake.NewSimpleClientset(makeRuntimeEvents(events)...)

	p := &k8sProvider{}
	p.setClientset(cs)

	got := collectAll(t, p, logwire.Query{MaxEntries: 0})
	assert.Len(t, got, n, "MaxEntries=0 should collect all events unbounded")
}

func TestCollect_MaxEntriesNegativeIsUnbounded(t *testing.T) {
	const n = 550
	base := time.Date(2026, 4, 14, 10, 0, 0, 0, time.UTC)
	events := make([]*eventsv1.Event, 0, n)
	for i := range n {
		events = append(events, makeEvent(
			fmt.Sprintf("e%04d", i),
			"Started", "ok", "Normal",
			base.Add(time.Duration(i)*time.Millisecond),
		))
	}
	cs := fake.NewSimpleClientset(makeRuntimeEvents(events)...)

	p := &k8sProvider{}
	p.setClientset(cs)

	got := collectAll(t, p, logwire.Query{MaxEntries: -1})
	assert.Len(t, got, n)
}

func TestCollect_MaxEntriesBoundedStopsAtCap(t *testing.T) {
	const n = 600
	base := time.Date(2026, 4, 14, 10, 0, 0, 0, time.UTC)
	events := make([]*eventsv1.Event, 0, n)
	for i := range n {
		events = append(events, makeEvent(
			fmt.Sprintf("e%04d", i),
			"Started", "ok", "Normal",
			base.Add(time.Duration(i)*time.Millisecond),
		))
	}
	cs := fake.NewSimpleClientset(makeRuntimeEvents(events)...)

	p := &k8sProvider{}
	p.setClientset(cs)

	got := collectAll(t, p, logwire.Query{MaxEntries: 100})
	assert.Len(t, got, 100, "MaxEntries=100 should cap exactly at 100")
}

func TestCollect_MaxEntriesCapCountsNormalizedEntriesNotEvents(t *testing.T) {
	// 100 series events → 200 normalized entries. MaxEntries=150 should cap
	// at 150 normalized entries (not 100 events, which would be 200 entries).
	const n = 100
	base := time.Date(2026, 4, 14, 10, 0, 0, 0, time.UTC)
	events := make([]*eventsv1.Event, 0, n)
	for i := range n {
		e := makeEvent(
			fmt.Sprintf("e%04d", i),
			"BackOff", "CrashLoopBackOff", "Warning",
			base.Add(time.Duration(i)*time.Second),
		)
		e.Series = &eventsv1.EventSeries{
			Count:            5,
			LastObservedTime: metav1.MicroTime{Time: e.EventTime.Add(time.Minute)},
		}
		events = append(events, e)
	}
	cs := fake.NewSimpleClientset(makeRuntimeEvents(events)...)

	p := &k8sProvider{}
	p.setClientset(cs)

	got := collectAll(t, p, logwire.Query{MaxEntries: 150})
	assert.Len(t, got, 150,
		"cap should count emitted LogEntry values, not Event objects")
}

func TestCollect_MultiPagePaginationViaContinueToken(t *testing.T) {
	// The fake tracker doesn't paginate natively, so we simulate two
	// pages by PrependReactor. Page 1 returns 200 events with Continue
	// set; page 2 returns 200 more with no Continue; total 400.
	p := &k8sProvider{}
	cs := fake.NewSimpleClientset()
	p.setClientset(cs)

	const perPage = 200
	base := time.Date(2026, 4, 14, 10, 0, 0, 0, time.UTC)
	makePage := func(start, count int, cont string) *eventsv1.EventList {
		list := &eventsv1.EventList{}
		list.Continue = cont
		for i := range count {
			e := makeEvent(
				fmt.Sprintf("e%04d", start+i),
				"Started", "ok", "Normal",
				base.Add(time.Duration(start+i)*time.Millisecond),
			)
			list.Items = append(list.Items, *e)
		}
		return list
	}
	page1 := makePage(0, perPage, "tok-page2")
	page2 := makePage(perPage, perPage, "")

	calls := 0
	cs.PrependReactor("list", "events",
		func(action k8stesting.Action) (bool, runtime.Object, error) {
			calls++
			listAction, ok := action.(k8stesting.ListAction)
			if !ok {
				return false, nil, nil
			}
			if calls == 1 {
				if listAction.GetListRestrictions().Labels.Empty() {
					// Empty LabelSelector is expected; no extra checks.
					_ = listAction
				}
				return true, page1, nil
			}
			return true, page2, nil
		})

	got := collectAll(t, p, logwire.Query{})
	assert.Equal(t, 2, calls, "expected two List pages")
	assert.Len(t, got, 2*perPage, "both pages concatenated")
}

func TestCollect_RespectsContextCancellation(t *testing.T) {
	cs := fake.NewSimpleClientset(makeEvent(
		"e1", "Started", "ok", "Normal",
		time.Date(2026, 4, 14, 10, 0, 0, 0, time.UTC),
	))
	p := &k8sProvider{}
	p.setClientset(cs)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before calling

	err := p.Collect(ctx, logwire.Query{}, func([]logwire.LogEntry) error { return nil })
	require.Error(t, err, "Collect should surface cancelled context")
	assert.ErrorIs(t, err, context.Canceled)
}
