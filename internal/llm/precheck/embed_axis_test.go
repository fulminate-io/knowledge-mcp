// SPDX-License-Identifier: Apache-2.0

package precheck

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// embedPingServer answers one embeddings request with a 32-byte vector and
// records that it was hit, so a test can prove the ping reached THIS
// endpoint rather than a vendor default.
func embedPingServer(t *testing.T, hits *atomic.Int32, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		row := make([]int, 32)
		if err := json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": row}},
		}); err != nil {
			t.Errorf("encode embed response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestCheckEmbedProvider_PingsResolvedBaseURL is the check's whole point:
// the ping goes to the RESOLVED base_url, so an operator pointing
// [embedder] at a local compatible server gets a check of the service the
// client actually calls — not of a vendor endpoint it never touches.
//
// The unconfigured case is the known-positive control for the hit counter:
// it must leave the counter at ZERO, which is what proves a non-zero count
// elsewhere means a real request rather than an always-incrementing stub.
func TestCheckEmbedProvider_PingsResolvedBaseURL(t *testing.T) {
	ctx := context.Background()
	t.Setenv("VOYAGE_API_KEY", "")
	t.Cleanup(config.SetForTest(nil))

	t.Run("unconfigured axis calls nothing", func(t *testing.T) {
		var hits atomic.Int32
		srv := embedPingServer(t, &hits, http.StatusOK)
		_ = srv
		sec := config.EmbedSection{Provider: config.EmbedProviderVoyage, Dimension: 256, Dtype: "ubinary"}
		if err := CheckEmbedProvider(ctx, sec); err != nil {
			t.Fatalf("an axis with no credential and no base_url must opt out, not error: %v", err)
		}
		if got := hits.Load(); got != 0 {
			t.Errorf("the opt-out path issued %d requests; want 0 (zero startup spend is the lever)", got)
		}
	})

	t.Run("keyless base_url is pinged", func(t *testing.T) {
		var hits atomic.Int32
		srv := embedPingServer(t, &hits, http.StatusOK)
		sec := config.EmbedSection{
			Provider: config.EmbedProviderVoyage, BaseURL: srv.URL, Dimension: 256, Dtype: "ubinary",
		}
		if err := CheckEmbedProvider(ctx, sec); err != nil {
			t.Fatalf("CheckEmbedProvider against a live local endpoint: %v", err)
		}
		if got := hits.Load(); got != 1 {
			t.Errorf("the resolved base_url received %d requests; want exactly 1", got)
		}
	})

	t.Run("a rejecting endpoint is an error naming the class", func(t *testing.T) {
		var hits atomic.Int32
		srv := embedPingServer(t, &hits, http.StatusUnauthorized)
		sec := config.EmbedSection{
			Provider: config.EmbedProviderVoyage, BaseURL: srv.URL, Dimension: 256, Dtype: "ubinary",
		}
		err := CheckEmbedProvider(ctx, sec)
		if err == nil {
			t.Fatal("a 401 from the configured endpoint must be an error")
		}
		if !strings.Contains(err.Error(), "invalid, revoked, or out of credits") {
			t.Errorf("401 error %q does not name the key-rejection class", err)
		}
		if got := hits.Load(); got != 1 {
			t.Errorf("hits = %d; want 1", got)
		}
	})

	t.Run("a malformed section errors before any call", func(t *testing.T) {
		var hits atomic.Int32
		srv := embedPingServer(t, &hits, http.StatusOK)
		sec := config.EmbedSection{
			Provider: config.EmbedProviderVoyage, BaseURL: srv.URL, Dimension: 1024, Dtype: "float",
		}
		err := CheckEmbedProvider(ctx, sec)
		if err == nil {
			t.Fatal("an off-width section must error rather than ping")
		}
		if got := hits.Load(); got != 0 {
			t.Errorf("a refused config issued %d requests; want 0", got)
		}
	})
}

// TestRunAll_AxisChecksRunConcurrently proves BOTH axis checks run INSIDE
// the existing errgroup fan-out rather than serially before or after it —
// the LLM tuples and both axis checks in flight at once, so total
// wall-clock stays bounded by the slowest check.
//
// CONCURRENCY IS OBSERVED, not assumed. Both checks report entry and exit
// to a shared tracker that records the PEAK number in flight at once; a
// serial fan-out can never push that peak above 1. Two checks that merely
// both ran would satisfy a naive hit-count assertion even if RunAll called
// them one after the other, which is exactly what this criterion is about.
// The tracker's own serial-vs-concurrent discrimination is controlled by
// TestInFlightTracker_SerialControl below.
func TestRunAll_AxisChecksRunConcurrently(t *testing.T) {
	ctx := context.Background()
	t.Setenv("VOYAGE_API_KEY", "")
	t.Cleanup(config.SetForTest(nil))

	tr := newInFlightTracker()
	var embedHits, rerankHits atomic.Int32

	checkRerank := func(_ context.Context, _ config.RerankSection) error {
		rerankHits.Add(1)
		tr.enter()
		time.Sleep(30 * time.Millisecond)
		tr.exit()
		return nil
	}
	// The embed axis is tracked from inside its HTTP handler, which only
	// runs once CheckEmbedProvider is actually issuing its ping.
	embedSignal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		embedHits.Add(1)
		tr.enter()
		time.Sleep(30 * time.Millisecond)
		tr.exit()
		row := make([]int, 32)
		if err := json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": row}},
		}); err != nil {
			t.Errorf("encode embed response: %v", err)
		}
	}))
	t.Cleanup(embedSignal.Close)

	cfg := &config.Config{
		Embedder: &config.EmbedSection{
			Provider: config.EmbedProviderVoyage, BaseURL: embedSignal.URL, Dimension: 256, Dtype: "ubinary",
		},
		Reranker: &config.RerankSection{
			Provider: config.EmbedProviderVoyage, BaseURL: "http://127.0.0.1:1", Key: "k",
		},
	}
	if err := RunAll(ctx, cfg, nil, checkRerank); err != nil {
		t.Fatalf("RunAll with both axes healthy and no consumers: %v", err)
	}
	if got := tr.max(); got != 2 {
		t.Fatalf("peak in-flight axis checks = %d; want 2 — the fan-out ran them serially", got)
	}
	if got := embedHits.Load(); got != 1 {
		t.Errorf("RunAll issued %d embed pings; want exactly 1", got)
	}
	if got := rerankHits.Load(); got != 1 {
		t.Errorf("RunAll invoked the rerank check %d times; want exactly 1", got)
	}
}

