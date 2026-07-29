// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"context"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// opCtx returns a context carrying a query-origin operation, mirroring what a
// production entry point stamps before any covered RPC is issued.
//
// Tests that drive a client seam below an entry point must use this rather than
// context.Background(). The stamping interceptor's default-deny deliberately
// FAILS an unstamped covered RPC in a test build — that guard is the point, not
// an obstacle, and silencing it would defeat it. An unstamped context is simply
// one production never produces, so a test using it is not exercising the real
// shape. The specific term does not matter here; its presence does.
func opCtx() context.Context {
	return graphclient.WithOperation(context.Background(), graphclient.OpQuery)
}
