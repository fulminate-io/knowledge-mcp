// SPDX-License-Identifier: Apache-2.0

package dream

import (
	"encoding/json"
	"time"
)

// InvocationRecord is the on-disk shape one worker invocation produces.
// Phase 3 declares the struct so the worker MCP tool's WorkerControl
// interface (now client-side at cmd/knowledge/internal/tools/worker.go)
// compiles; Phase 4 fills in the producer (the per-worker WorkerLog)
// and consumer (Runner.Status reads the last N records via
// WorkerLog.ReadRecent).
//
// JSON tags match the documented log-line format (one JSON object per
// line, append-only, 0600 perms). Producers MUST set Time to the wall-
// clock at write — the log writer does NOT timestamp on Append.
type InvocationRecord struct {
	// Time is the wall-clock at which the record was written.
	Time time.Time `json:"time"`

	// InvocationID is the per-run UUID assigned by the Runner when the
	// invocation enters the in-flight set. Set on Kind="start" / "end"
	// records by the Runner; absent on tool-call / tool-result records.
	// Operators read this from worker:status to target a specific run
	// when calling worker(operation:"cancel", invocation:"<id>").
	InvocationID string `json:"invocation_id,omitempty"`

	// Kind classifies the record. One of: "start", "tool-call",
	// "tool-result", "end". Unknown kinds are tolerated by readers
	// (forward-compat) but producers MUST stick to this set.
	Kind string `json:"kind"`

	// Trigger is set on Kind="start" records and identifies what
	// caused the invocation: "manual" | "event" | "cron". Empty on
	// other record kinds.
	Trigger string `json:"trigger,omitempty"`

	// Tool is the MCP tool name on Kind="tool-call" / "tool-result"
	// records; empty on start/end records.
	Tool string `json:"tool,omitempty"`

	// Args is the marshaled tool call arguments on Kind="tool-call"
	// records; nil on other record kinds.
	Args json.RawMessage `json:"args,omitempty"`

	// Result is the marshaled tool result on Kind="tool-result"
	// records; nil on other record kinds.
	Result json.RawMessage `json:"result,omitempty"`

	// Status is "ok" or "error" on tool-result and end records;
	// empty on start / tool-call.
	Status string `json:"status,omitempty"`

	// DurationMs is the elapsed time in milliseconds on tool-result
	// records (per-tool latency) and end records (total invocation
	// wallclock); zero on other record kinds.
	DurationMs int64 `json:"duration_ms,omitempty"`

	// Error is the human-readable error message on Kind="end" /
	// "tool-result" records when Status="error"; empty otherwise.
	Error string `json:"error,omitempty"`
}