// TestInFlightTracker_SerialControl is the KNOWN-NEGATIVE for the peak
// assertion above. It drives the identical tracker from a deliberately
// SERIAL driver and asserts the peak reads 1 — so a peak of 2 in the
// RunAll test is evidence of real concurrency, not of a tracker that
// always reports 2.
func TestInFlightTracker_SerialControl(t *testing.T) {
	tr := newInFlightTracker()
	for range 2 {
		tr.enter()
		time.Sleep(5 * time.Millisecond)
		tr.exit()
	}
	if got := tr.max(); got != 1 {
		t.Fatalf("serial driver produced a peak of %d; the tracker cannot distinguish serial from concurrent", got)
	}

	// And genuinely concurrent use of the same tracker reads 2, which is
	// the other half of the control.
	tr2 := newInFlightTracker()
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 2 {
		wg.Go(func() {
			<-start
			tr2.enter()
			time.Sleep(30 * time.Millisecond)
			tr2.exit()
		})
	}
	close(start)
	wg.Wait()
	if got := tr2.max(); got != 2 {
		t.Fatalf("concurrent driver produced a peak of %d; want 2", got)
	}
}

// inFlightTracker records the MAXIMUM number of concurrently-executing
// checks. It distinguishes a parallel fan-out from a serial one without
// any barrier or timeout, so it can neither deadlock nor flake on a slow
// machine: a serial driver can never push the counter above 1.
type inFlightTracker struct {
	mu   sync.Mutex
	cur  int
	peak int
}

func newInFlightTracker() *inFlightTracker { return &inFlightTracker{} }

func (t *inFlightTracker) enter() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cur++
	if t.cur > t.peak {
		t.peak = t.cur
	}
}

func (t *inFlightTracker) exit() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cur--
}

func (t *inFlightTracker) max() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.peak
}

// TestRunAll_RefusesNilRerankCheck pins the required-parameter contract:
// nil is a programming error that RunAll REFUSES, never a quiet skip of
// the rerank axis. Without this, threading the check through a parameter
// would introduce exactly the silent-no-check hole the indirection was
// supposed to avoid.
func TestRunAll_RefusesNilRerankCheck(t *testing.T) {
	t.Cleanup(config.SetForTest(nil))
	var hits atomic.Int32
	srv := embedPingServer(t, &hits, http.StatusOK)

	cfg := &config.Config{
		Embedder: &config.EmbedSection{
			Provider: config.EmbedProviderVoyage, BaseURL: srv.URL, Dimension: 256, Dtype: "ubinary",
		},
	}
	err := RunAll(context.Background(), cfg, nil, nil)
	if err == nil {
		t.Fatal("a nil rerank check must be REFUSED, not treated as skip")
	}
	if !strings.Contains(err.Error(), "rerank.CheckProvider") {
		t.Errorf("the refusal %q must name the caller's duty", err)
	}
	if got := hits.Load(); got != 0 {
		t.Errorf("a refused RunAll issued %d pings; want 0 — it must refuse before running anything", got)
	}
}

// TestRunAll_EmbedAxisFailureIsCollected proves a failing axis is
// COLLECTED into the joined error rather than canceling the group — the
// property the mutex-guarded slice exists for.
func TestRunAll_EmbedAxisFailureIsCollected(t *testing.T) {
	t.Setenv("VOYAGE_API_KEY", "")
	t.Cleanup(config.SetForTest(nil))

	var hits atomic.Int32
	bad := embedPingServer(t, &hits, http.StatusUnauthorized)
	cfg := &config.Config{
		Embedder: &config.EmbedSection{
			Provider: config.EmbedProviderVoyage, BaseURL: bad.URL, Dimension: 256, Dtype: "ubinary",
		},
	}
	noopRerank := func(context.Context, config.RerankSection) error { return nil }
	err := RunAll(context.Background(), cfg, nil, noopRerank)
	if err == nil {
		t.Fatal("a failing embed axis must surface in RunAll's joined error")
	}
	if !strings.Contains(err.Error(), "embed precheck") {
		t.Errorf("joined error %q does not name the embed axis", err)
	}

	// A failing RERANK check is collected the same way — the known-positive
	// that proves the injected check's error is not dropped on the floor.
	good := embedPingServer(t, &hits, http.StatusOK)
	cfg.Embedder.BaseURL = good.URL
	failRerank := func(context.Context, config.RerankSection) error {
		return errors.New("rerank precheck: simulated axis failure")
	}
	err = RunAll(context.Background(), cfg, nil, failRerank)
	if err == nil {
		t.Fatal("a failing rerank check must surface in RunAll's joined error")
	}
	if !strings.Contains(err.Error(), "simulated axis failure") {
		t.Errorf("joined error %q does not carry the rerank check's error", err)
	}
}
