// SPDX-License-Identifier: Apache-2.0

package dream

import (
	"sync"
	"testing"
	"time"
)

func TestEventBus_Subscribe_ReturnsBufferedChan(t *testing.T) {
	b := NewEventBus()
	ch := b.Subscribe(Trigger{Event: EventToolStarted})
	if ch == nil {
		t.Fatalf("Subscribe returned nil channel")
	}
	// Confirm buffer length by emitting SubscribeBufferSize events
	// without a reader; none should drop, and a SubscribeBufferSize+1th
	// emission should drop silently.
	for range SubscribeBufferSize {
		b.Emit(Event{Type: EventToolStarted})
	}
	// One more — should drop, not block.
	done := make(chan struct{})
	go func() {
		b.Emit(Event{Type: EventToolStarted})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("Emit blocked on full subscriber channel")
	}
	// Drain to confirm SubscribeBufferSize events landed.
	got := 0
	for {
		select {
		case <-ch:
			got++
		default:
			if got != SubscribeBufferSize {
				t.Fatalf("got %d events on full channel; want %d", got, SubscribeBufferSize)
			}
			return
		}
	}
}

func TestEventBus_Emit_FansOutToMatchingSubscribers(t *testing.T) {
	b := NewEventBus()
	chTool := b.Subscribe(Trigger{Event: EventToolStarted})
	chWorker := b.Subscribe(Trigger{Event: EventWorkerStarted})

	b.Emit(Event{Type: EventToolStarted, Tool: "search"})
	b.Emit(Event{Type: EventWorkerStarted, Worker: "smoke-hello"})

	select {
	case ev := <-chTool:
		if ev.Type != EventToolStarted || ev.Tool != "search" {
			t.Fatalf("tool subscriber got unexpected event: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatalf("tool subscriber did not receive its event")
	}

	select {
	case ev := <-chWorker:
		if ev.Type != EventWorkerStarted || ev.Worker != "smoke-hello" {
			t.Fatalf("worker subscriber got unexpected event: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatalf("worker subscriber did not receive its event")
	}

	// Tool subscriber must NOT have received the worker-started event.
	select {
	case ev := <-chTool:
		t.Fatalf("tool subscriber received cross-type event: %+v", ev)
	default:
	}
}

func TestEventBus_Emit_FilterMatchesAllRecognizedKeys(t *testing.T) {
	b := NewEventBus()
	ch := b.Subscribe(Trigger{
		Event:  EventToolCompleted,
		Filter: map[string]string{"tool": "search", "status": "ok", "origin": "worker:smoke-hello"},
	})
	// Mismatch on tool — drops.
	b.Emit(Event{Type: EventToolCompleted, Tool: "think", Status: "ok", Origin: "worker:smoke-hello"})
	// Mismatch on status — drops.
	b.Emit(Event{Type: EventToolCompleted, Tool: "search", Status: "error", Origin: "worker:smoke-hello"})
	// Mismatch on origin — drops.
	b.Emit(Event{Type: EventToolCompleted, Tool: "search", Status: "ok", Origin: "other"})
	// Full match — delivered.
	b.Emit(Event{Type: EventToolCompleted, Tool: "search", Status: "ok", Origin: "worker:smoke-hello"})

	select {
	case ev := <-ch:
		if ev.Tool != "search" || ev.Status != "ok" || ev.Origin != "worker:smoke-hello" {
			t.Fatalf("got wrong event: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatalf("subscriber did not get the matching event")
	}
	// No extras.
	select {
	case ev := <-ch:
		t.Fatalf("subscriber got unexpected extra event: %+v", ev)
	default:
	}
}

func TestEventBus_Emit_EmptyTriggerEventMatchesEveryType(t *testing.T) {
	b := NewEventBus()
	ch := b.Subscribe(Trigger{}) // empty trigger
	b.Emit(Event{Type: EventToolStarted})
	b.Emit(Event{Type: EventWorkerCompleted})
	got := 0
loop:
	for {
		select {
		case <-ch:
			got++
		case <-time.After(50 * time.Millisecond):
			break loop
		}
	}
	if got != 2 {
		t.Fatalf("empty trigger received %d events; want 2", got)
	}
}

func TestEventBus_Unsubscribe_StopsDeliveryAndClosesChan(t *testing.T) {
	b := NewEventBus()
	ch := b.Subscribe(Trigger{Event: EventToolStarted})
	b.Emit(Event{Type: EventToolStarted})
	if got := <-ch; got.Type != EventToolStarted {
		t.Fatalf("first event missing")
	}

	b.Unsubscribe(ch)
	// After Unsubscribe, the channel must be closed.
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatalf("channel still open after Unsubscribe")
		}
	case <-time.After(time.Second):
		t.Fatalf("channel not closed after Unsubscribe")
	}

	// Second Unsubscribe is a no-op (must not panic).
	b.Unsubscribe(ch)

	// Further Emits don't deliver to anyone (and don't panic).
	b.Emit(Event{Type: EventToolStarted})
}

func TestEventBus_Emit_DropOnFullSubscriberDoesNotBlockOthers(t *testing.T) {
	b := NewEventBus()
	slow := b.Subscribe(Trigger{Event: EventToolStarted}) // never read
	fast := b.Subscribe(Trigger{Event: EventToolStarted}) // read promptly
	_ = slow

	for range SubscribeBufferSize {
		b.Emit(Event{Type: EventToolStarted})
	}
	// Drain fast.
	got := 0
loop:
	for {
		select {
		case <-fast:
			got++
		case <-time.After(50 * time.Millisecond):
			break loop
		}
	}
	if got != SubscribeBufferSize {
		t.Fatalf("fast subscriber got %d / %d events", got, SubscribeBufferSize)
	}

	// Now emit one more; slow's chan is full so the emit drops there
	// but fast's chan is empty so fast must receive it. The publisher
	// must not block.
	done := make(chan struct{})
	go func() {
		b.Emit(Event{Type: EventToolStarted})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("Emit blocked despite fast subscriber having capacity")
	}
	select {
	case <-fast:
	case <-time.After(time.Second):
		t.Fatalf("fast subscriber missed the post-drop event")
	}
}

func TestEventBus_OriginIsDreamWorker(t *testing.T) {
	cases := map[string]bool{
		"worker:smoke-hello": true,
		"worker:detect":      true,
		"":                   false,
		"session-abc":        false,
		"worker":             false, // no colon
		"worker:":            false, // empty name after prefix
		"workerX:smoke":      false,
		" worker:smoke":      false, // leading space
	}
	for origin, want := range cases {
		got := OriginIsDreamWorker(Event{Origin: origin})
		if got != want {
			t.Errorf("OriginIsDreamWorker(%q) = %v; want %v", origin, got, want)
		}
	}
}

func TestEventBus_ConcurrentEmit_DropsCleanly(t *testing.T) {
	b := NewEventBus()
	ch := b.Subscribe(Trigger{Event: EventToolStarted})

	const goroutines = 100
	const perGoroutine = 50

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			for range perGoroutine {
				b.Emit(Event{Type: EventToolStarted})
			}
		}()
	}

	// Concurrently read from the channel; we don't care how many
	// land — the point is to exercise the drop path under -race.
	doneRead := make(chan struct{})
	go func() {
		defer close(doneRead)
		for {
			select {
			case <-ch:
			case <-time.After(200 * time.Millisecond):
				return
			}
		}
	}()

	wg.Wait()
	<-doneRead
}
