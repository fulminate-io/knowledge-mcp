// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

// segment_delta_refusal_test.go proves the behind-window refusal SURVIVES the
// client's own error path: it reaches the reconcile loop with its code intact,
// through the %w wrap, and a plain error does not masquerade as one.
//
// === THE WIRE CONTRACT THIS TEST IS SHAPED BY ===
// Only the CODE and the MESSAGE cross the wire. The client does not receive the
// server's error object — connect rebuilds one from the code and message, so the
// server's error CHAIN dies at serialization. Three consequences bind this test:
//
//  1. connect.CodeOf IS THE ONLY DISCRIMINATOR THAT EXISTS across the process
//     boundary. errors.Is against a server-side sentinel is ALWAYS false
//     client-side, so no assertion here may reference one.
//  2. THE FAKE MUST BE WIRE-SHAPED, not server-shaped. An in-process fake handing
//     over the server's rich error object would let sentinel assertions pass in
//     test while production — which only ever holds the rebuilt wire error — never
//     matches. This harness is better than a fake: the reconcile client is built
//     over a REAL httptest connect server, so the refusal genuinely round-trips
//     and arrives rebuilt exactly as production receives it.
//  3. THE MESSAGE IS FOR LOGS ONLY. Nothing here parses it, and the server side
//     is free to reword it.
//
// connect.CodeOf UNWRAPS, which is the whole point: MergeSegmentDelta wraps the
// scan error with %w, so a type assertion on the top-level error would miss the
// refusal while CodeOf resolves through it. The error is therefore injected at the
// SCANNER, so it travels the real wrap rather than being handed to the assertion
// directly.

// TestDeltaScanRefusalReachesTheLoopUnretried pins both directions: a
// CodeOutOfRange from the scan arrives with its code intact, and a plain error
// does not resolve to that code.
func TestDeltaScanRefusalReachesTheLoopUnretried(t *testing.T) {
	ctx := opCtx()
	const (
		repo     = "refusalRepo"
		embedded = 100
	)

	c, eng := buildReconcileClientWith(t, embedded, repo)

	// THE REFUSAL, constructed as the SERVER would send it. It round-trips through
	// the harness's real connect server, so what the client's pager receives is the
	// rebuilt wire error — no wrapped cause, code preserved.
	eng.setScanErr(connect.NewError(connect.CodeOutOfRange,
		errors.New("erasure journal trimmed past the caller's position")))

	_, err := tools.MergeSegmentDelta(
		ctx, c.PipelineScanner(), c.SegmentShipper(), c.segmentMgr, c.segmentMgr,
		kgtypes.GraphCode, repo, 1_600_000_000_000_000_000)
	require.Error(t, err, "the seeded refusal must surface as an error from the delta merge")
	require.Equal(t, connect.CodeOutOfRange, connect.CodeOf(err),
		"the refusal's code must survive the client's %%w wrap — connect.CodeOf unwraps, which is why the classification must use it rather than a type assertion on the top-level error")

	// THE KNOWN-NEGATIVE. Without it this test passes against an implementation
	// that treats EVERY scan failure as a refusal — which would put every transient
	// network blip onto the full-rebuild path, the most expensive possible
	// misclassification.
	eng.setScanErr(errors.New("boom"))
	_, plainErr := tools.MergeSegmentDelta(
		ctx, c.PipelineScanner(), c.SegmentShipper(), c.segmentMgr, c.segmentMgr,
		kgtypes.GraphCode, repo, 1_600_000_000_000_000_000)
	require.Error(t, plainErr, "the plain error must still surface as an error")
	require.NotEqual(t, connect.CodeOutOfRange, connect.CodeOf(plainErr),
		"a plain scan failure must NOT resolve to CodeOutOfRange, or every transient blip routes to a full rebuild")
}
