// SPDX-License-Identifier: Apache-2.0

package projects

import "github.com/fulminate-io/knowledge-mcp/internal/backends"

// PlanArgs is the input for CreatePlan.
type PlanArgs struct {
	Name          string
	Goal          string
	Summary       string
	ResearchID    string
	TicketID      string
	Phases        []PhaseArgs
	OpenQuestions []QuestionArgs

	// PatternIDs are canonical pattern node IDs the plan extends. Wired
	// as plan→pattern EdgeUses edges by BuildPlanGraph.
	PatternIDs []string

	// NoPatternsReason is the audited escape hatch when no pattern applies
	// (e.g. trivial doc edit, scaffolding). Stored as plan-node metadata
	// "no_patterns_reason" by BuildPlanGraph.
	NoPatternsReason string

	// ProposedPatterns are not-yet-cataloged patterns this plan introduces.
	// CreatePlan eagerly creates a NodePattern with status="emerging" for each
	// and links plan→new-pattern via EdgeUses.
	ProposedPatterns []ProposedPatternArgs

	// LanguagePatterns are language-specific defensive patterns/findings (e.g.,
	// from practice/go) — wired as plan→<finding|pattern> EdgeAudits edges.
	// Distinct from PatternIDs (architectural prescription); these are
	// vigilance markers for the planner/reviewer. Optional and independent of
	// the PatternIDs/NoPatternsReason/ProposedPatterns tristate.
	LanguagePatterns []string
}

// ProposedPatternArgs is a not-yet-cataloged pattern surfaced by a planner.
// CreatePlan eagerly creates a pattern node with status="emerging" for each.
type ProposedPatternArgs struct {
	Name   string
	Sketch string // interface sketch / pseudocode describing the proposed pattern shape
}

type PhaseArgs struct {
	Name     string
	Overview string
	Summary  string
	Steps    []StepArgs
}

type StepArgs struct {
	Name        string
	Description string
	Summary     string
	FilePaths   string
	Criteria    []CriterionArgs
}

type CriterionArgs struct {
	Description string
	Command     string
	Type        string // "automated" or "manual"
}

// ResearchArgs is the input for CreateResearch.
type ResearchArgs struct {
	Name      string
	Goal      string
	Summary   string
	TicketID  string
	Questions []QuestionArgs
}

type QuestionArgs struct {
	Question string
	Summary  string
	Context  string
}

// ProjectArgs is the input for CreateProject.
//
// Backend resolution moved to the client-side intercept
// layer (cmd/knowledge/internal/tools/intercept_create_project.go). The
// client calls Linear inline, then forwards create_project to the server
// with BackendName / RemoteRef / RemoteGroup pre-populated so this layer
// stays a pure local-graph builder. Empty BackendName = local-only.
type ProjectArgs struct {
	Name        string
	Description string
	Summary     string

	// BackendName is the backend identifier (e.g. "linear") to stamp on
	// the project node's `backend` metadata key. Empty = local-only path.
	BackendName string

	// RemoteRef carries the backend's identifiers for the newly-created
	// remote project (URL, ID, optional State). Populated by the client
	// intercept after backend.CreateProject succeeds. Zero-valued for
	// local-only.
	RemoteRef backends.RemoteRef

	// RemoteGroup carries the resolved backend group (Linear team, Jira
	// project) the remote project landed under. Populated by the client
	// intercept. Zero-valued for local-only.
	RemoteGroup backends.Group
}

// TicketArgs is the input for CreateTicket.
//
// Backend resolution moved to the client-side intercept
// layer (cmd/knowledge/internal/tools/intercept_create_ticket.go). The
// client calls Linear inline, then forwards create_ticket to the server
// with BackendName / RemoteRef / RemoteGroup / ParentBackendID
// pre-populated so this layer stays a pure local-graph builder. Empty
// BackendName = local-only.
type TicketArgs struct {
	Name        string
	Description string
	Summary     string
	ProjectID   string // required — links ticket to project
	ExternalID  string // optional metadata
	Priority    string // optional metadata
	Labels      string // optional metadata

	// PatternIDs are canonical pattern node IDs the ticket extends. Wired
	// as ticket→pattern EdgeUses edges by BuildTicketNode.
	PatternIDs []string

	// NoPatternsReason is the audited escape hatch when no pattern applies
	// (e.g. trivial doc edit, scaffolding). Stored as ticket-node metadata
	// "no_patterns_reason" by BuildTicketNode.
	NoPatternsReason string

	// ProposedPatterns are not-yet-cataloged patterns this ticket introduces.
	// CreateTicket eagerly creates a NodePattern with status="emerging" for each
	// and links ticket→new-pattern via EdgeUses.
	ProposedPatterns []ProposedPatternArgs

	// LanguagePatterns are language-specific defensive patterns/findings (e.g.,
	// from practice/go) — wired as ticket→<finding|pattern> EdgeAudits edges.
	// Distinct from PatternIDs (architectural prescription); these are
	// vigilance markers for the planner/reviewer. Optional and independent of
	// the PatternIDs/NoPatternsReason/ProposedPatterns tristate.
	LanguagePatterns []string

	// BackendName is the backend identifier (e.g. "linear") to stamp on
	// the ticket node's `backend` metadata key. Empty = local-only path.
	BackendName string

	// RemoteRef carries the backend's identifiers for the newly-created
	// remote ticket (URL, ID, Identifier, optional State). Populated by
	// the client intercept after backend.CreateTicket succeeds.
	RemoteRef backends.RemoteRef

	// RemoteGroup carries the resolved backend group (Linear team) the
	// remote ticket landed under. Inherited from the parent project's
	// metadata by the client intercept.
	RemoteGroup backends.Group

	// ParentBackendID is the parent project's "<backend>_id" metadata
	// value. Written as "<backend>_project_id" on the ticket node when
	// non-empty AND BackendName != "".
	ParentBackendID string
}

// TestPlanArgs is the input for CreateTestPlan.
type TestPlanArgs struct {
	Name    string
	Goal    string
	Summary string
	Steps   []TestStepArgs
}

type TestStepArgs struct {
	Name        string
	Description string
	Summary     string
	Criteria    []CriterionArgs
}
