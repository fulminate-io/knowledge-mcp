// SPDX-License-Identifier: Apache-2.0

package workers

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// Event-name constants used on Trigger.Event and Event.Type. These are
// the only legal values; Worker.Validate rejects unknown ones.
const (
	EventToolStarted     = "tool-started"
	EventToolCompleted   = "tool-completed"
	EventWorkerStarted   = "worker-started"
	EventWorkerCompleted = "worker-completed"
	EventCron            = "cron"
	EventManual          = "manual"
)

// validEvents lists every Trigger.Event string Worker.Validate accepts.
// Mirrors the constants above. Kept as a map for O(1) lookup.
var validEvents = map[string]struct{}{
	EventToolStarted:     {},
	EventToolCompleted:   {},
	EventWorkerStarted:   {},
	EventWorkerCompleted: {},
	EventCron:            {},
	EventManual:          {},
}

// slugRegex matches Worker.Name. Names are used unmodified as filenames
// (per-worker log file under <graphStorage>/workers/<name>.log) and as
// origin tags ("worker:<name>"), so they are constrained to a tight
// shell-safe slug: lowercase alphanumeric plus hyphens, no leading or
// trailing hyphen, no double-hyphen.
var slugRegex = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// Worker is the user-defined unit of agentic work. Each Worker has a
// stable Name (used as ID, log filename, and origin tag), a system
// prompt, an LLM Provider+Model resolved through domains/config, and a
// list of Triggers determining when it fires. ToolAllowlist is the
// set of MCP tool names the worker is permitted to call during a
// run; an empty allowlist is rejected by Validate (workers MUST
// declare which tools they need — implicit "all tools" is unsafe).
type Worker struct {
	// Name uniquely identifies the worker. Used as the row key in
	// the graph (NodeWorker.ID = "worker:" + Name in Phase 3), as the
	// log file basename, and as the Origin tag emitted on every tool
	// call the worker makes. Must match slugRegex.
	Name string

	// Description is human-prose, free-form. Optional.
	Description string

	// SystemPrompt is fed verbatim to the LLM at the start of every run.
	SystemPrompt string

	// Provider names the LLM backend. Reuses config.Provider so the
	// dream layer does not introduce a parallel enum. CLI providers
	// (claude-cli, codex-cli) are accepted by Validate but fail at
	// the first MCP-tool call with *llm.LLMError; see doc.go.
	Provider config.Provider

	// Model names the LLM model. Required; Worker.Validate rejects
	// empty values.
	Model string

	// Triggers determine when the worker fires. At least one trigger
	// is REQUIRED in practice but Validate does not enforce that —
	// a worker with zero triggers is parseable but only ever
	// reachable via worker:trigger (manual fire). Unknown trigger
	// events are rejected by Validate.
	Triggers []Trigger

	// ToolAllowlist is the set of MCP tool names the worker may call
	// (e.g. "search", "thoughts", "mutate"). Required and non-empty.
	ToolAllowlist []string

	// MaxIterations caps the number of ReAct turns per invocation.
	// Zero means use the package default (defaultMaxIterations).
	MaxIterations int

	// MaxWallclockSeconds caps the total wallclock duration of one
	// invocation. Zero means use the package default
	// (defaultMaxWallclockSeconds).
	MaxWallclockSeconds int

	// Enabled controls whether the Runner subscribes this worker to
	// its triggers. A disabled worker stays in the Registry but does
	// not fire on events; worker:trigger still works (manual override).
	Enabled bool
}

// Trigger declares one event source for a Worker. The Runner installs
// one EventBus subscription per Trigger plus the Runner's self-origin
// guard.
type Trigger struct {
	// Event is one of the Event* constants above.
	Event string

	// Filter is an AND-of-equality match against Event metadata.
	// Recognized keys per event type:
	//   - tool-started / tool-completed: "tool" (tool name), "status"
	//     ("ok" / "error"), "origin" (matches Event.Origin verbatim).
	//   - worker-started / worker-completed: "worker" (worker name),
	//     "status".
	//   - cron / manual: filter is unused.
	// An empty Filter matches every event of the declared Event type.
	Filter map[string]string

	// Schedule is a cron expression on Triggers with Event=="cron".
	// Validated by Worker.Validate (parse-only; not dispatched in v1).
	// Ignored on non-cron triggers.
	Schedule string
}

// Default values applied by the Runner when a Worker leaves them zero.
const (
	defaultMaxIterations       = 10
	defaultMaxWallclockSeconds = 300
)

