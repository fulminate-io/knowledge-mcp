// SPDX-License-Identifier: Apache-2.0

package cicd

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
	delay  time.Duration
	mu     sync.Mutex
	called bool
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
				{ID: "w1", Name: "deploy.yml", ResourceType: "workflow"},
				{ID: "w2", Name: "ci.yml", ResourceType: "workflow"},
			},
			Edges: []EdgeSpec{
				{SourceID: "w1", TargetID: "e1", Relationship: kgtypes.EdgeDeploysTo},
			},
		},
	}
	sub2 := &mockSubCollector{
		name: "sub2",
		result: SubCollectorResult{
			Resources: []ResourceSpec{
				{ID: "r1", Name: "runner-1", ResourceType: "runner"},
			},
			Edges: []EdgeSpec{
				{SourceID: "w1", TargetID: "r1", Relationship: kgtypes.EdgeRunsIn},
			},
		},
	}

	ctx := context.Background()
	nodes, edges, err := RunSubCollectors(ctx, []SubCollector{sub1, sub2}, RunOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nodes) != 3 {
		t.Errorf("expected 3 nodes, got %d", len(nodes))
	}
	if len(edges) != 2 {
		t.Errorf("expected 2 edges, got %d", len(edges))
	}

	ids := make(map[string]bool)
	for _, n := range nodes {
		ids[n.Id] = true
	}
	for _, id := range []string{"w1", "w2", "r1"} {
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

	_, _, err := RunSubCollectors(context.Background(), []SubCollector{sub}, RunOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	want := "subcollector failing-sub: connection refused"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestRunSubCollectors_PartialFailure(t *testing.T) {
	good := &mockSubCollector{
		name: "good",
		result: SubCollectorResult{
			Resources: []ResourceSpec{
				{ID: "w1", Name: "deploy.yml", ResourceType: "workflow"},
			},
		},
	}
	bad := &mockSubCollector{
		name: "bad",
		err:  errors.New("api timeout"),
	}
	alsoGood := &mockSubCollector{
		name: "also-good",
		result: SubCollectorResult{
			Resources: []ResourceSpec{
				{ID: "r1", Name: "runner-1", ResourceType: "runner"},
			},
		},
	}

	nodes, _, err := RunSubCollectors(
		context.Background(),
		[]SubCollector{good, bad, alsoGood},
		RunOptions{},
	)
	if err == nil {
		t.Fatal("expected error from partial failure")
	}
	if got := err.Error(); got != "subcollector bad: api timeout" {
		t.Errorf("error = %q, want wrapped error for 'bad'", got)
	}
	if len(nodes) != 2 {
		t.Errorf("expected 2 nodes from partial success, got %d", len(nodes))
	}
}

func TestRunSubCollectors_EmptyList(t *testing.T) {
	nodes, edges, err := RunSubCollectors(context.Background(), nil, RunOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nodes) != 0 {
		t.Errorf("expected 0 nodes, got %d", len(nodes))
	}
	if len(edges) != 0 {
		t.Errorf("expected 0 edges, got %d", len(edges))
	}
}

func TestRunSubCollectors_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	sub := &mockSubCollector{
		name:   "should-not-run",
		result: SubCollectorResult{},
	}

	_, _, err := RunSubCollectors(ctx, []SubCollector{sub}, RunOptions{})
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
}

func TestRunSubCollectors_ConcurrencyLimit(t *testing.T) {
	var mu sync.Mutex
	maxConcurrent := 0
	currentConcurrent := 0

	makeSub := func(name string) SubCollector {
		return &mockSubCollector{
			name:  name,
			delay: 20 * time.Millisecond,
			result: SubCollectorResult{
				Resources: []ResourceSpec{{ID: name, Name: name, ResourceType: "workflow"}},
			},
		}
	}

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

	_, _, err := RunSubCollectors(context.Background(), subs, RunOptions{MaxConcurrency: 2})
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
