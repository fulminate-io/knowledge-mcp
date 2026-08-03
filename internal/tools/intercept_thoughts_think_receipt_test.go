// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// thinkArgsJSON marshals a think call's arguments, so a content body carrying
// literal tool-call markup needs no hand-escaping in the test source.
func thinkArgsJSON(t *testing.T, fields map[string]any) json.RawMessage {
	t.Helper()
	fields["operation"] = "think"
	b, err := json.Marshal(fields)
	require.NoError(t, err)
	return b
}

// TestHandleThinkClient_ParamShapedTail_WarnsButWrites is the observability
// regression for the silent born-link narrowing: when a caller's serialization
// leaves parameter-like markup as literal text at the END of content, the
// parameter it names never reached the tool and no edge was written. The tool
// cannot recover the parameter — but it CAN say so. The warning is non-fatal by
// contract: the thought is still created and the call still succeeds.
func TestHandleThinkClient_ParamShapedTail_WarnsButWrites(t *testing.T) {
	fc := &fakeGraphCaller{mutateIDs: []string{"th-leaky"}}
	deps := interceptTestDeps{gc: fc}

	res := handleThinkClient(context.Background(), deps, kgtools.CallToolParams{
		Name: "thoughts",
		Arguments: thinkArgsJSON(t, map[string]any{
			"content": "a hypothesis whose serialization went wrong at the end\n" +
				"</content>\n<parameter name=\"session\">some-topic",
			"summary": "leaked-parameter tail gist",
		}),
	})
	require.False(t, res.IsError, "a param-shaped tail must NEVER refuse the write: %s", toolResultText(res))
	body := toolResultText(res)

	// The write still landed.
	require.Len(t, fc.execMutations, 1, "the thought create must still fire")
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_CREATE, fc.execMutations[0].GetKind())
	assert.Contains(t, body, "th-leaky", "the thought id is still returned")

	// And the tail is flagged, naming the parameter the markup mentions. Pinned
	// verbatim: the wording is the whole deliverable here, and it has to stay
	// precise about scope — it says the TEXT applied nothing, never that the named
	// parameter is missing from the write (which would contradict the receipt in
	// the sibling case below).
	assert.Contains(t, body, "## Warnings", "the tail warning rides the standard client warnings section")
	assert.Contains(t, body, "⚠ content ends with parameter-like markup mentioning: session"+
		" — text inside content is never interpreted as tool parameters, so nothing in that text was applied."+
		" If any of it was meant as a parameter, re-send it as a real tool parameter; the write receipt above"+
		" states what actually landed.", "the advisory text, verbatim")
}

// TestHandleThinkClient_ParamShapedTail_CorrectCallStillProceeds is the
// false-positive guard: a CORRECT call may legitimately carry the same markup
// inside content (a thought documenting the tool grammar, for instance) while
// emitting its parameters properly. Such a call must proceed, its session must
// still land, and the receipt must report the landed session — the warning is
// advisory, never a verdict about what was written.
func TestHandleThinkClient_ParamShapedTail_CorrectCallStillProceeds(t *testing.T) {
	fc := &fakeGraphCaller{
		nodeMatchResults: map[graphKey][]*knowledgev1.Node{
			{Type: "knowledge"}: {
				{Id: "sess-doc", Type: string(kgtypes.NodeThoughtSession), SymbolName: "grammar-notes"},
			},
		},
		mutateIDs: []string{"th-documenting"},
	}
	deps := interceptTestDeps{gc: fc}

	res := handleThinkClient(context.Background(), deps, kgtools.CallToolParams{
		Name: "thoughts",
		Arguments: thinkArgsJSON(t, map[string]any{
			"content": "documenting the malformed shape verbatim:\n" +
				"</content>\n<parameter name=\"session\">grammar-notes",
			"summary": "documenting the malformed tool-call shape",
			"session": "grammar-notes",
		}),
	})
	require.False(t, res.IsError, "a correct call carrying the markup must succeed: %s", toolResultText(res))
	body := toolResultText(res)

	assert.Contains(t, body, "## Warnings", "the advisory warning still fires on the shape")
	// The receipt is the authority on what landed, and it disagrees with a naive
	// reading of the warning: the session DID land here.
	assert.Contains(t, body, "Session: grammar-notes → sess-doc",
		"the receipt reports the session node the contains edge was written from")
}

