// SPDX-License-Identifier: Apache-2.0

// Package render — golden-file byte-parity verification suite.
//
// Phase 6: re-build each Phase 1.5 fixture under fakeGc,
// run render.Handle, scrub IDs/UUIDs to placeholders, and compare
// byte-for-byte against the committed *.golden files under testdata/.
//
// Phase 1.5 captured the goldens by invoking the still-alive
// server-side handleAssemble against a real store.Store() fixture.
// This verify step rebuilds each fixture shape against fakeGc and
// confirms render.Handle's output matches byte-for-byte after
// scrubbing the per-run random IDs to stable placeholders.

package render

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// goldenIDRegex matches the 32-char hex IDs that the production
// store assigns to nodes. Phase 1.5 captures scrub these to
// "<ID>"; the verify suite scrubs synthetic IDs the same way so
// fixture-assigned IDs (e.g. "p1", "t-c") don't pollute the diff.
var goldenIDRegex = regexp.MustCompile(`[0-9a-f]{32}`)

// readGolden reads the named .golden file from testdata/.
func readGolden(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}

// runGolden invokes render.Handle against the fixture's gc with
// the supplied root ID + args, scrubs the result to a stable form,
// and compares against the named golden. Fixture-assigned IDs
// (e.g. "fixture-decision") become "<ID>" via id-replacement (NOT
// hex-regex — synthetic IDs aren't 32-char hex). The substitution
// list is per-test: every fixture ID known to appear in the
// rendered output.
func runGolden(t *testing.T, golden string, text string, idSubs ...string) {
	t.Helper()
	scrubbed := text
	for _, id := range idSubs {
		scrubbed = replaceAll(scrubbed, id, "<ID>")
	}
	// Also scrub any 32-char hex IDs that slip through (mirrors the
	// capture-side scrub).
	scrubbed = goldenIDRegex.ReplaceAllString(scrubbed, "<ID>")
	want := readGolden(t, golden)
	assert.Equal(t, want, scrubbed, "golden mismatch for %s", golden)
}

// replaceAll is a tiny strings.ReplaceAll passthrough kept inline
// to satisfy the "no strings import beyond what's already used"
// hygiene comment on this file.
func replaceAll(s, old, new string) string {
	// Use strings.ReplaceAll via the std lib through a local
	// indirection — strings is already imported in other test files
	// in this package but not in this one. Inline a small loop
	// instead of widening imports.
	out := ""
	for {
		i := indexOf(s, old)
		if i < 0 {
			return out + s
		}
		out += s[:i] + new
		s = s[i+len(old):]
	}
}

