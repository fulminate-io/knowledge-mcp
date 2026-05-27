// SPDX-License-Identifier: Apache-2.0

package cloud

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestCascadeSet_MarkNew(t *testing.T) {
	cs := NewCascadeSet()
	if !cs.Mark("aws", "arn:aws:ec2:us-east-1:123:instance/i-abc") {
		t.Fatal("Mark should return true for a new pair")
	}
}

func TestCascadeSet_MarkDuplicate(t *testing.T) {
	cs := NewCascadeSet()
	cs.Mark("aws", "arn:aws:ec2:us-east-1:123:instance/i-abc")
	if cs.Mark("aws", "arn:aws:ec2:us-east-1:123:instance/i-abc") {
		t.Fatal("Mark should return false for a duplicate pair")
	}
}

func TestCascadeSet_DifferentCollectorSameID(t *testing.T) {
	cs := NewCascadeSet()
	if !cs.Mark("aws", "foo") {
		t.Fatal("first (aws, foo) should return true")
	}
	if !cs.Mark("k8s", "foo") {
		t.Fatal("(k8s, foo) should return true — different collector, same ID")
	}
}

func TestCascadeSet_SameCollectorDifferentID(t *testing.T) {
	cs := NewCascadeSet()
	if !cs.Mark("aws", "foo") {
		t.Fatal("first (aws, foo) should return true")
	}
	if !cs.Mark("aws", "bar") {
		t.Fatal("(aws, bar) should return true — same collector, different ID")
	}
}

func TestCascadeSet_ContextRoundTrip(t *testing.T) {
	cs := NewCascadeSet()
	cs.Mark("aws", "seed")

	ctx := WithCascadeSet(newTestCtx(t), cs)
	got := CascadeSetFrom(ctx)
	if got == nil {
		t.Fatal("CascadeSetFrom returned nil after WithCascadeSet")
	}
	if got != cs {
		t.Fatal("CascadeSetFrom returned a different CascadeSet")
	}
	// Verify the retrieved set has the same state.
	if got.Mark("aws", "seed") {
		t.Fatal("retrieved CascadeSet should already have (aws, seed) marked")
	}
}

func TestCascadeSet_ContextMissing(t *testing.T) {
	got := CascadeSetFrom(newTestCtx(t))
	if got != nil {
		t.Fatal("CascadeSetFrom on a plain context should return nil")
	}
}

func TestCascadeSet_ConcurrentMark(t *testing.T) {
	cs := NewCascadeSet()

	const goroutines = 100
	var trueCount atomic.Int64
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for range goroutines {
		go func() {
			defer wg.Done()
			if cs.Mark("aws", "same-id") {
				trueCount.Add(1)
			}
		}()
	}
	wg.Wait()

	if trueCount.Load() != 1 {
		t.Fatalf("expected exactly 1 goroutine to get true from Mark, got %d", trueCount.Load())
	}
}
