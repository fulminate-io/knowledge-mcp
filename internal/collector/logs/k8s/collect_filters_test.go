// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	eventsv1 "k8s.io/api/events/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

func TestCollect_TimeRangeClientSideFilter(t *testing.T) {
	base := time.Date(2026, 4, 14, 10, 0, 0, 0, time.UTC)
	events := make([]*eventsv1.Event, 0, 10)
	for i := range 10 {
		events = append(events, makeEvent(
			fmt.Sprintf("e%02d", i),
			"Started", "ok", "Normal",
			base.Add(time.Duration(i)*time.Minute),
		))
	}
	cs := fake.NewSimpleClientset(makeRuntimeEvents(events)...)

	p := &k8sProvider{}
	p.setClientset(cs)

	// Window [base+2m, base+5m] inclusive → minutes 2,3,4,5 → 4 entries.
	start := base.Add(2 * time.Minute)
	end := base.Add(5 * time.Minute)
	got := collectAll(t, p, logwire.Query{StartTime: start, EndTime: end})
	assert.Len(t, got, 4, "only in-range entries should survive client filter")
	for _, e := range got {
		assert.False(t, e.Timestamp.Before(start))
		assert.False(t, e.Timestamp.After(end))
	}
}

func TestCollect_TextFilterAppliesAfterNormalize(t *testing.T) {
	base := time.Date(2026, 4, 14, 10, 0, 0, 0, time.UTC)
	cs := fake.NewSimpleClientset(
		makeEvent("e1", "OOMKilled", "memory exceeded", "Warning", base),
		makeEvent("e2", "Started", "ok", "Normal", base.Add(time.Second)),
		makeEvent("e3", "OOMKilled", "cgroup oom", "Warning", base.Add(2*time.Second)),
		makeEvent("e4", "FailedMount", "volume timeout", "Warning", base.Add(3*time.Second)),
	)
	p := &k8sProvider{}
	p.setClientset(cs)

	got := collectAll(t, p, logwire.Query{TextFilter: "oom"})
	require.Len(t, got, 2)
	for _, e := range got {
		assert.Contains(t, strings.ToLower(e.Message), "oom")
	}
}

func TestCollect_SeverityMinAboveWarn_ServerFilterApplied(t *testing.T) {
	// Snag the ListOptions fed to the fake. We return false (handled=false)
	// so the tracker still serves default results.
	base := time.Date(2026, 4, 14, 10, 0, 0, 0, time.UTC)
	warningEvt := makeEvent("e1", "OOMKilled", "oom", "Warning", base)
	normalEvt := makeEvent("e2", "Started", "ok", "Normal", base.Add(time.Second))

	cs := fake.NewSimpleClientset(warningEvt, normalEvt)
	p := &k8sProvider{}
	p.setClientset(cs)

	var observedFieldSelector string
	cs.PrependReactor("list", "events",
		func(action k8stesting.Action) (bool, runtime.Object, error) {
			la, ok := action.(k8stesting.ListAction)
			if ok {
				observedFieldSelector = la.GetListRestrictions().Fields.String()
			}
			return false, nil, nil
		})

	_ = collectAll(t, p, logwire.Query{SeverityMin: logwire.SeverityError})
	assert.Contains(t, observedFieldSelector, "type=Warning",
		"server-side FieldSelector should include type=Warning for SeverityMin>=WARN")
}

func TestCollect_SeverityMinInfo_NoServerFilter(t *testing.T) {
	base := time.Date(2026, 4, 14, 10, 0, 0, 0, time.UTC)
	cs := fake.NewSimpleClientset(
		makeEvent("e1", "OOMKilled", "oom", "Warning", base),
	)
	p := &k8sProvider{}
	p.setClientset(cs)

	var observed string
	cs.PrependReactor("list", "events",
		func(action k8stesting.Action) (bool, runtime.Object, error) {
			if la, ok := action.(k8stesting.ListAction); ok {
				observed = la.GetListRestrictions().Fields.String()
			}
			return false, nil, nil
		})

	_ = collectAll(t, p, logwire.Query{SeverityMin: logwire.SeverityInfo})
	assert.NotContains(t, observed, "type=",
		"SeverityMin=INFO should not push down a server-side type selector")
}

