// SPDX-License-Identifier: Apache-2.0

package dream

import (
	"encoding/json"
	"sync"
	"time"
)

// Event is the in-memory record fanned out to dream-worker subscribers
// every time a tool call or worker invocation crosses a lifecycle
// boundary. Producers (the server-side MCP dispatch chokepoint added
// in Phase 2 and the Runner itself in Phase 4) construct an Event and
// hand it to EventBus.Emit; subscribers receive it on their buffered
// channel if their matcher accepts.
//
// Origin and SessionID are the same value in v1: the session_id wire
// field threaded through ToolCallRequest. The two field names are
// retained as a forward-compat split — a future change might
// distinguish "who tagged this event" from "which interactive session
// it belongs to" without breaking subscribers.
type Event struct {
	// Type is one of the Event* constants from worker.go.
	Type string

	// Tool is the MCP tool name on tool-started / tool-completed
	// events; empty on worker-* events.
	Tool string

	// Worker is the worker name on worker-started / worker-completed
	// events; empty on tool-* events.
	Worker string

	// Args is the marshaled tool call arguments on tool-started /
	// tool-completed; nil on worker-* events.
	Args json.RawMessage

	// Result is the marshaled ToolResult on tool-completed; nil on
	// other event types.
	Result json.RawMessage

	// Status is "ok" or "error" on completion events; empty on
	// started events.
	Status string

	// DurationMs is set on completion events; zero on started events.
	DurationMs int64

	// Origin is the session_id tag from the call site. When the
	// caller is a dream worker, Origin is "worker:<name>". When the
	// caller is anyone else, Origin is whatever they set on
	// ToolCallRequest.SessionId (often empty).
	Origin string

	// SessionID mirrors Origin in v1; see type comment.
	SessionID string

	// At is the wall-clock timestamp at which the event was minted.
	At time.Time
}

// subscription is one EventBus subscriber. The matcher is precomputed
// from the user-supplied Trigger so Emit never needs to inspect the
// Trigger again on the hot path. Each subscription owns a buffered
// channel; full channels drop on Emit instead of blocking the
// publisher.
type subscription struct {
	matcher func(Event) bool
	ch      chan Event
}

// SubscribeBufferSize is the buffer length of every subscription
// channel returned by Subscribe. Locked at 10 per the plan; a slow
// consumer that lets its channel fill drops events silently rather
// than backpressuring the EventBus or any of its other subscribers.
const SubscribeBufferSize = 10

// EventBus is a process-local fan-out bus. Producers call Emit;
// subscribers register a Trigger filter via Subscribe and read from
// the returned channel. The bus does not buffer events globally —
// Emit either delivers to a subscriber's per-subscription channel or
// drops, all in O(numSubscribers) time without I/O.
//
// The bus is intentionally generic: it knows nothing about worker
// identity or self-trigger guards. Callers that need a self-trigger
// guard (notably the Runner subscribing on behalf of a dream Worker)
// build it themselves by composing the Trigger's matcher with a
// pre-check on Event.Origin. EventBus exposes OriginIsDreamWorker as
// a helper for that composition.
type EventBus struct {
	mu   sync.RWMutex
	subs []*subscription
}

// NewEventBus returns an empty EventBus ready for Subscribe / Emit.
func NewEventBus() *EventBus { return &EventBus{} }

// Subscribe registers a subscriber that receives every Event matching
// the given Trigger. Returns a buffered read-only channel of size
// SubscribeBufferSize; pass that channel back to Unsubscribe to stop
// delivery and release resources.
//
// Filter semantics:
//   - The Trigger's Event field selects the Event.Type to match.
//     An empty Trigger.Event matches all event types (used by the
//     test fakes and any future "fire on anything" subscriber).
//   - The Trigger's Filter is an AND-of-equality match against
//     event metadata: keys "tool", "worker", "status", "origin"
//     are compared against the corresponding Event fields. Other
//     keys are ignored (unknown filter keys do not cause matching
//     to fail — they are simply not enforced).
//   - A Trigger with Event=="cron" or Event=="manual" is accepted
//     by Subscribe but never receives an Event from Emit, since
//     no producer in v1 emits those types. Those triggers are
//     for the Runner's own internal scheduling/manual-fire paths.
func (b *EventBus) Subscribe(filter Trigger) <-chan Event {
	sub := &subscription{
		matcher: matcherFor(filter),
		ch:      make(chan Event, SubscribeBufferSize),
	}
	b.mu.Lock()
	b.subs = append(b.subs, sub)
	b.mu.Unlock()
	return sub.ch
}

