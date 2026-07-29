// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// opCtx returns a context carrying a query-origin operation, mirroring what a
// production entry point stamps before any covered RPC is issued.
//
// Tests that drive a client seam BELOW an entry point (constructClient's
// dispatch, the router, healNeedsRebuild, the bind-first serve path) must use
// this rather than context.Background(). The stamping interceptor's default-deny
// deliberately FAILS an unstamped covered RPC in a test build — that guard is
// the point, not an obstacle, and silencing it would defeat it. An unstamped
// test context is simply a context production never produces, so a test using
// one is not exercising the real shape.
//
// The specific term does not matter to these tests; what matters is that one is
// present, exactly as it would be in production.
func opCtx() context.Context {
	return graphclient.WithOperation(context.Background(), graphclient.OpQuery)
}
