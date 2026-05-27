// SPDX-License-Identifier: Apache-2.0

package cloud

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// mockSubCollector implements SubCollector for testing.
type mockSubCollector struct {
	name   string
	result SubCollectorResult
	err    error
	delay  time.Duration // optional artificial delay
	mu     sync.Mutex
	called bool // set to true when Collect is invoked
}

func (m *mockSubCollector) Name() string { return m.name }

func (m *mockSubCollector) Collect(_ context.Context) (SubCollectorResult, error) {
	m.mu.Lock()
	m.called = true
	m.mu.Unlock()
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	return m.result, m.err
}

func TestRunSubCollectors_MergesResults(t *testing.T) {
	sub1 := &mockSubCollector{
		name: "sub1",
		result: SubCollectorResult{
			Resources: []ResourceSpec{
				{ID: "r1", Name: "Resource 1", ResourceType: "ec2:instance"},
				{ID: "r2", Name: "Resource 2", ResourceType: "ec2:instance"},
			},
			Edges: []EdgeSpec{
				{SourceID: "r1", TargetID: "r2", Relationship: kgtypes.EdgeUsesSA},
			},
			Targets: []CollectTarget{
				{Collector: "k8s", ID: "cluster-1"},
			},
		},
	}
	sub2 := &mockSubCollector{
		name: "sub2",
		result: SubCollectorResult{
			Resources: []ResourceSpec{
				{ID: "r3", Name: "Resource 3", ResourceType: "gke:cluster"},
				{ID: "r4", Name: "Resource 4", ResourceType: "gke:nodepool"},
			},
			Edges: []EdgeSpec{
				{SourceID: "r3", TargetID: "r4", Relationship: kgtypes.EdgeContains},
			},
			Targets: []CollectTarget{
				{Collector: "k8s", ID: "cluster-2"},
			},
		},
	}

	nodes, edges, targets, err := RunSubCollectors(newTestCtx(t), []SubCollector{sub1, sub2}, RunOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nodes) != 4 {
		t.Errorf("expected 4 nodes, got %d", len(nodes))
	}
	if len(edges) != 2 {
		t.Errorf("expected 2 edges, got %d", len(edges))
	}
	if len(targets) != 2 {
		t.Errorf("expected 2 targets, got %d", len(targets))
	}

	// Verify node IDs are preserved.
	ids := make(map[string]bool)
	for _, n := range nodes {
		ids[n.Id] = true
	}
	for _, id := range []string{"r1", "r2", "r3", "r4"} {
		if !ids[id] {
			t.Errorf("missing node ID %s", id)
		}
	}
}

