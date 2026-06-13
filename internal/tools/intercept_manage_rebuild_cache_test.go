// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// TestInterceptManage_RebuildCache_FiresOneRPC asserts a code rebuild_cache fires
// exactly ONE Index RPC with INDEX_OP_REBUILD_CACHE for the named repo and renders
// the async STARTED acknowledgement (the op is now async — the server drops +
// re-derives on a background goroutine and no derived count is known at return).
func TestInterceptManage_RebuildCache_FiresOneRPC(t *testing.T) {
	ix := &fakeIndexer{affectedCount: 0}
	handled, res := manageCall(t, ix, `{"operation":"rebuild_cache","graph":"code","name":"myrepo"}`)
	require.True(t, handled)
	require.False(t, res.IsError, "rebuild_cache: %s", toolResultText(res))

	require.EqualValues(t, 1, ix.indexCalls.Load(), "exactly one rebuild_cache Index RPC")
	reqs := ix.requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, knowledgev1.IndexRequest_INDEX_OP_REBUILD_CACHE, reqs[0].GetOperation())
	assert.Equal(t, "code", reqs[0].GetTarget().GetGraph())
	assert.Equal(t, "myrepo", reqs[0].GetTarget().GetRepo())

	body := toolResultText(res)
	assert.Contains(t, body, "started", "async rebuild renders a started acknowledgement")
	assert.Contains(t, body, "code/myrepo")
	assert.Contains(t, body, "rebuild_cache.complete", "points the operator at the completion log line")
}

// TestInterceptManage_RebuildCache_RequiresSupportedGraphAndName asserts the
// builtin-graph gate: graph=knowledge (no name) is ACCEPTED — it fires exactly one
// INDEX_OP_REBUILD_CACHE RPC with a knowledge Target (name defaulted to "default")
// and renders a started-ack; graph=code with no name is rejected (code requires a
// repo); graph=practice is rejected before any RPC; and a knowledge "@"-overlay
// name is rejected (base layer only in v1, symmetric with rebuild_segments).
// Reverting the Phase 3 client gate fails the knowledge-accepted case.
func TestInterceptManage_RebuildCache_RequiresSupportedGraphAndName(t *testing.T) {
	// graph=knowledge with no name is accepted — the name defaults to "default" and
	// one knowledge-Target RPC fires.
	ix := &fakeIndexer{affectedCount: 0}
	handled, res := manageCall(t, ix, `{"operation":"rebuild_cache","graph":"knowledge"}`)
	require.True(t, handled)
	require.False(t, res.IsError, "rebuild_cache graph=knowledge must be accepted: %s", toolResultText(res))
	require.EqualValues(t, 1, ix.indexCalls.Load(), "exactly one rebuild_cache Index RPC for knowledge")
	reqs := ix.requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, knowledgev1.IndexRequest_INDEX_OP_REBUILD_CACHE, reqs[0].GetOperation())
	assert.Equal(t, "knowledge", reqs[0].GetTarget().GetGraph(), "the RPC carries a knowledge Target")
	assert.Empty(t, reqs[0].GetTarget().GetRepo(), "the knowledge default instance needs no repo field on the wire")
	body := toolResultText(res)
	assert.Contains(t, body, "started")
	assert.Contains(t, body, "knowledge/default", "a nameless knowledge rebuild renders the default instance")

	// graph=code with no name is rejected — code still requires a repo.
	ix2 := &fakeIndexer{}
	handled2, res2 := manageCall(t, ix2, `{"operation":"rebuild_cache","graph":"code"}`)
	require.True(t, handled2)
	assert.True(t, res2.IsError, "rebuild_cache graph=code without a repo name must error")
	assert.Empty(t, ix2.requests(), "no Index RPC when the repo name is missing")

	// graph=practice is rejected before any RPC — the content-hash caches are
	// code+knowledge only.
	ix3 := &fakeIndexer{}
	handled3, res3 := manageCall(t, ix3, `{"operation":"rebuild_cache","graph":"practice","name":"go"}`)
	require.True(t, handled3)
	assert.True(t, res3.IsError, "rebuild_cache graph=practice must be rejected")
	assert.Empty(t, ix3.requests(), "no Index RPC for a non-code/non-knowledge graph")

	// A knowledge "@"-overlay name is rejected (base layer only in v1, mirroring
	// rebuild_segments so the two operator levers treat overlay names symmetrically).
	ix4 := &fakeIndexer{}
	handled4, res4 := manageCall(t, ix4, `{"operation":"rebuild_cache","graph":"knowledge","name":"default@session-x"}`)
	require.True(t, handled4)
	assert.True(t, res4.IsError, "a knowledge overlay name must be rejected for rebuild_cache")
	assert.Contains(t, toolResultText(res4), "overlay rebuilds not supported in v1",
		"the rejection names the v1 base-layer-only boundary, symmetric with rebuild_segments")
	assert.Empty(t, ix4.requests(), "no Index RPC for a knowledge overlay name")
}
