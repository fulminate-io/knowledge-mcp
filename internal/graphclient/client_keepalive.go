// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"log/slog"
	"time"

	"connectrpc.com/connect"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// healthPinger is the narrow interface satisfied by the generated
// knowledgev1connect.HealthServiceClient — used so the keepalive
// loop can be driven by a mock in tests without spinning up an
// httptest server per unit case.
type healthPinger interface {
	Check(ctx context.Context, req *connect.Request[knowledgev1.HealthCheckRequest]) (*connect.Response[knowledgev1.HealthCheckResponse], error)
}

// StartKeepalive launches a background goroutine that pings
// HealthService.Check every 30s and logs escalating failure counts.
// Purpose: drop detection + operator visibility. http2.Transport
// redials on the NEXT real request transparently, so the keepalive
// does not itself "heal" the connection — it surfaces drops in slog
// so the operator sees the problem before the next user tool call
// lands.
//
// Intended to be called once during MCP client construction. The
// goroutine exits when ctx cancels.
func (c *GraphClient) StartKeepalive(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	go func() {
		defer ticker.Stop()
		c.keepaliveLoopWith(ctx, ticker.C, c.health)
	}()
}

// keepaliveLoopWith is the testable seam: it takes a <-chan time.Time
// (production passes ticker.C; tests pass a hand-driven channel) and
// a healthPinger (production passes c.health; tests pass a mock).
//
// pingCtx timeout = 2s. Rationale: the unary reconnect interceptor
// retries over ~4.25s for user-triggered calls. A 2s pingCtx on each
// 30s keepalive tick means one tick = one ping attempt, no stacked
// retries; if the ping fails, the next tick 30s later tries again.
// The interceptor's retry budget is reserved for user-initiated
// calls, not heartbeats.
func (c *GraphClient) keepaliveLoopWith(
	ctx context.Context,
	ticks <-chan time.Time,
	pinger healthPinger,
) {
	consecutive := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			_, err := pinger.Check(pingCtx,
				connect.NewRequest(&knowledgev1.HealthCheckRequest{}))
			cancel()
			if err != nil {
				consecutive++
				if consecutive == 2 {
					slog.Warn("knowledge-server keepalive failing",
						"consecutive_failures", consecutive, "err", err)
				} else if consecutive == 5 {
					slog.Error("knowledge-server keepalive: server appears unreachable",
						"consecutive_failures", consecutive, "err", err)
				}
				continue
			}
			if consecutive > 0 {
				slog.Info("knowledge-server keepalive recovered",
					"after_failures", consecutive)
				consecutive = 0
			}
		}
	}
}