func TestRunSubCollectors_ErrorWrapping(t *testing.T) {
	sub := &mockSubCollector{
		name: "failing-sub",
		err:  errors.New("connection refused"),
	}

	_, _, _, err := RunSubCollectors(newTestCtx(t), []SubCollector{sub}, RunOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	want := "subcollector failing-sub: connection refused"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestRunSubCollectors_PartialFailure(t *testing.T) {
	goodSub := &mockSubCollector{
		name: "good",
		result: SubCollectorResult{
			Resources: []ResourceSpec{
				{ID: "r1", Name: "Good Resource", ResourceType: "ec2:instance"},
			},
		},
	}
	badSub := &mockSubCollector{
		name: "bad",
		err:  errors.New("api timeout"),
	}
	anotherGoodSub := &mockSubCollector{
		name: "also-good",
		result: SubCollectorResult{
			Resources: []ResourceSpec{
				{ID: "r2", Name: "Also Good", ResourceType: "s3:bucket"},
			},
		},
	}

	nodes, edges, targets, err := RunSubCollectors(
		newTestCtx(t),
		[]SubCollector{goodSub, badSub, anotherGoodSub},
		RunOptions{},
	)

	// Error should be present for the failed subcollector.
	if err == nil {
		t.Fatal("expected error from partial failure")
	}
	if got := err.Error(); got != "subcollector bad: api timeout" {
		t.Errorf("error = %q, want wrapped error for 'bad'", got)
	}

	// Partial results from the successful subcollectors should still be returned.
	if len(nodes) != 2 {
		t.Errorf("expected 2 nodes from partial success, got %d", len(nodes))
	}
	if len(edges) != 0 {
		t.Errorf("expected 0 edges, got %d", len(edges))
	}
	if len(targets) != 0 {
		t.Errorf("expected 0 targets, got %d", len(targets))
	}
}

func TestRunSubCollectors_Progress(t *testing.T) {
	subs := []SubCollector{
		&mockSubCollector{name: "first", result: SubCollectorResult{}},
		&mockSubCollector{name: "second", result: SubCollectorResult{}},
		&mockSubCollector{name: "third", result: SubCollectorResult{}},
	}

	var mu sync.Mutex
	type progressCall struct {
		current int
		total   int
		message string
	}
	var calls []progressCall

	opts := RunOptions{
		OnProgress: func(current, total int, message string) {
			mu.Lock()
			calls = append(calls, progressCall{current, total, message})
			mu.Unlock()
		},
	}

	_, _, _, err := RunSubCollectors(newTestCtx(t), subs, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 3 {
		t.Fatalf("expected 3 progress calls, got %d", len(calls))
	}
	// With parallel execution, completion order is non-deterministic.
	// Verify all calls have correct total and unique names.
	names := make(map[string]bool)
	for _, c := range calls {
		if c.total != 3 {
			t.Errorf("total = %d, want 3", c.total)
		}
		names[c.message] = true
	}
	for _, expected := range []string{"first", "second", "third"} {
		if !names[expected] {
			t.Errorf("missing progress call for %q", expected)
		}
	}
}

func TestRunSubCollectors_EmptyList(t *testing.T) {
	nodes, edges, targets, err := RunSubCollectors(newTestCtx(t), nil, RunOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nodes) != 0 {
		t.Errorf("expected 0 nodes, got %d", len(nodes))
	}
	if len(edges) != 0 {
		t.Errorf("expected 0 edges, got %d", len(edges))
	}
	if len(targets) != 0 {
		t.Errorf("expected 0 targets, got %d", len(targets))
	}
}

func TestRunSubCollectors_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(newTestCtx(t))
	cancel() // cancel immediately

	sub := &mockSubCollector{
		name:   "should-not-run",
		result: SubCollectorResult{},
	}

	_, _, _, err := RunSubCollectors(ctx, []SubCollector{sub}, RunOptions{})
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
}

func TestRunSubCollectors_Parallel(t *testing.T) {
	// Verify subcollectors actually run concurrently.
	// 3 subcollectors each taking 50ms — serial would take ~150ms,
	// parallel should take ~50ms.
	subs := []SubCollector{
		&mockSubCollector{name: "a", delay: 50 * time.Millisecond, result: SubCollectorResult{
			Resources: []ResourceSpec{{ID: "a1", Name: "A", ResourceType: "test"}},
		}},
		&mockSubCollector{name: "b", delay: 50 * time.Millisecond, result: SubCollectorResult{
			Resources: []ResourceSpec{{ID: "b1", Name: "B", ResourceType: "test"}},
		}},
		&mockSubCollector{name: "c", delay: 50 * time.Millisecond, result: SubCollectorResult{
			Resources: []ResourceSpec{{ID: "c1", Name: "C", ResourceType: "test"}},
		}},
	}

	start := time.Now()
	nodes, _, _, err := RunSubCollectors(newTestCtx(t), subs, RunOptions{})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nodes) != 3 {
		t.Errorf("expected 3 nodes, got %d", len(nodes))
	}
	// Should complete in well under 150ms (serial time).
	if elapsed > 120*time.Millisecond {
		t.Errorf("expected parallel execution (<120ms), took %v", elapsed)
	}
}

func TestRunSubCollectors_ConcurrencyLimit(t *testing.T) {
	// Verify semaphore limits concurrency.
	var mu sync.Mutex
	maxConcurrent := 0
	currentConcurrent := 0

	makeSub := func(name string) SubCollector {
		return &mockSubCollector{
			name:  name,
			delay: 20 * time.Millisecond,
			result: SubCollectorResult{
				Resources: []ResourceSpec{{ID: name, Name: name, ResourceType: "test"}},
			},
		}
	}

	// Use 5 subs with max concurrency 2.
	subs := make([]SubCollector, 5)
	for i := range subs {
		name := fmt.Sprintf("sub-%d", i)
		inner := makeSub(name)
		subs[i] = &concurrencyTracker{
			inner: inner,
			onStart: func() {
				mu.Lock()
				currentConcurrent++
				if currentConcurrent > maxConcurrent {
					maxConcurrent = currentConcurrent
				}
				mu.Unlock()
			},
			onEnd: func() {
				mu.Lock()
				currentConcurrent--
				mu.Unlock()
			},
		}
	}

	_, _, _, err := RunSubCollectors(newTestCtx(t), subs, RunOptions{MaxConcurrency: 2})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if maxConcurrent > 2 {
		t.Errorf("expected max concurrency 2, got %d", maxConcurrent)
	}
}

// concurrencyTracker wraps a SubCollector to track concurrent execution.
type concurrencyTracker struct {
	inner   SubCollector
	onStart func()
	onEnd   func()
}

func (c *concurrencyTracker) Name() string { return c.inner.Name() }

func (c *concurrencyTracker) Collect(ctx context.Context) (SubCollectorResult, error) {
	c.onStart()
	defer c.onEnd()
	return c.inner.Collect(ctx)
}
