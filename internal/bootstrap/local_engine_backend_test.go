// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// succeedingEngine answers every Execute with an empty success and leaves every
// other method on the generated Unimplemented base.
type succeedingEngine struct {
	knowledgev1connect.UnimplementedEngineServiceHandler
}

func (succeedingEngine) Execute(
	context.Context, *connect.Request[knowledgev1.ExecuteRequest],
) (*connect.Response[knowledgev1.ExecuteResponse], error) {
	return connect.NewResponse(&knowledgev1.ExecuteResponse{}), nil
}

// localEngineBackend returns a GraphClient pointed at an in-process h2c server
// behind the EngineService handler.
//
// WHY THIS EXISTS AT ALL. Admission now follows a SUCCESSFUL dispatch, so the
// admission harness in this package can no longer pass a nil local client and
// short-circuit at pick(). The graphclient package has its own equivalent
// harness, but it is unexported, so this is its bootstrap-side copy.
//
// h2c RATHER THAN PLAIN httptest IS REQUIRED: GraphClient dials cleartext
// HTTP/2 with prior knowledge, so an HTTP/1.1-only test server never reaches the
// handler and the call fails seconds later with a transport error that reads
// exactly like a refusal.
//
// THE TEARDOWN ORDER IS A CORRECTNESS REQUIREMENT, NOT TIDINESS. This package
// runs goleak.VerifyTestMain, and an HTTP/2 connection the CLIENT still holds
// keeps the SERVER's serve goroutine alive — so the client is closed BEFORE the
// server. It is the client's Close rather than CloseIdleConnections for the
// reason the graphclient package records at leakguard_test.go: a pool-level
// release reaches a connection only once the transport has retired its last
// stream, which a test that asserts and returns does not wait for, while Close
// does not depend on winning that race. Get this wrong and the h2 serve
// goroutine outlives the test and the WHOLE PACKAGE fails at suite end, with no
// failing test to point at.
func localEngineBackend(t *testing.T) *graphclient.GraphClient {
	t.Helper()

	mux := http.NewServeMux()
	path, hdlr := knowledgev1connect.NewEngineServiceHandler(succeedingEngine{})
	mux.Handle(path, hdlr)

	srv := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	gc := graphclient.NewGraphClientForURL(srv.URL)

	t.Cleanup(func() {
		gc.Close()
		srv.CloseClientConnections()
		srv.Close()
	})

	return gc
}
