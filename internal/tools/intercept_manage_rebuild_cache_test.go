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

// TestInterceptManage_RebuildCache_RequiresCodeAndName asserts rebuild_cache
// rejects a non-code graph and an empty name before firing any RPC.
func TestInterceptManage_RebuildCache_RequiresCodeAndName(t *testing.T) {
	ix := &fakeIndexer{}
	handled, res := manageCall(t, ix, `{"operation":"rebuild_cache","graph":"knowledge"}`)
	require.True(t, handled)
	assert.True(t, res.IsError, "rebuild_cache with a non-code graph must error")
	assert.Empty(t, ix.requests(), "no Index RPC for a non-code graph")

	ix2 := &fakeIndexer{}
	handled2, res2 := manageCall(t, ix2, `{"operation":"rebuild_cache","graph":"code"}`)
	require.True(t, handled2)
	assert.True(t, res2.IsError, "rebuild_cache without a repo name must error")
	assert.Empty(t, ix2.requests(), "no Index RPC when the repo name is missing")
}
