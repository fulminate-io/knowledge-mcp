// SPDX-License-Identifier: Apache-2.0

package hivemonitor

// LivenessState is the daemon's classification of a worker's transcript tail —
// the answer to "is this worker still working?" derived deterministically from
// the on-disk transcript, never from the (possibly degenerate) LLM's own
// report. The four states drive the heartbeat/escalation decision in the
// Monitor:
//
//   - EXECUTING: the transcript grew with fresh conversation activity since the
//     last tick — the worker is actively producing. Renew the lease.
//   - BLOCKED_ON_TOOL: the tail is an assistant tool_use with no matching
//     tool_result yet — the worker is waiting on a tool (e.g. a long Bash, an
//     MCP round-trip, or a human approval). This is STILL WORKING, not idle:
//     the linchpin of the classifier is that a blocked-on-tool tail must NOT be
//     mistaken for IDLE. Renew the lease.
//   - IDLE: the tail is a completed turn (tool_use → tool_result → assistant
//     end_turn, or codex turn.completed) with no new activity — the worker has
//     finished its turn and is awaiting input. Past the grace window this is the
//     escalation signal: stop renewing, hand off to the supervisor.
//   - DEAD: the transcript file is missing/unreadable — the worker's process is
//     gone. Stop renewing; the machine-down path (peer reaper) handles eviction.
type LivenessState int

const (
	// StateUnknown is the zero value — an unclassified state. Readers never
	// return it on success; it exists so a zero LivenessState is not silently
	// one of the four real states.
	StateUnknown LivenessState = iota
	// StateExecuting — fresh conversation activity since the last tick.
	StateExecuting
	// StateBlockedOnTool — tail is an unmatched tool_use; worker is waiting on a
	// tool. STILL WORKING (the linchpin: never classify this as IDLE).
	StateBlockedOnTool
	// StateIdle — completed turn awaiting input.
	StateIdle
	// StateDead — transcript file missing/unreadable; process gone.
	StateDead
)

// String renders the state for logs.
func (s LivenessState) String() string {
	switch s {
	case StateExecuting:
		return "EXECUTING"
	case StateBlockedOnTool:
		return "BLOCKED_ON_TOOL"
	case StateIdle:
		return "IDLE"
	case StateDead:
		return "DEAD"
	default:
		return "UNKNOWN"
	}
}

// Working reports whether the state means the worker is still working — the
// renew-the-lease predicate. EXECUTING and BLOCKED_ON_TOOL are working; IDLE and
// DEAD are not. Centralizing the predicate keeps the BLOCKED_ON_TOOL-is-working
// linchpin in one place.
func (s LivenessState) Working() bool {
	return s == StateExecuting || s == StateBlockedOnTool
}

// TranscriptReader classifies a worker's liveness from its transcript file,
// following the file by offset so each tick decodes only the bytes appended
// since prevOffset (pass 0 on the first classify for a claim). It returns the
// classified state and the new end-of-file offset to pass on the next tick.
//
// Implementations: claudeReader (the ~/.claude/projects JSONL shape) and
// codexReader (the ~/.codex/sessions rollout shape). A missing file classifies
// StateDead with no error (the worker's process is gone, which is a valid
// classification, not a read failure); a genuine read error returns a non-nil
// err.
type TranscriptReader interface {
	Classify(path string, prevOffset int64) (state LivenessState, newOffset int64, err error)
}