// Emit delivers ev to every matching subscriber, non-blocking. A
// subscriber whose channel is full drops the event silently — Emit
// MUST NOT block the publisher on a slow consumer. Returns
// immediately; deliveries are synchronous (no goroutine fan-out)
// because the channels themselves serve as the asynchrony seam.
//
// Emit takes a read lock so concurrent emits proceed in parallel; a
// subscribe or unsubscribe briefly excludes them.
func (b *EventBus) Emit(ev Event) {
	b.mu.RLock()
	subs := b.subs
	b.mu.RUnlock()
	for _, sub := range subs {
		if !sub.matcher(ev) {
			continue
		}
		select {
		case sub.ch <- ev:
		default:
			// Subscriber channel is full; drop the event. Per the
			// plan: at-most-once, lost-on-full-channel is fine.
		}
	}
}

// Unsubscribe stops delivery to the channel returned by Subscribe and
// closes that channel so any blocked reader unblocks with the
// zero-value-and-not-ok signal. Calling Unsubscribe with a channel
// that is not currently registered is a no-op.
func (b *EventBus) Unsubscribe(ch <-chan Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, sub := range b.subs {
		if compareChans(sub.ch, ch) {
			// Remove from slice (preserve order for determinism in tests).
			b.subs = append(b.subs[:i], b.subs[i+1:]...)
			close(sub.ch)
			return
		}
	}
}

// compareChans reports whether the underlying channel of a is the
// same channel as b. Direct comparison of `chan Event` against
// `<-chan Event` requires the conversion to happen on one side.
func compareChans(a chan Event, b <-chan Event) bool {
	return (<-chan Event)(a) == b
}

// matcherFor compiles a Trigger into a fast Emit-time predicate.
//
// The empty Trigger (zero value) matches everything; this keeps test
// fakes and future "fire on anything" subscribers concise.
func matcherFor(filter Trigger) func(Event) bool {
	wantType := filter.Event
	wantTool := filter.Filter["tool"]
	wantWorker := filter.Filter["worker"]
	wantStatus := filter.Filter["status"]
	wantOrigin := filter.Filter["origin"]
	return func(ev Event) bool {
		if wantType != "" && ev.Type != wantType {
			return false
		}
		if wantTool != "" && ev.Tool != wantTool {
			return false
		}
		if wantWorker != "" && ev.Worker != wantWorker {
			return false
		}
		if wantStatus != "" && ev.Status != wantStatus {
			return false
		}
		if wantOrigin != "" && ev.Origin != wantOrigin {
			return false
		}
		return true
	}
}

// OriginIsDreamWorker reports whether ev was emitted on behalf of a
// dream worker. The Runner uses this in the self-trigger guard it
// installs on every dream-worker subscription: events whose Origin
// names the same worker that owns the subscription are dropped before
// the user's Trigger.Filter even runs. The EventBus itself does not
// implement that policy; it just exposes the helper.
func OriginIsDreamWorker(ev Event) bool {
	return originIsDreamWorker(ev.Origin)
}

// workerOriginPrefix is the substring stamped onto Event.Origin /
// Event.SessionID by the Runner when a dream worker invokes an MCP
// tool. Mirrored in the Phase 4 Runner code; centralized here so the
// guard helper and the producer agree on the spelling.
const workerOriginPrefix = "worker:"

func originIsDreamWorker(origin string) bool {
	return len(origin) > len(workerOriginPrefix) &&
		origin[:len(workerOriginPrefix)] == workerOriginPrefix
}
