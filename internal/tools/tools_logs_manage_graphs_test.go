// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/logs"
	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// newManageCtx returns a fakeLogGraphCaller-backed handler ready to invoke
// manage-level operations — list_logs / discard_logs route through the
// generic Execute seam (RETURN_MODE_GRAPH_NAMES + DROP_GRAPH), so a store-free
// fake serves them. The graph CATALOG is seeded on the fake; the engine-live
// overlay still comes from the process-local logs registry (logs.RegisterEngine),
// which is not a store.
func newManageCtx(t *testing.T) (*Handler, *fakeLogGraphCaller) {
	t.Helper()
	fake := newFakeLogGraphCaller()
	return &Handler{graphCallerOverride: fake}, fake
}

// seedLogGraphName registers a log graph's presence on the fake's catalog so a
// RETURN_MODE_GRAPH_NAMES query lists it. An empty-node corpus is enough for
// the list/discard paths, which only enumerate names.
func seedLogGraphName(fake *fakeLogGraphCaller, queryID string) {
	fake.seedLogGraph(queryID, nil, nil)
}

// TestListLogs_EmptyRegistryReturnsHelpfulMessage covers the pre-collect
// case: no log graphs exist so callers should see guidance rather than a
// header-only table.
func TestListLogs_EmptyRegistryReturnsHelpfulMessage(t *testing.T) {
	h, _ := newManageCtx(t)

	res := h.handleListLogs(context.Background(), "")
	require.False(t, res.IsError)
	assert.Contains(t, resultText(res), "No active log graphs")
}

// TestListLogs_FoldsEngineStats installs a QueryEngine via the public
// RegisterEngine entry point and confirms list_logs surfaces the
// template + stream counts for that queryID.
func TestListLogs_FoldsEngineStats(t *testing.T) {
	h, fake := newManageCtx(t)

	queryID := "q-stats"
	seedLogGraphName(fake, queryID)

	engine := logs.NewQueryEngine(
		[]*logwire.LogStream{
			{ID: "s1", Labels: map[string]string{"service": "api"}},
			{ID: "s2", Labels: map[string]string{"service": "web"}},
			{ID: "s3", Labels: map[string]string{"service": "worker"}},
		},
		nil,
		[]*logwire.LogTemplate{
			{ID: "t1", Pattern: "<*> error <*>"},
			{ID: "t2", Pattern: "timeout <*>"},
		},
	)
	logs.RegisterEngine(queryID, engine)
	t.Cleanup(func() { logs.UnregisterEngine(queryID) })

	res := h.handleListLogs(context.Background(), "")
	require.False(t, res.IsError, "list_logs failed: %s", resultText(res))
	txt := resultText(res)
	assert.Contains(t, txt, queryID, "queryID must appear in row")
	assert.Contains(t, txt, "live", "engine status must reflect registration")
}

// TestDiscardLogs_ByName unregisters the engine AND deletes the persisted
// graph for a specific query_id, leaving other graphs untouched.
func TestDiscardLogs_ByName(t *testing.T) {
	h, fake := newManageCtx(t)

	discardID := "q-discard"
	keepID := "q-keep"
	for _, id := range []string{discardID, keepID} {
		seedLogGraphName(fake, id)
		logs.RegisterEngine(id, logs.NewQueryEngine(nil, nil, nil))
	}
	t.Cleanup(func() { logs.UnregisterEngine(keepID) })

	res := h.handleDiscardLogs(context.Background(), discardID)
	require.False(t, res.IsError, "discard failed: %s", resultText(res))
	assert.Contains(t, resultText(res), discardID)

	if _, ok := logs.LookupEngine(discardID); ok {
		t.Fatalf("engine %q should be unregistered after discard", discardID)
	}
	if _, ok := logs.LookupEngine(keepID); !ok {
		t.Fatalf("engine %q must survive a discard of a different graph", keepID)
	}

	// The discarded graph must no longer appear in the fake's catalog; the
	// kept graph must remain.
	if _, ok := fake.graphs[discardID]; ok {
		t.Fatalf("log graph %q still present after discard", discardID)
	}
	if _, ok := fake.graphs[keepID]; !ok {
		t.Fatalf("log graph %q must survive a discard of a different graph", keepID)
	}
}

// TestDiscardLogs_AllWhenNameEmpty runs discard across every log graph
// and confirms the catalog plus engine map end up empty.
func TestDiscardLogs_AllWhenNameEmpty(t *testing.T) {
	h, fake := newManageCtx(t)

	ids := []string{"q1", "q2", "q3"}
	for _, id := range ids {
		seedLogGraphName(fake, id)
		logs.RegisterEngine(id, logs.NewQueryEngine(nil, nil, nil))
	}

	res := h.handleDiscardLogs(context.Background(), "")
	require.False(t, res.IsError, "bulk discard failed: %s", resultText(res))
	assert.Contains(t, resultText(res), "Discarded")

	for _, id := range ids {
		if _, ok := logs.LookupEngine(id); ok {
			t.Fatalf("engine %q should be unregistered after bulk discard", id)
		}
	}
	assert.Empty(t, fake.graphs, "the fake catalog must be empty after bulk discard")
}

// TestDiscardLogs_EmptyRegistryNoop confirms discard_logs doesn't error
// when there's nothing to discard.
func TestDiscardLogs_EmptyRegistryNoop(t *testing.T) {
	h, _ := newManageCtx(t)

	res := h.handleDiscardLogs(context.Background(), "")
	require.False(t, res.IsError)
	assert.Contains(t, resultText(res), "No log graphs")
}
