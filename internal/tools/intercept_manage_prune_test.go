// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// TestInterceptManage_Prune_RequiresGraph asserts prune rejects an empty graph
// (and ONLY that — no allowlist) before firing any RPC.
func TestInterceptManage_Prune_RequiresGraph(t *testing.T) {
	ix := &fakeIndexer{}
	handled, res := manageCall(t, ix, `{"operation":"prune"}`)
	require.True(t, handled)
	assert.True(t, res.IsError, "prune without graph must error")
	assert.Contains(t, toolResultText(res), "requires")
	assert.Empty(t, ix.requests(), "no Index RPC when the required graph is missing")
}

// TestInterceptManage_Prune_AllTombstones asserts a graph-only prune (no before)
// fires ONE INDEX_OP_PRUNE RPC with before_nanos=0 and renders the count.
func TestInterceptManage_Prune_AllTombstones(t *testing.T) {
	ix := &fakeIndexer{affectedCount: 7}
	handled, res := manageCall(t, ix, `{"operation":"prune","graph":"knowledge"}`)
	require.True(t, handled)
	require.False(t, res.IsError, "prune: %s", toolResultText(res))

	require.EqualValues(t, 1, ix.indexCalls.Load(), "exactly one prune Index RPC")
	reqs := ix.requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, knowledgev1.IndexRequest_INDEX_OP_PRUNE, reqs[0].GetOperation())
	assert.Equal(t, "knowledge", reqs[0].GetTarget().GetGraph())
	assert.EqualValues(t, 0, reqs[0].GetBeforeNanos(), "no before => prune all (0 cutoff)")

	body := toolResultText(res)
	assert.Contains(t, body, "Pruned 7 tombstoned node(s)")
	assert.Contains(t, body, "all tombstones")
}

// TestInterceptManage_Prune_GenericGraphRouting asserts prune routes a non-code,
// non-knowledge graph (practice) generically via the Language selector — no
// allowlist gate.
func TestInterceptManage_Prune_GenericGraphRouting(t *testing.T) {
	ix := &fakeIndexer{affectedCount: 2}
	handled, res := manageCall(t, ix, `{"operation":"prune","graph":"practice","name":"go"}`)
	require.True(t, handled)
	require.False(t, res.IsError, "prune: %s", toolResultText(res))

	reqs := ix.requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, knowledgev1.IndexRequest_INDEX_OP_PRUNE, reqs[0].GetOperation())
	assert.Equal(t, "practice", reqs[0].GetTarget().GetGraph())
	assert.Equal(t, "go", reqs[0].GetTarget().GetLanguage(), "practice routes name via Language")
}

// TestInterceptManage_Prune_RelativeBefore asserts a relative window ("24h") is
// lowered to an absolute unix-nanos cutoff roughly 24h in the past.
func TestInterceptManage_Prune_RelativeBefore(t *testing.T) {
	ix := &fakeIndexer{affectedCount: 1}
	before := time.Now()
	handled, res := manageCall(t, ix, `{"operation":"prune","graph":"knowledge","before":"24h"}`)
	after := time.Now()
	require.True(t, handled)
	require.False(t, res.IsError, "prune: %s", toolResultText(res))

	reqs := ix.requests()
	require.Len(t, reqs, 1)
	cutoff := reqs[0].GetBeforeNanos()
	lo := before.Add(-24 * time.Hour).UnixNano()
	hi := after.Add(-24 * time.Hour).UnixNano()
	assert.GreaterOrEqual(t, cutoff, lo, "cutoff is ~24h before now (lower bound)")
	assert.LessOrEqual(t, cutoff, hi, "cutoff is ~24h before now (upper bound)")
}

// TestInterceptManage_Prune_RFC3339Before asserts an absolute RFC3339 timestamp
// is parsed straight to its unix-nanos.
func TestInterceptManage_Prune_RFC3339Before(t *testing.T) {
	ix := &fakeIndexer{affectedCount: 1}
	const ts = "2026-01-02T15:04:05Z"
	handled, res := manageCall(t, ix, `{"operation":"prune","graph":"knowledge","before":"`+ts+`"}`)
	require.True(t, handled)
	require.False(t, res.IsError, "prune: %s", toolResultText(res))

	want, perr := time.Parse(time.RFC3339, ts)
	require.NoError(t, perr)
	reqs := ix.requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, want.UnixNano(), reqs[0].GetBeforeNanos(), "RFC3339 before parses to its exact unix-nanos")
}

// TestInterceptManage_Prune_GarbageBefore asserts an unparseable before is a
// validation error with no RPC fired.
func TestInterceptManage_Prune_GarbageBefore(t *testing.T) {
	ix := &fakeIndexer{}
	handled, res := manageCall(t, ix, `{"operation":"prune","graph":"knowledge","before":"not-a-duration"}`)
	require.True(t, handled)
	assert.True(t, res.IsError, "unparseable before must error")
	assert.Contains(t, toolResultText(res), "unparseable before")
	assert.Empty(t, ix.requests(), "no Index RPC on a bad before")
}
