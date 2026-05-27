// SPDX-License-Identifier: Apache-2.0

package dream

import (
	"encoding/json"
	"time"
)

// EmitToolStarted is the Phase 2 chokepoint hook for tool-started lifecycle
// events. The handler dispatch loop calls this at every tool entry; the
// Runner translates the bare arguments back into an Event and fans out via
// the EventBus.
//
// Best-effort + non-blocking: the EventBus drops on full subscriber
// channels, so this method completes in O(numSubscribers) without I/O.
//
// Emitted from the client-side dispatch chokepoint in cmd/knowledge/mcp.go.
func (r *Runner) EmitToolStarted(toolName, origin, sessionID string, args json.RawMessage, at time.Time) {
	if r == nil || r.Bus == nil {
		return
	}
	r.Bus.Emit(Event{
		Type:      EventToolStarted,
		Tool:      toolName,
		Origin:    origin,
		SessionID: sessionID,
		Args:      args,
		At:        at,
	})
}

// EmitToolCompleted is the Phase 2 chokepoint hook for tool-completed
// lifecycle events. The dispatch loop calls this at every tool exit
// (success or error); the Runner emits one Event per call.
//
// Best-effort + non-blocking; see EmitToolStarted.
//
// Emitted from the client-side dispatch chokepoint in cmd/knowledge/mcp.go.
func (r *Runner) EmitToolCompleted(toolName, origin, sessionID string, args, result json.RawMessage, status string, durationMs int64, at time.Time) {
	if r == nil || r.Bus == nil {
		return
	}
	r.Bus.Emit(Event{
		Type:       EventToolCompleted,
		Tool:       toolName,
		Origin:     origin,
		SessionID:  sessionID,
		Args:       args,
		Result:     result,
		Status:     status,
		DurationMs: durationMs,
		At:         at,
	})
}