func TestCollect_FieldSelectorRejectionRetries_FallsBackToClientSide(t *testing.T) {
	// Simulate an apiserver that rejects our FieldSelector on the first
	// call. The second call (no FieldSelector) must succeed and return
	// events normally; drainEvents logic applies client-side filters.
	base := time.Date(2026, 4, 14, 10, 0, 0, 0, time.UTC)
	warningEvt := makeEvent("e1", "OOMKilled", "oom", "Warning", base)
	normalEvt := makeEvent("e2", "Started", "ok", "Normal", base.Add(time.Second))

	cs := fake.NewSimpleClientset(warningEvt, normalEvt)
	p := &k8sProvider{}
	p.setClientset(cs)

	calls := 0
	var firstFieldSelector, secondFieldSelector string
	cs.PrependReactor("list", "events",
		func(action k8stesting.Action) (bool, runtime.Object, error) {
			calls++
			la, _ := action.(k8stesting.ListAction)
			fs := ""
			if la != nil {
				fs = la.GetListRestrictions().Fields.String()
			}
			if calls == 1 {
				firstFieldSelector = fs
				// Apiserver rejects the field selector.
				return true, nil, errors.New(
					`fieldSelector "type=Warning" is not supported`)
			}
			secondFieldSelector = fs
			return false, nil, nil // delegate to tracker
		})

	// Request Warn+ so the provider emits type=Warning on the wire.
	got := collectAll(t, p, logwire.Query{SeverityMin: logwire.SeverityWarn})

	assert.Contains(t, firstFieldSelector, "type=Warning",
		"first call should include FieldSelector")
	assert.NotContains(t, secondFieldSelector, "type=",
		"graceful-degrade retry must drop FieldSelector")

	// Client-side severity filter still drops Normal events, so we expect
	// exactly one entry from the Warning event.
	require.Len(t, got, 1)
	assert.Equal(t, "OOMKilled: oom", got[0].Message)
}

func TestCollect_FieldSelectorRejection_SubsequentPagesAlsoDegrade(t *testing.T) {
	// First call: rejected. Retry (no FieldSelector): returns 2 events and
	// a Continue token. Third call (next page, still no FieldSelector):
	// returns 1 event and empty Continue. Total: 3 entries.
	base := time.Date(2026, 4, 14, 10, 0, 0, 0, time.UTC)
	p := &k8sProvider{}
	cs := fake.NewSimpleClientset()
	p.setClientset(cs)

	makeList := func(cont string, evs ...*eventsv1.Event) *eventsv1.EventList {
		list := &eventsv1.EventList{}
		list.Continue = cont
		for _, e := range evs {
			list.Items = append(list.Items, *e)
		}
		return list
	}
	page1 := makeList("tok",
		makeEvent("e1", "OOMKilled", "oom", "Warning", base),
		makeEvent("e2", "BackOff", "crash", "Warning", base.Add(time.Second)),
	)
	page2 := makeList("",
		makeEvent("e3", "Evicted", "mem pressure", "Warning", base.Add(2*time.Second)),
	)

	calls := 0
	cs.PrependReactor("list", "events",
		func(action k8stesting.Action) (bool, runtime.Object, error) {
			calls++
			la, _ := action.(k8stesting.ListAction)
			fs := ""
			if la != nil {
				fs = la.GetListRestrictions().Fields.String()
			}
			switch calls {
			case 1:
				return true, nil, errors.New(`invalid field selector: ` + fs)
			case 2:
				assert.NotContains(t, fs, "type=",
					"degraded retry must drop FieldSelector")
				return true, page1, nil
			case 3:
				assert.NotContains(t, fs, "type=",
					"subsequent pages must keep FieldSelector dropped")
				return true, page2, nil
			}
			return false, nil, nil
		})

	got := collectAll(t, p, logwire.Query{SeverityMin: logwire.SeverityWarn})
	assert.Equal(t, 3, calls)
	require.Len(t, got, 3)
}

func TestCollect_FieldSelectorRejection_NonSelectorErrorPropagates(t *testing.T) {
	// Errors that don't match isFieldSelectorRejection must bubble up.
	cs := fake.NewSimpleClientset()
	p := &k8sProvider{}
	p.setClientset(cs)

	cs.PrependReactor("list", "events",
		func(action k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("connection refused")
		})

	err := p.Collect(context.Background(), logwire.Query{SeverityMin: logwire.SeverityWarn},
		func([]logwire.LogEntry) error { return nil })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection refused")
}
