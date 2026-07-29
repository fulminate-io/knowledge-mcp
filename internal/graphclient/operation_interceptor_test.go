// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
)

// stampTestOp / stampTestOtherOp are local to this file on purpose: these tests
// exercise the stamping MECHANISM, not the vocabulary (which has its own
// enumerating test), so they must not go red merely because a term was renamed.
const (
	stampTestOp      Operation = "search"
	stampTestOtherOp Operation = "collect.chunk"
)

// newStampHarness stands up an in-process h2c httptest.Server serving BOTH the
// Engine and Health handlers and returns a real GraphClient pointed at it plus
// the health-call counter. HealthService is the deliberate carve-out from the
// covered set — its request messages carry no client_context field — so it has
// to be served for real here to prove the stamper leaves it alone rather than
// rejecting it. The health handler is the existing stubHealth (reconnect tests),
// scripted to always succeed.
//
// Going through NewGraphClientForURL is the point of this harness, not an
// incidental convenience: calling the interceptor function directly would pass
// even if nobody had installed it, which is exactly the defect that would let
// every RPC leave unlabeled. Only the real client stack proves the label
// reaches the wire.
func newStampHarness(
	t *testing.T,
	respond func(req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error),
) (*GraphClient, *atomic.Int32) {
	t.Helper()
	mux := http.NewServeMux()
	enginePath, engineHdlr := knowledgev1connect.NewEngineServiceHandler(&stubEngine{respond: respond})
	mux.Handle(enginePath, engineHdlr)
	var healthCalls atomic.Int32
	healthPath, healthHdlr := knowledgev1connect.NewHealthServiceHandler(
		&stubHealth{attempt: &healthCalls, respond: func(int32) error { return nil }})
	mux.Handle(healthPath, healthHdlr)

	srv := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(srv.Close)
	return NewGraphClientForURL(srv.URL), &healthCalls
}

// TestClientOperationStamped drives the REAL client stack and asserts the
// operation from ctx arrives on the wire for a covered request message, and
// that an uncovered one (HealthService) is untouched and still reachable.
func TestClientOperationStamped(t *testing.T) {
	t.Run("covered request carries the ctx operation on the wire", func(t *testing.T) {
		var gotReq *knowledgev1.ExecuteRequest
		gc, _ := newStampHarness(t, func(req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
			gotReq = req
			return enginetest.ResponseWithNodes(), nil
		})

		ctx := WithOperation(context.Background(), stampTestOp)
		_, err := gc.Execute(ctx, &knowledgev1.ExecuteRequest{
			Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{ById: "x"}},
		})
		require.NoError(t, err)
		require.NotNil(t, gotReq, "handler should have received the request")
		assert.Equal(t, string(stampTestOp), gotReq.GetClientContext().GetOperation(),
			"the covered request must reach the server carrying the ctx operation")
	})

	t.Run("uncovered request is untouched and stays reachable", func(t *testing.T) {
		gc, healthCalls := newStampHarness(t, func(*knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
			return enginetest.ResponseWithNodes(), nil
		})

		// No operation in ctx, deliberately: HealthCheckRequest has no
		// client_context field, so the stamper must treat it as uncovered rather
		// than as an unstamped covered call. A credential-less, label-less
		// liveness probe has to keep working.
		_, err := gc.health.Check(context.Background(), connect.NewRequest(&knowledgev1.HealthCheckRequest{}))
		require.NoError(t, err, "an unlabeled health probe must still succeed")
		assert.Equal(t, int32(1), healthCalls.Load(), "the health handler should have been reached")
	})

	t.Run("an operation already on the message is not overwritten", func(t *testing.T) {
		var gotReq *knowledgev1.ExecuteRequest
		gc, _ := newStampHarness(t, func(req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
			gotReq = req
			return enginetest.ResponseWithNodes(), nil
		})

		ctx := WithOperation(context.Background(), stampTestOp)
		_, err := gc.Execute(ctx, &knowledgev1.ExecuteRequest{
			Plan:          &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{ById: "x"}},
			ClientContext: &knowledgev1.ClientContext{Operation: string(stampTestOtherOp)},
		})
		require.NoError(t, err)
		require.NotNil(t, gotReq)
		assert.Equal(t, string(stampTestOtherOp), gotReq.GetClientContext().GetOperation(),
			"an explicitly-set operation is more specific than the ambient ctx and must win")
	})
}
