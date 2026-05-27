// SPDX-License-Identifier: Apache-2.0

package dream

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
	workers "github.com/fulminate-io/knowledge-mcp/internal/workers"
)

// fakeLister satisfies workerLister without a real wire-loopback
// workercrud.Client. Tests inject canned []workers.Worker or an error;
// the call counter pins how often the Registry hits the underlying
// loader. Mirrors the same-shape fakeRuntime / fakeCRUD seams in the
// client tools package — keeping the doubles narrow makes test bodies
// short.
type fakeLister struct {
	workers []workers.Worker
	err     error
	calls   int
}

func (f *fakeLister) List(_ context.Context) ([]workers.Worker, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.workers, nil
}

// TestRegistry_All_NilListerReturnsEmpty pins the empty-registry mode
// unit tests rely on when they don't stand up a transport.
func TestRegistry_All_NilListerReturnsEmpty(t *testing.T) {
	r := NewRegistry(nil)
	got, err := r.All(context.Background())
	if err != nil {
		t.Fatalf("All err: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("All returned %d workers; want 0", len(got))
	}
}

// TestRegistry_All_DelegatesToLister pins the all-happy path: the lister
// is called exactly once, and its return value flows through verbatim.
func TestRegistry_All_DelegatesToLister(t *testing.T) {
	want := []Worker{
		{Name: "alpha", Provider: config.ProviderAnthropic, Model: "claude-sonnet-4-5", ToolAllowlist: []string{"think"}, Enabled: true},
		{Name: "bravo", Provider: config.ProviderOpenAI, Model: "gpt-x", ToolAllowlist: []string{"search"}, Enabled: false},
	}
	fake := &fakeLister{workers: append([]Worker(nil), want...)}
	r := NewRegistry(fake)

	got, err := r.All(context.Background())
	if err != nil {
		t.Fatalf("All err: %v", err)
	}
	if fake.calls != 1 {
		t.Errorf("List called %d times; want 1", fake.calls)
	}
	if len(got) != len(want) {
		t.Fatalf("len(got) = %d, want %d", len(got), len(want))
	}
	sort.Slice(got, func(i, j int) bool { return got[i].Name < got[j].Name })
	for i := range want {
		if got[i].Name != want[i].Name || got[i].Provider != want[i].Provider {
			t.Errorf("got[%d] = (%s, %s), want (%s, %s)",
				i, got[i].Name, got[i].Provider, want[i].Name, want[i].Provider)
		}
	}
}

// TestRegistry_All_ListerErrorBubbles pins that errors from the
// underlying lister surface verbatim — All does NOT swallow them, the
// runner decides whether to boot with a degraded catalog.
func TestRegistry_All_ListerErrorBubbles(t *testing.T) {
	fake := &fakeLister{err: errors.New("connection refused")}
	r := NewRegistry(fake)

	_, err := r.All(context.Background())
	if err == nil {
		t.Fatalf("All: want error, got nil")
	}
}

// TestRegistry_All_EmptyListReturnsEmpty pins the empty-catalog shape:
// the lister can legitimately return (nil, nil) when the graph carries
// zero NodeWorker rows; All forwards that as-is.
func TestRegistry_All_EmptyListReturnsEmpty(t *testing.T) {
	fake := &fakeLister{workers: nil}
	r := NewRegistry(fake)

	got, err := r.All(context.Background())
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("All = %v, want empty", got)
	}
}

// TestRegistry_ByName_HitAndMiss exercises both branches of the
// list-then-scan ByName path.
func TestRegistry_ByName_HitAndMiss(t *testing.T) {
	fake := &fakeLister{workers: []Worker{
		{Name: "alpha", Provider: config.ProviderAnthropic, Model: "m", Enabled: true},
	}}
	r := NewRegistry(fake)

	got, ok, err := r.ByName(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("ByName(alpha) err: %v", err)
	}
	if !ok || got.Name != "alpha" {
		t.Errorf("ByName(alpha) = (%v, %v), want (alpha, true)", got.Name, ok)
	}

	_, ok, err = r.ByName(context.Background(), "missing")
	if err != nil {
		t.Fatalf("ByName(missing) err: %v", err)
	}
	if ok {
		t.Errorf("ByName(missing) ok=true, want false")
	}
}

// TestRegistry_ByName_EmptyNameReturnsFalse pins the short-circuit
// contract: an empty name returns (zero, false, nil) without touching
// the lister.
func TestRegistry_ByName_EmptyNameReturnsFalse(t *testing.T) {
	fake := &fakeLister{}
	r := NewRegistry(fake)
	_, ok, err := r.ByName(context.Background(), "")
	if err != nil {
		t.Fatalf("ByName(\"\") err: %v", err)
	}
	if ok {
		t.Errorf("ByName(\"\") ok=true, want false")
	}
	if fake.calls != 0 {
		t.Errorf("List called %d times on empty name; want 0", fake.calls)
	}
}

// TestRegistry_All_NilReceiverShortCircuits pins the panic-free contract
// on a nil Registry — the runner sometimes constructs one lazily.
func TestRegistry_All_NilReceiverShortCircuits(t *testing.T) {
	var r *Registry
	got, err := r.All(context.Background())
	if err != nil {
		t.Fatalf("All err: %v", err)
	}
	if got != nil {
		t.Fatalf("All = %v, want nil", got)
	}
}
