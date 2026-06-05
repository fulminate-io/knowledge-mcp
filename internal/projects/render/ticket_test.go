// SPDX-License-Identifier: Apache-2.0

// Package render — assembleTicket / assemblePattern tests for T4/T5
// pattern-catalog context surfacing.
//
// Ported from cmd/knowledge-server/tools/tools_assemble_containers_test.go
// (Phase 5): tests use a fakeGc fixture instead of seeding the
// in-process store, so they live alongside the client-side render
// package they exercise.
package render

import (
	"context"
	"strings"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// callAssembleTicket runs Handle against a ticket node and returns
// the rendered text. The fixture must include the ticket at
// ticketID.
func callAssembleTicket(t *testing.T, f *graphFixture, ticketID string) string {
	t.Helper()
	text, err := callRender(context.Background(), f, map[string]any{"id": ticketID})
	require.NoError(t, err)
	return text
}

// TestAssembleTicket_RendersPatterns covers the happy path — a
// ticket that uses one real pattern plus one unresolved (bogus)
// pattern ID. Resolved pattern name must appear in the
// `## Patterns` section; unresolved IDs must surface under the
// `⚠ **Unresolved pattern IDs:**` line; and the section header
// itself must be present.
func TestAssembleTicket_RendersPatterns(t *testing.T) {
	const patID = "pat-1"
	const ticketID = "t-1"
	const bogus = "deadbeefdeadbeef"

	ticket := &knowledgev1.Node{
		Id:         ticketID,
		Type:       string(kgtypes.NodeTicket),
		SymbolName: "t-patterns",
		Status:     kgtypes.StatusOpen,
	}
	kgtypes.SetValue(ticket, "unresolved_pattern_ids", bogus)

	pattern := &knowledgev1.Node{
		Id: patID, Type: string(kgtypes.NodePattern), SymbolName: "ResolvedPattern",
		Status: "active",
	}

	f := newGraphFixture().
		addKnowledgeNode(ticket).
		addKnowledgeNode(pattern).
		addKnowledgeEdge(ticketID, patID, kgtypes.EdgeUses)

	text := callAssembleTicket(t, f, ticketID)

	assert.Contains(t, text, "## Patterns",
		"rendered output must carry the `## Patterns` section header")
	assert.Contains(t, text, "ResolvedPattern",
		"resolved pattern node name must be listed")
	assert.Contains(t, text, patID,
		"resolved pattern ID must be listed for planner reference")
	assert.Contains(t, text, "⚠",
		"unresolved pattern IDs line must be prefixed with ⚠")
	assert.Contains(t, text, "Unresolved pattern IDs:",
		"unresolved pattern IDs line must use the canonical header text")
	assert.Contains(t, text, bogus,
		"each unresolved ID must be named in the output")
}

// TestAssembleTicket_RendersNoPatternsReason covers the audited
// escape hatch — a ticket that declares no pattern applies.
func TestAssembleTicket_RendersNoPatternsReason(t *testing.T) {
	const ticketID = "t-2"
	ticket := &knowledgev1.Node{
		Id:         ticketID,
		Type:       string(kgtypes.NodeTicket),
		SymbolName: "t-no-patterns",
		Status:     kgtypes.StatusOpen,
	}
	kgtypes.SetValue(ticket, "no_patterns_reason", "trivial typo fix")

	f := newGraphFixture().addKnowledgeNode(ticket)
	text := callAssembleTicket(t, f, ticketID)

	assert.Contains(t, text, "## Patterns")
	assert.Contains(t, text, "No patterns reason:")
	assert.Contains(t, text, "trivial typo fix")
}

// TestAssembleTicket_EmptyPatternSignal covers the silent-omission
// guard. A ticket with no pattern context at all must still render
// the `## Patterns` section with an explicit placeholder.
func TestAssembleTicket_EmptyPatternSignal(t *testing.T) {
	const ticketID = "t-3"
	ticket := &knowledgev1.Node{
		Id:         ticketID,
		Type:       string(kgtypes.NodeTicket),
		SymbolName: "t-empty-patterns",
		Status:     kgtypes.StatusOpen,
	}
	f := newGraphFixture().addKnowledgeNode(ticket)
	text := callAssembleTicket(t, f, ticketID)

	assert.Contains(t, text, "## Patterns",
		"empty ticket still renders the section")
	assert.Contains(t, text, "no pattern context",
		"empty-signal placeholder must be present")
	assert.NotContains(t, strings.ToLower(text), "⚠",
		"empty ticket must not emit a warning marker")
}

// TestAssemblePattern_RendersFullTree pins the happy-path rendering
// of the granular pattern shape. The pattern carries applies-when +
// avoid-when use_cases, an example, and a reference.
func TestAssemblePattern_RendersFullTree(t *testing.T) {
	const patID = "pat-tree"
	pat := &knowledgev1.Node{
		Id: patID, Type: string(kgtypes.NodePattern), SymbolName: "fan-out-fan-in",
		Summary:     "Distribute work across goroutines then collect results.",
		Description: "Fan-out splits a stream onto N workers; fan-in multiplexes the results back.",
		Status:      "active",
	}
	uc1 := &knowledgev1.Node{Id: "uc-1", Type: string(kgtypes.NodeUseCase), SymbolName: "CPU-bound stage",
		Description: "The stage is CPU-bound and the workload is partitionable."}
	uc2 := &knowledgev1.Node{Id: "uc-2", Type: string(kgtypes.NodeUseCase), SymbolName: "Independent inputs",
		Description: "Inputs can be processed independently without cross-talk."}
	uc3 := &knowledgev1.Node{Id: "uc-3", Type: string(kgtypes.NodeUseCase), SymbolName: "Order-sensitive output",
		Description: "Downstream consumers require input order to be preserved end-to-end."}
	ex := &knowledgev1.Node{Id: "ex-1", Type: string(kgtypes.NodeExample), SymbolName: "basic fan-out",
		Content: "// example code\nfor i := 0; i < n; i++ { go worker(ch) }"}
	kgtypes.SetValue(ex, "language", "go")
	kgtypes.SetValue(ex, "attribution", "MIT — kat-co/concurrency-in-go-src")
	ref := &knowledgev1.Node{Id: "ref-1", Type: string(kgtypes.NodeReference), SymbolName: "Concurrency in Go"}
	kgtypes.SetValue(ref, "url", "https://example.com/book")

	// Place pattern + family in knowledge graph (the simplest case;
	// FromPracticeGraph covers the practice-graph path).
	f := newGraphFixture().
		addKnowledgeNode(pat).
		addKnowledgeNode(uc1).addKnowledgeNode(uc2).addKnowledgeNode(uc3).
		addKnowledgeNode(ex).addKnowledgeNode(ref).
		addKnowledgeEdge(patID, "uc-1", kgtypes.EdgeAppliesWhen).
		addKnowledgeEdge(patID, "uc-2", kgtypes.EdgeAppliesWhen).
		addKnowledgeEdge(patID, "uc-3", kgtypes.EdgeAvoidWhen).
		addKnowledgeEdge(patID, "ex-1", kgtypes.EdgeKGContains).
		addKnowledgeEdge(patID, "ref-1", kgtypes.EdgeReferences)

	text, err := callRender(context.Background(), f, map[string]any{"id": patID})
	require.NoError(t, err)

	assert.Contains(t, text, "# Pattern: fan-out-fan-in")
	assert.Contains(t, text, "Distribute work across goroutines then collect results.")
	assert.Contains(t, text, "Fan-out splits a stream onto N workers")
	assert.Contains(t, text, "## Applies when")
	assert.Contains(t, text, "CPU-bound stage")
	assert.Contains(t, text, "Independent inputs")
	assert.Contains(t, text, "The stage is CPU-bound")
	assert.Contains(t, text, "## Avoid when")
	assert.Contains(t, text, "Order-sensitive output")
	assert.Contains(t, text, "## Examples")
	assert.Contains(t, text, "```go")
	assert.Contains(t, text, "// example code")
	assert.Contains(t, text, "Source: MIT — kat-co/concurrency-in-go-src")
	assert.Contains(t, text, "## References")
	assert.Contains(t, text, "https://example.com/book")
}

// TestAssemblePattern_FromPracticeGraph exercises the practice-graph
// fallback in resolveAssembleNode + the cross-graph IterEdgesIn /
// FetchNodeIn calls in assemblePatternIn.
func TestAssemblePattern_FromPracticeGraph(t *testing.T) {
	const patID = "pat-practice"
	pat := &knowledgev1.Node{
		Id: patID, Type: string(kgtypes.NodePattern), SymbolName: "confinement",
		Summary:     "Ensure a value is only accessible from a single goroutine.",
		Description: "Confinement restricts a piece of data so that only one goroutine can reach it at a time.",
	}
	uc := &knowledgev1.Node{
		Id: "uc-confinement", Type: string(kgtypes.NodeUseCase),
		SymbolName:  "worker producing a local buffer",
		Description: "A goroutine builds up a slice locally; no other goroutine touches it.",
	}

	f := newGraphFixture().
		addNode("practice", "design-patterns", pat).
		addNode("practice", "design-patterns", uc).
		addEdge("practice", "design-patterns", &knowledgev1.Edge{
			FromId: patID, ToId: "uc-confinement", Type: string(kgtypes.EdgeAppliesWhen),
		})

	text, err := callRender(context.Background(), f, map[string]any{"id": patID})
	require.NoError(t, err)

	assert.Contains(t, text, "# Pattern: confinement")
	assert.Contains(t, text, "## Applies when")
	assert.Contains(t, text, "worker producing a local buffer")
}

// TestAssemblePattern_EmptySectionsOmitted pins the absence
// contract: a bare pattern with no children renders header +
// summary + description + ID and NONE of the four section headers.
func TestAssemblePattern_EmptySectionsOmitted(t *testing.T) {
	const patID = "pat-bare"
	pat := &knowledgev1.Node{
		Id: patID, Type: string(kgtypes.NodePattern), SymbolName: "bare-pattern",
		Summary:     "A pattern with no children yet.",
		Description: "Placeholder for the empty-sections contract.",
		Status:      "active",
	}
	f := newGraphFixture().addKnowledgeNode(pat)
	text, err := callRender(context.Background(), f, map[string]any{"id": patID})
	require.NoError(t, err)

	assert.Contains(t, text, "# Pattern: bare-pattern")
	assert.Contains(t, text, "A pattern with no children yet.")
	assert.Contains(t, text, "Placeholder for the empty-sections contract.")
	assert.Contains(t, text, "ID: "+patID)
	assert.NotContains(t, text, "## Applies when")
	assert.NotContains(t, text, "## Avoid when")
	assert.NotContains(t, text, "## Examples")
	assert.NotContains(t, text, "## References")
}
