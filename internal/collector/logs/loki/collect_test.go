// SPDX-License-Identifier: Apache-2.0

package loki

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// lokiPageServer simulates a Loki /loki/api/v1/query_range endpoint that
// returns entries from an internal store, respecting the start/end/limit
// query-range parameters and the direction=backward contract.
//
// Entries are in-memory with nanosecond timestamps. The handler returns
// up to `limit` entries whose timestamps satisfy start < ts <= end
// (Loki semantics: start is exclusive, end is inclusive when direction
// is backward), sorted newest-first. This is the minimal model needed
// to exercise our time-narrowing pagination.
type lokiPageServer struct {
	entries  []lokiStoredEntry
	requests int
}

type lokiStoredEntry struct {
	ns     int64
	line   string
	stream map[string]string
}

func (s *lokiPageServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.requests++
		q := r.URL.Query()
		start := parseNsDefault(q.Get("start"), 0)
		end := parseNsDefault(q.Get("end"), time.Now().Add(24*time.Hour).UnixNano())
		limit, _ := strconv.Atoi(q.Get("limit"))
		if limit <= 0 {
			limit = 100
		}

		// Collect matching entries newest-first.
		var matched []lokiStoredEntry
		for _, e := range s.entries {
			if start > 0 && e.ns <= start {
				continue
			}
			if e.ns > end {
				continue
			}
			matched = append(matched, e)
		}
		// Sort descending by ns.
		for i := 0; i < len(matched); i++ {
			for j := i + 1; j < len(matched); j++ {
				if matched[j].ns > matched[i].ns {
					matched[i], matched[j] = matched[j], matched[i]
				}
			}
		}
		if len(matched) > limit {
			matched = matched[:limit]
		}

		// Build response grouped by stream signature.
		buckets := map[string]*lokiStream{}
		order := []string{}
		for _, e := range matched {
			key := streamKey(e.stream)
			if _, ok := buckets[key]; !ok {
				s := &lokiStream{Stream: cloneLabels(e.stream)}
				buckets[key] = s
				order = append(order, key)
			}
			buckets[key].Values = append(buckets[key].Values, []string{
				strconv.FormatInt(e.ns, 10),
				e.line,
			})
		}
		resp := lokiQueryResponse{
			Status: "success",
			Data: lokiQueryData{
				ResultType: "streams",
				Result:     make([]lokiStream, 0, len(order)),
			},
		}
		for _, k := range order {
			resp.Data.Result = append(resp.Data.Result, *buckets[k])
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}
}

func parseNsDefault(raw string, fallback int64) int64 {
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return fallback
	}
	return v
}

func streamKey(labels map[string]string) string {
	var parts []string
	for k, v := range labels {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, ",")
}

func cloneLabels(src map[string]string) map[string]string {
	out := make(map[string]string, len(src))
	maps.Copy(out, src)
	return out
}

// makeLokiEntries produces n entries with sequential nanosecond timestamps
// starting at baseNs and incrementing by 1ms per entry.
func makeLokiEntries(n int, baseNs int64) []lokiStoredEntry {
	out := make([]lokiStoredEntry, 0, n)
	for i := range n {
		out = append(out, lokiStoredEntry{
			ns:     baseNs + int64(i)*int64(time.Millisecond),
			line:   fmt.Sprintf("message %d", i),
			stream: map[string]string{"namespace": "prod", "job": "api"},
		})
	}
	return out
}

func newLokiTestProvider(t *testing.T, server *httptest.Server) *lokiProvider {
	t.Helper()
	p := &lokiProvider{}
	err := p.Configure(map[string]string{"url": server.URL})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	return p
}

// TestCollect_UnboundedPagesAcrossTimeNarrowing verifies MaxEntries=0 drains
// more than lokiPageLimit entries by walking backward in time. This is the
// core behavior that used to be capped at 5000.
func TestCollect_UnboundedPagesAcrossTimeNarrowing(t *testing.T) {
	// Generate 3 full pages worth of entries (far exceeds the old 5000 default).
	total := lokiPageLimit*2 + 1234
	entries := makeLokiEntries(total, time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC).UnixNano())
	svr := &lokiPageServer{entries: entries}
	ts := httptest.NewServer(svr.handler())
	defer ts.Close()

	p := newLokiTestProvider(t, ts)

	got := 0
	emit := func(batch []logwire.LogEntry) error {
		got += len(batch)
		return nil
	}
	err := p.Collect(t.Context(), logwire.Query{
		Source:     "prod",
		MaxEntries: 0,
	}, emit)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if got != total {
		t.Errorf("collected %d entries, want %d (unbounded)", got, total)
	}
	// Should have taken at least 3 pages.
	if svr.requests < 3 {
		t.Errorf("server requests = %d, want >= 3", svr.requests)
	}
}

// TestCollect_BoundedStopsAtCap verifies MaxEntries > 0 still truncates.
func TestCollect_BoundedStopsAtCap(t *testing.T) {
	total := lokiPageLimit + 200
	entries := makeLokiEntries(total, time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC).UnixNano())
	svr := &lokiPageServer{entries: entries}
	ts := httptest.NewServer(svr.handler())
	defer ts.Close()

	p := newLokiTestProvider(t, ts)

	got := 0
	emit := func(batch []logwire.LogEntry) error {
		got += len(batch)
		return nil
	}
	err := p.Collect(t.Context(), logwire.Query{
		Source:     "prod",
		MaxEntries: 300,
	}, emit)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if got != 300 {
		t.Errorf("collected %d entries, want 300 (bounded)", got)
	}
	// Requests cap at 1 because 300 < lokiPageLimit — one page covers it.
	if svr.requests != 1 {
		t.Errorf("server requests = %d, want 1", svr.requests)
	}
}

// TestCollect_EmptyResponse returns no entries.
func TestCollect_EmptyResponse(t *testing.T) {
	svr := &lokiPageServer{entries: nil}
	ts := httptest.NewServer(svr.handler())
	defer ts.Close()

	p := newLokiTestProvider(t, ts)

	called := false
	emit := func(batch []logwire.LogEntry) error {
		called = true
		return nil
	}
	err := p.Collect(t.Context(), logwire.Query{Source: "prod", MaxEntries: 0}, emit)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if called {
		t.Error("emit should not be called for empty response")
	}
}

// TestCollect_TimeRangeNarrowing verifies the narrowing logic: if the first
// page returns exactly lokiPageLimit entries, a second call is issued with
// end = oldest_ns - 1.
func TestCollect_TimeRangeNarrowing(t *testing.T) {
	// Two partial pages: first returns lokiPageLimit (triggers narrowing),
	// second returns a tail smaller than the limit (terminates).
	total := lokiPageLimit + 50
	entries := makeLokiEntries(total, time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC).UnixNano())
	svr := &lokiPageServer{entries: entries}
	ts := httptest.NewServer(svr.handler())
	defer ts.Close()

	p := newLokiTestProvider(t, ts)

	got := 0
	emit := func(batch []logwire.LogEntry) error {
		got += len(batch)
		return nil
	}
	err := p.Collect(t.Context(), logwire.Query{Source: "prod", MaxEntries: 0}, emit)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if got != total {
		t.Errorf("collected %d, want %d", got, total)
	}
	if svr.requests != 2 {
		t.Errorf("server requests = %d, want 2 (first full page + tail)", svr.requests)
	}
}