// TestHandleThinkClient_WriteReceipt asserts the render tail is a receipt of
// OUTCOMES rather than an echo of the caller's arguments: the resolved session
// node id, whether the ticket contains edge was written, how many links resolved
// vs stayed unresolved, and the born-link count. Each number has a known-positive
// here (a resolvable link AND an unresolvable one, a resolvable code referent),
// so a counter that never increments cannot pass.
func TestHandleThinkClient_WriteReceipt(t *testing.T) {
	const repo = "knowledge"
	const ref = "tools/wire.go:PersistBatch"
	fc := &fakeGraphCaller{
		listGraphsResult: listGraphsResultJSON(t, [2]string{"code", repo}),
		queryResponsesByGraphName: map[graphKey]map[string]kgtools.ToolResult{
			{Type: "code", Name: repo}: {
				ref: nodeResultJSON(t, ref, "function_declaration", nil),
			},
		},
		queryResponses: map[string]kgtools.ToolResult{
			"tkt-7":  nodeResultJSON(t, "tkt-7", "ticket", nil),
			"link-a": nodeResultJSON(t, "link-a", "finding", nil),
			// "ghost-link" resolves nowhere.
		},
		nodeMatchResults: map[graphKey][]*knowledgev1.Node{
			{Type: "knowledge"}: {
				{Id: "sess-1", Type: string(kgtypes.NodeThoughtSession), SymbolName: "design"},
			},
		},
		mutateIDs: []string{"th-receipt"},
	}
	deps := interceptTestDeps{gc: fc}

	res := handleThinkClient(context.Background(), deps, kgtools.CallToolParams{
		Name: "thoughts",
		Arguments: thinkArgsJSON(t, map[string]any{
			"content":   "a thought citing " + ref + " while carrying full context",
			"summary":   "receipt smoke gist",
			"session":   "design",
			"ticket_id": "tkt-7",
			"links":     []string{"link-a", "ghost-link"},
		}),
	})
	require.False(t, res.IsError, "the fully-decorated think must succeed: %s", toolResultText(res))
	body := toolResultText(res)

	assert.Contains(t, body, "Thought recorded → ID: th-receipt")
	assert.Contains(t, body, "Session: design → sess-1", "session NAME and resolved node ID")
	assert.Contains(t, body, "Ticket: tkt-7 → contains edge written", "the ticket edge outcome, not the argument")
	assert.Contains(t, body, "Links: 1 resolved, 1 unresolved", "per-link resolution outcome")
	assert.Contains(t, body, "Code born-links: 1", "born-link edge count")
}

// TestHandleThinkClient_Receipt_NoSessionShape is the tell the two damaged lanes
// missed: today an absent session renders as an ABSENT line, which is easy to
// overlook. The receipt must state "none" explicitly for every context param the
// call did not land, so a silently-narrowed write is visible rather than merely
// unmentioned.
func TestHandleThinkClient_Receipt_NoSessionShape(t *testing.T) {
	fc := &fakeGraphCaller{mutateIDs: []string{"th-bare"}}
	deps := interceptTestDeps{gc: fc}

	res := handleThinkClient(context.Background(), deps, kgtools.CallToolParams{
		Name: "thoughts",
		Arguments: thinkArgsJSON(t, map[string]any{
			"content": "a thought with no context parameters at all",
			"summary": "bare think gist",
		}),
	})
	require.False(t, res.IsError, "%s", toolResultText(res))
	body := toolResultText(res)

	// The WHOLE body, verbatim: every context parameter states its none/0 outcome
	// and a clean call carries no warnings section. Pinned as an equality rather
	// than a set of Contains checks because the property under test is that
	// nothing is OMITTED — and a Contains suite cannot observe an omission.
	assert.Equal(t, "Thought recorded → ID: th-bare\n"+
		"Session: none\n"+
		"Ticket: none\n"+
		"Links: 0 resolved, 0 unresolved\n"+
		"Code born-links: 0\n"+
		"Branches from: none", body)
}

// TestHandleThinkClient_UnresolvableTicket_ReceiptSaysNotLinked pairs with
// TestHandleThinkClient_AbsentTicket_ThoughtStillCreated: the drop is
// fail-tolerant by contract, and the receipt is where that drop becomes visible
// to the caller instead of only to the server log.
func TestHandleThinkClient_UnresolvableTicket_ReceiptSaysNotLinked(t *testing.T) {
	fc := &fakeGraphCaller{mutateIDs: []string{"th-noticket"}}
	deps := interceptTestDeps{gc: fc}

	res := handleThinkClient(context.Background(), deps, kgtools.CallToolParams{
		Name: "thoughts",
		Arguments: thinkArgsJSON(t, map[string]any{
			"content":   "an orphan-ticket thought",
			"summary":   "orphan-ticket receipt gist",
			"ticket_id": "ghost-ticket",
		}),
	})
	require.False(t, res.IsError, "an absent ticket must not fail the think: %s", toolResultText(res))
	assert.Contains(t, toolResultText(res), "Ticket: ghost-ticket → NOT linked",
		"a dropped ticket link is reported in the receipt")
}
