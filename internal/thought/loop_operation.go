// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// newPropagationBaseContext mints the propagation loop's daemon-lifetime context
// and stamps the query-origin operation on it. NewPropagationLoop calls it; it
// lives beside the loop rather than inside it only because loop.go is at the
// file-length cap.
//
// The stamp belongs on this ONE context because it is the root every pass
// derives from (baseContext → runPass, runClusterDetection), and the loop is a
// background job with no originating tool call to inherit an operation from —
// unstamped, its whole hourly pass reports as client.unstamped, which in the
// metrics is indistinguishable from a real stamping bug elsewhere.
//
// It is a FLOOR, not a ceiling: the per-call-site stamps already in this package
// (reflect_gen.go's probe, loop_corpus.go's CorpusDelta drain) sit below it and
// still win by innermost-wins. What this newly attributes is the pass work with
// no narrower term of its own — the thought browse, the bulk edge read, the node
// hydrate, and the full-corpus metadata writeback.
func newPropagationBaseContext() (context.Context, context.CancelFunc) {
	//nolint:gosec // G118: the cancel is stored on the loop and invoked by Stop.
	return context.WithCancel(
		graphclient.WithOperation(context.Background(), graphclient.OpPropagationReflect))
}
