// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"testing"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// fixedTime is the deterministic timestamp the examine fixtures use
// for CreatedAt/UpdatedAt so the scrubbers produce stable bytes. Under the
// value-embed flip knowledgev1.Node.CreatedAt/UpdatedAt are int64 unix-nanos, so
// this carries the nanos directly.
var fixedTime = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC).UnixNano()

// seedExamineFinding seeds an orphan finding (matches the "no
// parent" + "no edges" path in examine_finding.golden).
func seedExamineFinding() (*parityGraphFixture, string) {
	f := newParityFixture()
	id := "000000000000000000000000000000f0"
	f.add(&knowledgev1.Node{
		Id: id, Type: string(kgtypes.NodeFinding), SymbolName: "ex-finding",
		Status: "active", Source: "test",
		Description: "finding desc", Summary: "finding sum",
		CreatedAt: fixedTime, UpdatedAt: fixedTime,
	})
	return f, id
}

// seedExamineDecision seeds an orphan decision.
func seedExamineDecision() (*parityGraphFixture, string) {
	f := newParityFixture()
	id := "000000000000000000000000000000d0"
	n := knowledgev1.Node{
		Id: id, Type: string(kgtypes.NodeDecision), SymbolName: "ex-decision",
		Status: "active", Source: "test",
		Summary:   "decision sum",
		CreatedAt: fixedTime, UpdatedAt: fixedTime,
	}
	kgtypes.SetValue(&n, "choice", "option A")
	kgtypes.SetValue(&n, "rationale", "rationale text")
	f.add(&n)
	return f, id
}

// seedExamineProject seeds an orphan project. ID must be pure hex
// (32 chars) so scrubAll's regex matches it.
func seedExamineProject() (*parityGraphFixture, string) {
	f := newParityFixture()
	id := "000000000000000000000000000000a0"
	f.add(&knowledgev1.Node{
		Id: id, Type: string(kgtypes.NodeProject), SymbolName: "ex-project",
		Status: "active", Source: "llm:claude",
		Description: "ex project desc", Summary: "ex project sum",
		CreatedAt: fixedTime, UpdatedAt: fixedTime,
	})
	return f, id
}

// seedExamineStep seeds a step with full ancestry (project →
// ticket → plan → phase → step) so the ancestry walk in
// renderExamineAncestry has 4 levels to walk.
func seedExamineStep() (*parityGraphFixture, string) {
	f := newParityFixture()
	stepID := "000000000000000000000000000000c1"
	phaseID := "000000000000000000000000000000c2"
	planID := "000000000000000000000000000000c3"
	ticketID := "000000000000000000000000000000c4"
	projID := "000000000000000000000000000000c5"

	f.add(&knowledgev1.Node{Id: stepID, Type: string(kgtypes.NodeStep), SymbolName: "ex-step",
		Status: "pending", Source: "llm:claude",
		Description: "step desc", Summary: "step sum",
		CreatedAt: fixedTime, UpdatedAt: fixedTime})
	f.add(&knowledgev1.Node{Id: phaseID, Type: string(kgtypes.NodePhase), SymbolName: "ex-phase",
		Status: "pending", Source: "llm:claude",
		Description: "ph over", Summary: "ph sum",
		CreatedAt: fixedTime, UpdatedAt: fixedTime})
	f.add(&knowledgev1.Node{Id: planID, Type: string(kgtypes.NodePlan), SymbolName: "ex-plan",
		Status: "active", Source: "llm:claude",
		Description: "plan goal", Summary: "ex plan sum",
		CreatedAt: fixedTime, UpdatedAt: fixedTime})
	f.add(&knowledgev1.Node{Id: ticketID, Type: string(kgtypes.NodeTicket), SymbolName: "ex-pticket",
		Status: "open", Source: "llm:claude",
		Description: "tp desc", Summary: "tp sum",
		CreatedAt: fixedTime, UpdatedAt: fixedTime})
	f.add(&knowledgev1.Node{Id: projID, Type: string(kgtypes.NodeProject), SymbolName: "ex-pproj",
		Status: "active", Source: "llm:claude",
		Description: "pp desc", Summary: "pp sum",
		CreatedAt: fixedTime, UpdatedAt: fixedTime})

	f.link(phaseID, stepID)
	f.link(planID, phaseID)
	f.link(ticketID, planID)
	f.link(projID, ticketID)

	return f, stepID
}

// seedExamineRule seeds an orphan rule. Note: AddRule does NOT set
// Status (only ProjectArgs / TicketArgs / PlanArgs do), so the
// fixture's Status field is intentionally empty to match the golden.
func seedExamineRule() (*parityGraphFixture, string) {
	f := newParityFixture()
	id := "000000000000000000000000000000b0"
	n := knowledgev1.Node{
		Id: id, Type: string(kgtypes.NodeRule), SymbolName: "ex-rule",
		Source:      "llm:claude",
		Description: "rule desc", Summary: "ex-rule rule summary",
		CreatedAt: fixedTime, UpdatedAt: fixedTime,
	}
	kgtypes.SetValue(&n, "scope", "*.go")
	kgtypes.SetValue(&n, "enforcement", "lint")
	f.add(&n)
	return f, id
}

