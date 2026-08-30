// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// segment_reemit_report_test.go is the gate on the failure REPORT of the shipped-
// corpus re-emit, for both paths that drive it: mutate(delete) and manage(prune).
//
// THE DEFECT IT REFUSES. reEmitDeletedFromSegments returned nothing and both acks
// were fixed strings, so a failed DeleteFromBuckets was unreachable to the caller
// BY CONSTRUCTION — no error, no field, no varied text. The rows were gone from
// the graph while every one of those documents stayed resident in this client's
// shipped blobs, and the caller was told the operation had simply succeeded.
//
// EVERY CASE CARRIES ITS KNOWN-NEGATIVE, in the same test. An assertion that the
// qualifier is PRESENT on failure is satisfied by an implementation that appends
// it to every result; the clean leg is what refuses that. And both legs assert the
// original ack SURVIVES, because a report that replaced the success text would be
// its own false statement — the delete did land.

// deleteWithSegmentDeleter drives a plain (non-backend) mutate(delete) end to end
// through InterceptMutate with a SegmentDeleter wired, which is the only way the
// re-emit's verdict can reach the rendered result.
func deleteWithSegmentDeleter(t *testing.T, del SegmentDeleter) kgtools.ToolResult {
	t.Helper()
	fc := &fakeGraphCaller{
		// No backend metadata: the archive loop skips the id and the tombstone
		// forward is what runs, which is the ordinary knowledge-graph delete.
		queryResponses: map[string]kgtools.ToolResult{
			"n-1": nodeResultJSON(t, "n-1", "finding", nil),
		},
		mutateResult: kgtools.ToolResult{Content: []kgtools.ContentBlock{{Type: "text", Text: "ok"}}},
	}
	handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc, deleter: del}, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"delete","ids":["n-1"]}`),
	})
	require.True(t, handled, "the delete arm claims this call")
	return res
}

// TestMutateDelete_SegmentReEmitFailureIsReported is the mutate half. This path
// had NO test of its own before — the swallow was pinned only on the prune side,
// so the arm most callers reach could report a non-durable delete as a clean one
// with nothing going red.
func TestMutateDelete_SegmentReEmitFailureIsReported(t *testing.T) {
	t.Run("a failed re-emit is named in the result", func(t *testing.T) {
		del := &fakeSegmentDeleter{err: errors.New("bucket delete boom")}
		var res kgtools.ToolResult
		logged := captureSlog(func() { res = deleteWithSegmentDeleter(t, del) })

		body := toolResultText(res)
		assert.False(t, res.IsError,
			"the tombstone is committed server-side — reporting the delete as FAILED would be a "+
				"second false statement, not a fix for the first")
		assert.Contains(t, body, "tombstoned 1 node(s)", "the delete that DID land is still reported")
		assert.Contains(t, body, "shipped segment corpus was NOT updated",
			"an unqualified success here is the defect: the caller believes the removal is durable "+
				"in the shipped blobs when it is not")
		assert.Contains(t, body, "bucket delete boom", "the cause is named, not just the fact")
		assert.Len(t, del.recorded(), 1, "the seam was driven; the error came from it")
		assert.Contains(t, logged, "segment delete re-emit failed", "the log line survives the change")
	})

	t.Run("known-negative: a clean re-emit carries no qualifier", func(t *testing.T) {
		del := &fakeSegmentDeleter{}
		res := deleteWithSegmentDeleter(t, del)
		body := toolResultText(res)
		require.False(t, res.IsError)
		assert.Contains(t, body, "tombstoned 1 node(s)")
		assert.NotContains(t, body, "shipped segment corpus was NOT updated",
			"without this leg the assertion above is satisfied by appending the warning to every "+
				"delete, which would report a durable removal as undurable")
	})
}
