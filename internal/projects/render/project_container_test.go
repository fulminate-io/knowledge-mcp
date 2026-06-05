// SPDX-License-Identifier: Apache-2.0

// Package render — assembleProjectContainer progress-count tests.
//
// Ported from cmd/knowledge-server/tools/tools_assemble_progress_test.go
// The tests use the fakeGc fixture instead of
// seeding the in-process store; the progress-count logic mirrors
// the server-side contract (closed/completed/skipped count as done).
package render

import (
	"context"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestAssembleProject_TicketProgressCountsClosed seeds a project
// with 5 tickets in 5 distinct statuses and asserts the rendered
// progress line counts closed + completed + skipped as done, and
// open + in_progress as not-done.
func TestAssembleProject_TicketProgressCountsClosed(t *testing.T) {
	project := &knowledgev1.Node{
		Id: "p1", Type: string(kgtypes.NodeProject), SymbolName: "Progress Test",
		Status: kgtypes.StatusActive,
	}
	tClosed := &knowledgev1.Node{Id: "t-c", Type: string(kgtypes.NodeTicket), SymbolName: "t-closed", Status: kgtypes.StatusClosed}
	tCompleted := &knowledgev1.Node{Id: "t-cmp", Type: string(kgtypes.NodeTicket), SymbolName: "t-completed", Status: kgtypes.StatusCompleted}
	tSkipped := &knowledgev1.Node{Id: "t-s", Type: string(kgtypes.NodeTicket), SymbolName: "t-skipped", Status: kgtypes.StatusSkipped}
	tOpen := &knowledgev1.Node{Id: "t-o", Type: string(kgtypes.NodeTicket), SymbolName: "t-open", Status: kgtypes.StatusOpen}
	tInProg := &knowledgev1.Node{Id: "t-ip", Type: string(kgtypes.NodeTicket), SymbolName: "t-inprog", Status: kgtypes.StatusInProgress}

	f := newGraphFixture().
		addKnowledgeNode(project).
		addKnowledgeNode(tClosed).addKnowledgeNode(tCompleted).
		addKnowledgeNode(tSkipped).addKnowledgeNode(tOpen).addKnowledgeNode(tInProg).
		link("p1", "t-c").link("p1", "t-cmp").link("p1", "t-s").
		link("p1", "t-o").link("p1", "t-ip")

	text, err := callRender(context.Background(), f, map[string]any{"id": "p1"})
	require.NoError(t, err)

	assert.Contains(t, text, "**Progress:** 3/5 tickets done",
		"closed/completed/skipped should all count as done")
}

// TestAssembleProject_AllOpenZeroDone asserts the edge case: a
// project with only open tickets shows 0/N done.
func TestAssembleProject_AllOpenZeroDone(t *testing.T) {
	project := &knowledgev1.Node{Id: "p2", Type: string(kgtypes.NodeProject), SymbolName: "All Open"}
	t1 := &knowledgev1.Node{Id: "a", Type: string(kgtypes.NodeTicket), SymbolName: "a", Status: kgtypes.StatusOpen}
	t2 := &knowledgev1.Node{Id: "b", Type: string(kgtypes.NodeTicket), SymbolName: "b", Status: kgtypes.StatusOpen}

	f := newGraphFixture().
		addKnowledgeNode(project).addKnowledgeNode(t1).addKnowledgeNode(t2).
		link("p2", "a").link("p2", "b")

	text, err := callRender(context.Background(), f, map[string]any{"id": "p2"})
	require.NoError(t, err)

	assert.Contains(t, text, "**Progress:** 0/2 tickets done")
}

// TestAssembleProject_NoTicketsOmitsProgress asserts the progress
// line is suppressed when the project has no tickets, rather than
// rendering a misleading "0/0".
func TestAssembleProject_NoTicketsOmitsProgress(t *testing.T) {
	project := &knowledgev1.Node{Id: "p3", Type: string(kgtypes.NodeProject), SymbolName: "Empty Project"}
	f := newGraphFixture().addKnowledgeNode(project)

	text, err := callRender(context.Background(), f, map[string]any{"id": "p3"})
	require.NoError(t, err)

	assert.NotContains(t, text, "Progress:",
		"empty project should not render a progress line")
}