// TestInterceptQueryExamineProjects_OrphanShape covers the
// no-parent-no-edges sub-shapes (finding, decision, project, rule)
// across both text and json formats. Each fixture exercises the
// (no parent — orphan node) + (no edges) message paths.
func TestInterceptQueryExamineProjects_Finding_TextFormat(t *testing.T) {
	f, id := seedExamineFinding()
	runExamineParity(t, f, id, "", "examine_finding")
}
func TestInterceptQueryExamineProjects_Finding_JSONFormat(t *testing.T) {
	f, id := seedExamineFinding()
	runExamineParity(t, f, id, "json", "examine_finding.json")
}
func TestInterceptQueryExamineProjects_Decision_TextFormat(t *testing.T) {
	f, id := seedExamineDecision()
	runExamineParity(t, f, id, "", "examine_decision")
}
func TestInterceptQueryExamineProjects_Decision_JSONFormat(t *testing.T) {
	f, id := seedExamineDecision()
	runExamineParity(t, f, id, "json", "examine_decision.json")
}
func TestInterceptQueryExamineProjects_Project_TextFormat(t *testing.T) {
	f, id := seedExamineProject()
	runExamineParity(t, f, id, "", "examine_project")
}
func TestInterceptQueryExamineProjects_Project_JSONFormat(t *testing.T) {
	f, id := seedExamineProject()
	runExamineParity(t, f, id, "json", "examine_project.json")
}
func TestInterceptQueryExamineProjects_Rule_TextFormat(t *testing.T) {
	f, id := seedExamineRule()
	runExamineParity(t, f, id, "", "examine_rule")
}
func TestInterceptQueryExamineProjects_Rule_JSONFormat(t *testing.T) {
	f, id := seedExamineRule()
	runExamineParity(t, f, id, "json", "examine_rule.json")
}
func TestInterceptQueryExamineProjects_Step_TextFormat(t *testing.T) {
	f, id := seedExamineStep()
	runExamineParity(t, f, id, "", "examine_step")
}
func TestInterceptQueryExamineProjects_Step_JSONFormat(t *testing.T) {
	f, id := seedExamineStep()
	runExamineParity(t, f, id, "json", "examine_step.json")
}

func TestInterceptQueryExamineProjects_NonProjectDomain_FallsThrough(t *testing.T) {
	f := newParityFixture()
	id := "00000000000000000000000000000abc"
	f.add(&knowledgev1.Node{Id: id, Type: string(kgtypes.NodeFile), SymbolName: "file.go", Status: "active"})

	deps := &parityDeps{gc: f.gc()}
	args := mustMarshal(t, map[string]any{"mode": "examine", "id": id})
	handled, _ := InterceptQueryExamineProjects(deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	assert.False(t, handled, "NodeFile must fall through to server-side renderer")
}

func TestInterceptQueryExamineProjects_Thought_FallsThrough(t *testing.T) {
	f := newParityFixture()
	id := "00000000000000000000000000000111"
	f.add(&knowledgev1.Node{Id: id, Type: string(kgtypes.NodeThought), SymbolName: "a thought", Status: "active"})

	deps := &parityDeps{gc: f.gc()}
	args := mustMarshal(t, map[string]any{"mode": "examine", "id": id})
	handled, _ := InterceptQueryExamineProjects(deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	assert.False(t, handled, "NodeThought must fall through to server-side handleExamine")
}

func TestInterceptQueryExamineProjects_NonKnowledgeGraph_FallsThrough(t *testing.T) {
	f := newParityFixture()
	deps := &parityDeps{gc: f.gc()}
	args := mustMarshal(t, map[string]any{
		"mode": "examine", "id": "any", "graph": "cloud",
	})
	handled, _ := InterceptQueryExamineProjects(deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	assert.False(t, handled, "non-knowledge graphs must fall through")
}

func TestInterceptQueryExamineProjects_EmptyID_FallsThrough(t *testing.T) {
	deps := &parityDeps{gc: newParityFixture().gc()}
	args := mustMarshal(t, map[string]any{"mode": "examine"})
	handled, _ := InterceptQueryExamineProjects(deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	assert.False(t, handled, "empty id must fall through so server emits canonical error")
}

func TestInterceptQueryExamineProjects_WrongMode_FallsThrough(t *testing.T) {
	deps := &parityDeps{gc: newParityFixture().gc()}
	args := mustMarshal(t, map[string]any{"mode": "evidence", "id": "x"})
	handled, _ := InterceptQueryExamineProjects(deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	assert.False(t, handled)
}

func TestInterceptQueryExamineProjects_WrongTool_FallsThrough(t *testing.T) {
	deps := &parityDeps{gc: newParityFixture().gc()}
	args := mustMarshal(t, map[string]any{"mode": "examine", "id": "x"})
	handled, _ := InterceptQueryExamineProjects(deps, kgtools.CallToolParams{Name: "search", Arguments: args})
	assert.False(t, handled)
}

// runExamineParity is the shared per-fixture body for the byte-
// parity assertions. Picks up the golden by name and compares with
// the scrubbed intercept output.
func runExamineParity(t *testing.T, f *parityGraphFixture, id, format, goldenName string) {
	t.Helper()
	deps := &parityDeps{gc: f.gc()}
	args := mustMarshal(t, map[string]any{
		"mode": "examine", "id": id, "format": format,
	})
	handled, res := InterceptQueryExamineProjects(deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	require.True(t, handled)
	require.False(t, res.IsError, "intercept error: %v", res.Content)

	got := scrubForParity(extractText(res))
	want := readGolden(t, goldenName)
	assert.Equal(t, want, got)
}
