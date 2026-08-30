// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"

	"connectrpc.com/connect"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// segment_reconcile_stats_test.go holds the fake engine's STATS surface: the
// coverage counters it serves and the levers a test moves them with.
//
// IT IS A SEPARATE FILE FROM THE ENGINE ITSELF so that one stays inside the repo's
// per-file line cap. The split is by SUBJECT rather than by size — everything here
// concerns one RPC and the fields behind it.

// Stats serves the embedded (binary-vector) count under e.mu.
//
// THE LOCK IS REQUIRED, not defensive. setEmbedded below lets a test move this count
// MID-RUN — which is how a dead-vector reap's effect on the server-side count is
// simulated — and this method is served on the connect handler's goroutine while that
// write happens on the test's. Reading it unguarded is a genuine race that -race
// reports.
func (e *reconcileEngine) Stats(
	context.Context, *connect.Request[knowledgev1.StatsRequest],
) (*connect.Response[knowledgev1.StatsResponse], error) {
	e.mu.Lock()
	e.statsCalls++
	embedded, embedFail, holding := e.embedded, e.embedFailures, e.embedFailuresHoldingVec
	e.mu.Unlock()
	return connect.NewResponse(&knowledgev1.StatsResponse{
		GraphStats: &knowledgev1.GraphStats{
			BinaryVectorCount: embedded,
			EmbedFailureCount: embedFail,
			// A NIL holding pointer is served as an ABSENT field, which is exactly how
			// a server that does not compute the subset answers. Substituting a zero
			// here would make the absent case indistinguishable from a measured none
			// and every presence assertion vacuous.
			EmbedFailureHoldingVectorCount: holding,
		},
	}), nil
}

// statsCallCount reports how many Stats reads this engine has served.
func (e *reconcileEngine) statsCallCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.statsCalls
}

// setEmbedFailures scripts the marked-failure count and the holding subset Stats
// serves. A nil subset models a server that does not report it at all — the
// version-skew shape the client's caveat must survive.
func (e *reconcileEngine) setEmbedFailures(marked int32, holdingSubset *int32) {
	e.mu.Lock()
	e.embedFailures, e.embedFailuresHoldingVec = marked, holdingSubset
	e.mu.Unlock()
}

// setEmbedded moves the served vector count, so a test can model a reap that actually
// removed something server-side. Without it a fake reap could only CLAIM a removal, and
// the "the reap closed the gap" branch would be unreachable.
func (e *reconcileEngine) setEmbedded(n int32) {
	e.mu.Lock()
	e.embedded = n
	e.mu.Unlock()
}