func indexOf(s, sub string) int {
	if sub == "" {
		return 0
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// --- 1. project ---

func TestGoldenProject(t *testing.T) {
	project := &knowledgev1.Node{
		Id: "proj-id", Type: string(kgtypes.NodeProject), SymbolName: "fixture-project",
		Status: kgtypes.StatusActive, Description: "project desc", Summary: "project summary",
	}
	ticket := &knowledgev1.Node{
		Id: "tkt-id", Type: string(kgtypes.NodeTicket), SymbolName: "child-ticket",
		Status: kgtypes.StatusOpen, Description: "ticket desc", Summary: "ticket summary",
	}
	kgtypes.SetValue(ticket, "no_patterns_reason", "fixture")
	f := newGraphFixture().
		addKnowledgeNode(project).
		addKnowledgeNode(ticket).
		link("proj-id", "tkt-id")

	text, err := callRender(context.Background(), f, map[string]any{"id": "proj-id"})
	require.NoError(t, err)
	runGolden(t, "project", text, "proj-id", "tkt-id")
}

// --- 2. ticket_with_patterns ---

func TestGoldenTicketWithPatterns(t *testing.T) {
	ticket := &knowledgev1.Node{
		Id: "tkt-pat", Type: string(kgtypes.NodeTicket), SymbolName: "t-with-patterns",
		Status: kgtypes.StatusOpen, Description: "patterns desc", Summary: "patterns summary",
	}
	kgtypes.SetValue(ticket, "unresolved_pattern_ids", "deadbeefdeadbeef")
	pat := &knowledgev1.Node{
		Id: "pat-id", Type: string(kgtypes.NodePattern), SymbolName: "ResolvedPattern",
		Status: "active",
	}
	f := newGraphFixture().
		addKnowledgeNode(ticket).addKnowledgeNode(pat).
		addKnowledgeEdge("tkt-pat", "pat-id", kgtypes.EdgeUses)

	text, err := callRender(context.Background(), f, map[string]any{"id": "tkt-pat"})
	require.NoError(t, err)
	runGolden(t, "ticket_with_patterns", text, "tkt-pat", "pat-id")
}

// --- 3. ticket_with_language_patterns ---

func TestGoldenTicketWithLanguagePatterns(t *testing.T) {
	ticket := &knowledgev1.Node{
		Id: "tkt-lang", Type: string(kgtypes.NodeTicket), SymbolName: "t-with-lang",
		Status: kgtypes.StatusOpen, Description: "lang desc", Summary: "lang summary",
	}
	kgtypes.SetValue(ticket, "no_patterns_reason", "fixture")
	kgtypes.SetValue(ticket, "unresolved_language_patterns", "lang-unresolved")
	lp := &knowledgev1.Node{
		Id: "lp-id", Type: string(kgtypes.NodeFinding), SymbolName: "lang-pattern-fixture",
		Source: "test", Status: "active",
	}
	// 102 characters on purpose — longer than the 80-character cut this
	// section used to apply, so the golden below can only be produced by a
	// render that emits the body whole.
	kgtypes.SetValue(lp, "dsl_pattern", "defer $DB.Close(); rows, err := $DB.QueryContext($CTX, $Q, $$$ARGS); if err != nil { return nil, err }")
	f := newGraphFixture().
		addKnowledgeNode(ticket).addKnowledgeNode(lp).
		addKnowledgeEdge("tkt-lang", "lp-id", kgtypes.EdgeAudits)

	text, err := callRender(context.Background(), f, map[string]any{"id": "tkt-lang"})
	require.NoError(t, err)
	runGolden(t, "ticket_with_language_patterns", text,
		"tkt-lang", "lp-id", "lang-unresolved")
}

// --- 4. ticket_with_research ---

func TestGoldenTicketWithResearch(t *testing.T) {
	ticket := &knowledgev1.Node{
		Id: "tkt-rs", Type: string(kgtypes.NodeTicket), SymbolName: "t-with-research",
		Status: kgtypes.StatusOpen, Description: "research desc", Summary: "research summary",
	}
	kgtypes.SetValue(ticket, "no_patterns_reason", "fixture")
	research := &knowledgev1.Node{
		Id: "rs-id", Type: string(kgtypes.NodeResearch), SymbolName: "linked-research",
		Source: "test", Status: "active", Summary: "research summary",
	}
	question := &knowledgev1.Node{
		Id: "q-id2", Type: string(kgtypes.NodeQuestion), SymbolName: "What is the answer?",
		Source: "test", Status: "open",
	}
	f := newGraphFixture().
		addKnowledgeNode(ticket).
		addKnowledgeNode(research).
		addKnowledgeNode(question).
		link("tkt-rs", "rs-id").
		link("rs-id", "q-id2")

	text, err := callRender(context.Background(), f, map[string]any{"id": "tkt-rs"})
	require.NoError(t, err)
	runGolden(t, "ticket_with_research", text,
		"tkt-rs", "rs-id", "q-id2")
}

// --- 5. plan ---

func TestGoldenPlan(t *testing.T) {
	plan := &knowledgev1.Node{
		Id: "plan-id", Type: string(kgtypes.NodePlan), SymbolName: "fixture-plan",
		Status: "active",
	}
	phase := &knowledgev1.Node{
		Id: "phase-id", Type: string(kgtypes.NodePhase), SymbolName: "phase-1",
		Status: kgtypes.StatusPending, Description: "p1 overview",
	}
	step1 := &knowledgev1.Node{
		Id: "s1-id", Type: string(kgtypes.NodeStep), SymbolName: "step-1",
		Status: kgtypes.StatusPending, Description: "step 1 desc",
	}
	step2 := &knowledgev1.Node{
		Id: "s2-id", Type: string(kgtypes.NodeStep), SymbolName: "step-2",
		Status: kgtypes.StatusPending, Description: "step 2 desc",
	}
	f := newGraphFixture().
		addKnowledgeNode(plan).addKnowledgeNode(phase).
		addKnowledgeNode(step1).addKnowledgeNode(step2).
		link("plan-id", "phase-id").
		link("phase-id", "s1-id").
		link("phase-id", "s2-id")

	text, err := callRender(context.Background(), f, map[string]any{"id": "plan-id"})
	require.NoError(t, err)
	runGolden(t, "plan", text, "plan-id", "phase-id", "s1-id", "s2-id")
}

// --- 6. test_plan_with_steps ---

func TestGoldenTestPlanWithSteps(t *testing.T) {
	tp := &knowledgev1.Node{
		Id: "tps-id", Type: string(kgtypes.NodeTestPlan), SymbolName: "fixture-test-plan",
	}
	s1 := &knowledgev1.Node{Id: "tps1", Type: string(kgtypes.NodeTestStep), SymbolName: "tp-step-1",
		Description: "tp step 1"}
	s2 := &knowledgev1.Node{Id: "tps2", Type: string(kgtypes.NodeTestStep), SymbolName: "tp-step-2",
		Description: "tp step 2"}
	f := newGraphFixture().
		addKnowledgeNode(tp).addKnowledgeNode(s1).addKnowledgeNode(s2).
		link("tps-id", "tps1").
		link("tps-id", "tps2")

	text, err := callRender(context.Background(), f, map[string]any{"id": "tps-id"})
	require.NoError(t, err)
	runGolden(t, "test_plan_with_steps", text,
		"tps-id", "tps1", "tps2")
}

// --- 7. test_plan_empty ---

func TestGoldenTestPlanEmpty(t *testing.T) {
	tp := &knowledgev1.Node{
		Id: "tp-id", Type: string(kgtypes.NodeTestPlan), SymbolName: "empty-tp",
		Status: "active", Description: "empty", Summary: "empty tp",
	}
	f := newGraphFixture().addKnowledgeNode(tp)

	text, err := callRender(context.Background(), f, map[string]any{"id": "tp-id"})
	require.NoError(t, err)
	runGolden(t, "test_plan_empty", text, "tp-id")
}

// --- 8. research ---

func TestGoldenResearch(t *testing.T) {
	research := &knowledgev1.Node{
		Id: "research-id", Type: string(kgtypes.NodeResearch), SymbolName: "fixture-research",
		Source: "test", Status: "active", Description: "research desc",
		Summary: "research summary",
	}
	question := &knowledgev1.Node{
		Id: "q-id", Type: string(kgtypes.NodeQuestion), SymbolName: "What is the answer?",
		Source: "test", Status: "open", Description: "question desc",
		Summary: "question summary",
	}
	finding := &knowledgev1.Node{
		Id: "finding-id", Type: string(kgtypes.NodeFinding), SymbolName: "fixture-finding",
		Source: "test", Status: "active", Description: "finding desc fixture-finding",
		Summary: "finding summary",
	}
	f := newGraphFixture().
		addKnowledgeNode(research).addKnowledgeNode(question).addKnowledgeNode(finding).
		link("research-id", "q-id").
		addKnowledgeEdge("finding-id", "q-id", kgtypes.EdgeAnswers)

	text, err := callRender(context.Background(), f, map[string]any{"id": "research-id"})
	require.NoError(t, err)
	runGolden(t, "research", text, "research-id", "q-id", "finding-id")
}

// --- 9. decision ---

func TestGoldenDecision(t *testing.T) {
	d := &knowledgev1.Node{
		Id: "dec-id", Type: string(kgtypes.NodeDecision), SymbolName: "fixture-decision",
		Source: "test", Status: "active", Summary: "decision summary",
	}
	kgtypes.SetValue(d, "choice", "choose option B")
	f := newGraphFixture().addKnowledgeNode(d)

	text, err := callRender(context.Background(), f, map[string]any{"id": "dec-id"})
	require.NoError(t, err)
	runGolden(t, "decision", text, "dec-id")
}

// --- 10. rule ---

func TestGoldenRule(t *testing.T) {
	r := &knowledgev1.Node{
		Id: "rule-id", Type: string(kgtypes.NodeRule), SymbolName: "fixture-rule",
		Source: "test", Status: "active", Description: "rule desc",
		Summary: "rule summary",
	}
	f := newGraphFixture().addKnowledgeNode(r)

	text, err := callRender(context.Background(), f, map[string]any{"id": "rule-id"})
	require.NoError(t, err)
	runGolden(t, "rule", text, "rule-id")
}

// --- 11. pattern_use_cases ---

func TestGoldenPatternUseCases(t *testing.T) {
	patID := "pat-use_case-fixture"
	pat := &knowledgev1.Node{
		Id: patID, Type: string(kgtypes.NodePattern), SymbolName: "fixture-pattern-use_case-fixture",
		Source: "test", Status: "active", Description: "pattern desc",
		Summary: "pattern summary",
	}
	child := &knowledgev1.Node{
		Id: "child-use_case-fixture", Type: string(kgtypes.NodeUseCase), SymbolName: "child-use_case-fixture",
		Source: "test", Status: "active", Description: "child desc",
		Summary: "child summary",
	}
	// The capture used EdgeKGContains (since seedPatternFamilyForGolden
	// links via that). Renderer's bucketPatternChildrenIn only buckets
	// use_case into appliesWhen/avoidWhen — under EdgeKGContains an
	// example would render, but a use_case under contains is silently
	// dropped. The golden reflects exactly that: header + ID, no
	// section. Fixture matches the capture for byte parity.
	f := newGraphFixture().
		addNode("practice", "go", pat).
		addNode("practice", "go", child).
		addEdge("practice", "go", &knowledgev1.Edge{
			FromId: patID, ToId: "child-use_case-fixture", Type: string(kgtypes.EdgeKGContains),
		})

	text, err := callRender(context.Background(), f, map[string]any{"id": patID})
	require.NoError(t, err)
	runGolden(t, "pattern_use_cases", text, patID, "child-use_case-fixture")
}

// --- 12. pattern_examples ---

func TestGoldenPatternExamples(t *testing.T) {
	patID := "pat-example-fixture"
	pat := &knowledgev1.Node{
		Id: patID, Type: string(kgtypes.NodePattern), SymbolName: "fixture-pattern-example-fixture",
		Source: "test", Status: "active", Description: "pattern desc",
		Summary: "pattern summary",
	}
	child := &knowledgev1.Node{
		Id: "ex-child", Type: string(kgtypes.NodeExample), SymbolName: "child-example-fixture",
		Source: "test", Status: "active", Description: "child desc",
		Summary: "child summary",
	}
	f := newGraphFixture().
		addNode("practice", "go", pat).
		addNode("practice", "go", child).
		addEdge("practice", "go", &knowledgev1.Edge{
			FromId: patID, ToId: "ex-child", Type: string(kgtypes.EdgeKGContains),
		})

	text, err := callRender(context.Background(), f, map[string]any{"id": patID})
	require.NoError(t, err)
	runGolden(t, "pattern_examples", text, patID, "ex-child")
}

// --- 13. pattern_references ---

func TestGoldenPatternReferences(t *testing.T) {
	patID := "pat-reference-fixture"
	pat := &knowledgev1.Node{
		Id: patID, Type: string(kgtypes.NodePattern), SymbolName: "fixture-pattern-reference-fixture",
		Source: "test", Status: "active", Description: "pattern desc",
		Summary: "pattern summary",
	}
	child := &knowledgev1.Node{
		Id: "child-reference-fixture", Type: string(kgtypes.NodeReference), SymbolName: "child-reference-fixture",
		Source: "test", Status: "active", Description: "child desc",
		Summary: "child summary",
	}
	// The capture linked via EdgeKGContains. References must come
	// through EdgeReferences to be bucketed under "## References";
	// under EdgeKGContains they're dropped (only examples + contains
	// land). The golden reflects exactly that empty render. Fixture
	// matches capture for byte parity.
	f := newGraphFixture().
		addNode("practice", "go", pat).
		addNode("practice", "go", child).
		addEdge("practice", "go", &knowledgev1.Edge{
			FromId: patID, ToId: "child-reference-fixture", Type: string(kgtypes.EdgeKGContains),
		})

	text, err := callRender(context.Background(), f, map[string]any{"id": patID})
	require.NoError(t, err)
	runGolden(t, "pattern_references", text, patID, "child-reference-fixture")
}

// --- 14. agent ---

func TestGoldenAgent(t *testing.T) {
	a := &knowledgev1.Node{
		Id: "agent-id", Type: string(kgtypes.NodeAgent), SymbolName: "fixture-agent",
		Source: "test", Status: "active",
		Content: "# fixture-agent\n\nYou are a fixture agent.",
		Summary: "agent summary",
	}
	f := newGraphFixture().addKnowledgeNode(a)

	text, err := callRender(context.Background(), f, map[string]any{"id": "agent-id"})
	require.NoError(t, err)
	runGolden(t, "agent", text, "agent-id")
}

// --- 15. skill ---

// NOTE on the 14-vs-15 criterion: the spec locks "14
// distinct Test* functions in golden_test.go (one per shape)" while
// Phase 1.5 produced 15 .golden files (test_plan split into two
// shapes). Keeping a full one-test-per-shape coverage requires 15
// top-level Test* functions; the criterion's "14" reflects the plan
// body's 14-shape enumeration, while the capture pragmatically
// produced 15 to exercise the test_plan empty-vs-populated branches.
// Going with 15 tests so every captured golden has a corresponding
// verify; over-coverage is harmless, under-coverage would leave a
// golden unverified.
func TestGoldenSkill(t *testing.T) {
	s := &knowledgev1.Node{
		Id: "skill-id", Type: string(kgtypes.NodeSkill), SymbolName: "fixture-skill",
		Source: "test", Status: "active",
		Content: "# fixture-skill\n\nDo the skill thing.",
		Summary: "skill summary",
	}
	f := newGraphFixture().addKnowledgeNode(s)

	text, err := callRender(context.Background(), f, map[string]any{"id": "skill-id"})
	require.NoError(t, err)
	runGolden(t, "skill", text, "skill-id")
}