// DefaultMaxIterations and DefaultMaxWallclockSeconds expose the
// runtime defaults so callers (the Runner, tests, status-reporting
// MCP responses) can resolve the effective value without duplicating
// the constant.
func DefaultMaxIterations() int       { return defaultMaxIterations }
func DefaultMaxWallclockSeconds() int { return defaultMaxWallclockSeconds }

// Validate reports the first structural problem with w, or nil when
// every required field is present and well-formed. Validate does NOT
// check anything that requires runtime state (provider availability,
// API keys, MCP catalog membership of ToolAllowlist values) — those
// are Runner-side concerns.
func (w *Worker) Validate() error {
	if w == nil {
		return errors.New("dream: nil Worker")
	}
	if w.Name == "" {
		return errors.New("dream: Worker.Name is required")
	}
	if !slugRegex.MatchString(w.Name) {
		return fmt.Errorf("dream: Worker.Name %q is not a valid slug (lowercase letters, digits, single hyphens; no leading/trailing hyphen)", w.Name)
	}
	if strings.TrimSpace(w.SystemPrompt) == "" {
		return errors.New("dream: Worker.SystemPrompt is required")
	}
	if w.Provider == "" {
		return errors.New("dream: Worker.Provider is required")
	}
	if !w.Provider.IsValid() {
		return fmt.Errorf("dream: Worker.Provider %q is not a recognized provider (anthropic / openai / gemini / claude-cli / codex-cli)", w.Provider)
	}
	if strings.TrimSpace(w.Model) == "" {
		return errors.New("dream: Worker.Model is required")
	}
	if len(w.ToolAllowlist) == 0 {
		return errors.New("dream: Worker.ToolAllowlist is required and must contain at least one tool name")
	}
	for i, tool := range w.ToolAllowlist {
		if strings.TrimSpace(tool) == "" {
			return fmt.Errorf("dream: Worker.ToolAllowlist[%d] is empty", i)
		}
	}
	for i, t := range w.Triggers {
		if err := validateTrigger(t); err != nil {
			return fmt.Errorf("dream: Worker.Triggers[%d]: %w", i, err)
		}
	}
	return nil
}

// validateTrigger checks one Trigger in isolation.
func validateTrigger(t Trigger) error {
	if t.Event == "" {
		return errors.New("Trigger.Event is required")
	}
	if _, ok := validEvents[t.Event]; !ok {
		return fmt.Errorf("Trigger.Event %q is not recognized (must be one of tool-started, tool-completed, worker-started, worker-completed, cron, manual)", t.Event)
	}
	if t.Event == EventCron {
		if strings.TrimSpace(t.Schedule) == "" {
			return errors.New("Trigger.Schedule is required when Event=cron")
		}
		if err := validateCronExpr(t.Schedule); err != nil {
			return fmt.Errorf("Trigger.Schedule %q: %w", t.Schedule, err)
		}
	}
	return nil
}

// cronFieldRegex matches one whitespace-separated field of a cron
// expression at the lexical level. It is intentionally permissive on
// value ranges (the parse is shape-only; v1 never dispatches the
// schedule) but rejects characters that no cron dialect treats as
// legal field syntax. Specifically allowed: digits, '*', '/', '-',
// ',', '?', 'L', 'W', '#', and lowercase letters (for month/day-of-week
// short names like 'mon', 'jan').
var cronFieldRegex = regexp.MustCompile(`^[0-9*/,?\-LW#a-z]+$`)

// validateCronExpr does a shape-level validation of a cron expression.
// We support both 5-field (standard cron: minute, hour, dom, month,
// dow) and 6-field (with leading second) variants — the dispatcher
// added in a follow-up ticket will pick a parser. Validate's job is
// only to reject obvious garbage at config-load time.
func validateCronExpr(expr string) error {
	expr = strings.ToLower(strings.TrimSpace(expr))
	if expr == "" {
		return errors.New("empty cron expression")
	}
	// Special @-style aliases recognized by most cron parsers.
	switch expr {
	case "@yearly", "@annually", "@monthly", "@weekly", "@daily", "@midnight", "@hourly":
		return nil
	}
	fields := strings.Fields(expr)
	if len(fields) != 5 && len(fields) != 6 {
		return fmt.Errorf("expected 5 or 6 space-separated fields, got %d", len(fields))
	}
	for i, f := range fields {
		if !cronFieldRegex.MatchString(f) {
			return fmt.Errorf("field %d %q contains illegal characters", i+1, f)
		}
	}
	return nil
}
