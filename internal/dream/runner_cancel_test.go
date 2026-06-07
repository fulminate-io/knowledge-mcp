// SPDX-License-Identifier: Apache-2.0

package dream

import (
	"context"
	"testing"
	"time"
)

// TestRunner_RegisterUnregister exercises the in-flight registry directly:
// register two invocations, observe Running() returns both, unregister one,
// observe Running() returns just the survivor. Verifies the map plumbing
// before any cancel-flow assertions ride on it.
func TestRunner_RegisterUnregister(t *testing.T) {
	r := NewRunner(nil, NewEventBus(), t.TempDir(), nil, nil)

	id1, _, cancel1 := r.registerInvocation(context.Background(), "alpha")
	defer cancel1()
	id2, _, cancel2 := r.registerInvocation(context.Background(), "beta")
	defer cancel2()

	if id1 == id2 {
		t.Fatalf("invocation IDs must be unique; got duplicates %q", id1)
	}
	got := r.Running()
	if len(got) != 2 {
		t.Fatalf("Running() len = %d, want 2", len(got))
	}

	r.unregisterInvocation(id1)
	got = r.Running()
	if len(got) != 1 {
		t.Fatalf("after unregister Running() len = %d, want 1", len(got))
	}
	if got[0].WorkerName != "beta" || got[0].InvocationID != id2 {
		t.Fatalf("survivor mismatch: got %+v", got[0])
	}
}

// TestRunner_CancelByID cancels one invocation by id and asserts:
//   - its derived context observes the cancellation (Done channel closes).
//   - the OTHER invocation's context stays open.
//   - Cancel returns count=1.
//   - after the runWorker goroutine would have unregistered (here we
//     simulate by calling unregisterInvocation directly), Cancel(id) on
//     the same id returns count=0.
func TestRunner_CancelByID(t *testing.T) {
	r := NewRunner(nil, NewEventBus(), t.TempDir(), nil, nil)

	id1, ctx1, cancel1 := r.registerInvocation(context.Background(), "alpha")
	defer cancel1()
	id2, ctx2, cancel2 := r.registerInvocation(context.Background(), "beta")
	defer cancel2()

	count, err := r.Cancel(id1, "")
	if err != nil {
		t.Fatalf("Cancel(id1): %v", err)
	}
	if count != 1 {
		t.Fatalf("Cancel(id1) count = %d, want 1", count)
	}

	select {
	case <-ctx1.Done():
		// expected
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("ctx1 not cancelled within 200ms")
	}
	select {
	case <-ctx2.Done():
		t.Fatalf("ctx2 cancelled but should still be open")
	default:
	}

	// Simulate the runWorker goroutine unregistering on exit. After that,
	// Cancel(id1) finds no entry and reports 0.
	r.unregisterInvocation(id1)
	count, err = r.Cancel(id1, "")
	if err != nil {
		t.Fatalf("Cancel(id1) post-unregister: %v", err)
	}
	if count != 0 {
		t.Fatalf("post-unregister Cancel(id1) count = %d, want 0", count)
	}

	_ = id2
}

// TestRunner_CancelByName cancels every in-flight invocation matching a
// worker name. Two invocations of "alpha" + one of "beta" → Cancel("",
// "alpha") returns 2 and only the "alpha" contexts close.
func TestRunner_CancelByName(t *testing.T) {
	r := NewRunner(nil, NewEventBus(), t.TempDir(), nil, nil)

	_, ctxA1, cA1 := r.registerInvocation(context.Background(), "alpha")
	defer cA1()
	_, ctxA2, cA2 := r.registerInvocation(context.Background(), "alpha")
	defer cA2()
	_, ctxB, cB := r.registerInvocation(context.Background(), "beta")
	defer cB()

	count, err := r.Cancel("", "alpha")
	if err != nil {
		t.Fatalf("Cancel(name=alpha): %v", err)
	}
	if count != 2 {
		t.Fatalf("Cancel(name=alpha) count = %d, want 2", count)
	}

	for _, ctx := range []context.Context{ctxA1, ctxA2} {
		select {
		case <-ctx.Done():
		case <-time.After(200 * time.Millisecond):
			t.Fatalf("alpha ctx not cancelled within 200ms")
		}
	}
	select {
	case <-ctxB.Done():
		t.Fatalf("beta ctx cancelled but should still be open")
	default:
	}
}

// TestRunner_CancelValidation asserts that Cancel without id or name
// errors out with a clear message. Sanity check on the only validation
// path exposed by the cancel surface.
func TestRunner_CancelValidation(t *testing.T) {
	r := NewRunner(nil, NewEventBus(), t.TempDir(), nil, nil)
	if _, err := r.Cancel("", ""); err == nil {
		t.Fatalf("Cancel(\"\", \"\"): want error, got nil")
	}
}
