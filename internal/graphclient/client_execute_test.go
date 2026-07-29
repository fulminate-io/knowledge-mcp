// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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

// stubEngine is an EngineService handler whose Execute behavior is driven by a
// closure so each test scripts the response (a canned ExecuteResponse or an
// error). The Topology/Stats/MetadataStats/Index methods satisfy the generated
// EngineServiceHandler interface but return Unimplemented — these tests exercise
// only the Execute wrapper.
type stubEngine struct {
	respond func(req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error)
}

func (s *stubEngine) Execute(
	_ context.Context,
	req *connect.Request[knowledgev1.ExecuteRequest],
) (*connect.Response[knowledgev1.ExecuteResponse], error) {
	resp, err := s.respond(req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (s *stubEngine) Stats(
	context.Context, *connect.Request[knowledgev1.StatsRequest],
) (*connect.Response[knowledgev1.StatsResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("stubEngine: Stats not scripted"))
}

func (s *stubEngine) MetadataStats(
	context.Context, *connect.Request[knowledgev1.MetadataStatsRequest],
) (*connect.Response[knowledgev1.MetadataStatsResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("stubEngine: MetadataStats not scripted"))
}

func (s *stubEngine) Index(
	context.Context, *connect.Request[knowledgev1.IndexRequest],
) (*connect.Response[knowledgev1.IndexResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("stubEngine: Index not scripted"))
}

func (s *stubEngine) Hive(
	context.Context, *connect.Request[knowledgev1.HiveRequest],
) (*connect.Response[knowledgev1.HiveResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("stubEngine: Hive not scripted"))
}

func (s *stubEngine) PipelineScan(
	context.Context, *connect.Request[knowledgev1.PipelineScanRequest],
) (*connect.Response[knowledgev1.PipelineScanResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("stubEngine: PipelineScan not scripted"))
}

func (s *stubEngine) PipelineGenPoll(
	context.Context, *connect.Request[knowledgev1.PipelineGenPollRequest],
) (*connect.Response[knowledgev1.PipelineGenPollResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("stubEngine: PipelineGenPoll not scripted"))
}

func (s *stubEngine) CorpusDelta(
	context.Context, *connect.Request[knowledgev1.CorpusDeltaRequest],
) (*connect.Response[knowledgev1.CorpusDeltaResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("stubEngine: CorpusDelta not scripted"))
}

func (s *stubEngine) ExportGraph(
	context.Context, *connect.Request[knowledgev1.ExportGraphRequest],
) (*connect.Response[knowledgev1.ExportGraphResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("stubEngine: ExportGraph not scripted"))
}

func (s *stubEngine) OverwriteGraph(
	context.Context, *connect.Request[knowledgev1.OverwriteGraphRequest],
) (*connect.Response[knowledgev1.OverwriteGraphResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("stubEngine: OverwriteGraph not scripted"))
}

// newEngineHarness stands up an in-process h2c httptest.Server behind the
// EngineService handler and returns a GraphClient (via NewGraphClientForURL)
// pointed at it.
func newEngineHarness(
	t *testing.T,
	respond func(req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error),
) *GraphClient {
	t.Helper()
	handler := &stubEngine{respond: respond}
	mux := http.NewServeMux()
	path, hdlr := knowledgev1connect.NewEngineServiceHandler(handler)
	mux.Handle(path, hdlr)

	h2s := &http2.Server{}
	srv := httptest.NewServer(h2c.NewHandler(mux, h2s))
	t.Cleanup(srv.Close)

	return NewGraphClientForURL(srv.URL)
}

// TestGraphClientExecute_ReturnsResponse asserts the wrapper issues
// EngineService.Execute and returns the server's *ExecuteResponse intact.
func TestGraphClientExecute_ReturnsResponse(t *testing.T) {
	canned := enginetest.ResponseWithNodes(&knowledgev1.Node{Id: "node-a"})
	canned.Ids = []string{"node-a", "node-b"}
	canned.Total = 42
	canned.AffectedCount = 0
	var gotReq *knowledgev1.ExecuteRequest
	gc := newEngineHarness(t, func(req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
		gotReq = req
		return canned, nil
	})

	req := &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Query{
			Query: &knowledgev1.QueryPlan{ById: "x"},
		},
	}

	resp, err := gc.Execute(opCtx(), req)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, []string{"node-a", "node-b"}, resp.GetIds())
	assert.Equal(t, int64(42), resp.GetTotal())
	require.Len(t, resp.GetNodes(), 1)
	assert.Equal(t, "node-a", resp.GetNodes()[0].GetId())
	require.NotNil(t, gotReq, "handler should have received the request")
	assert.Equal(t, "x", gotReq.GetQuery().GetById())
}

// TestGraphClientExecute_SurfacesTransportError asserts an RPC error returns
// (nil, err) — NOT a coerced kgtools.ToolResult (that coercion is Call's
// behavior, wrong for the typed engine RPC).
func TestGraphClientExecute_SurfacesTransportError(t *testing.T) {
	gc := newEngineHarness(t, func(_ *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("bad plan"))
	})

	req := &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Query{
			Query: &knowledgev1.QueryPlan{ById: "x"},
		},
	}
	resp, err := gc.Execute(opCtx(), req)
	require.Error(t, err)
	assert.Nil(t, resp, "transport error returns nil response")
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}
