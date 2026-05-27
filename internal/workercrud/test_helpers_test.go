// SPDX-License-Identifier: Apache-2.0

package workercrud

import (
	"context"
	"encoding/json"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	workers "github.com/fulminate-io/knowledge-mcp/internal/workers"
)

// fakeCRUDClient is a workerCRUDClient (the production interface at
// server.go) for unit tests. The worker CRUD surface rides the Execute
// carrier seam (T-GTB6), so every CRUD op appends an execRecord (the
// compiled *ExecuteRequest) and returns the next queued execResponse —
// the test body asserts on the compiled plan shape and feeds canned
// ExecuteResponses / connect errors back. Call is retained to satisfy the
// interface but is not exercised by the CRUD methods.
type fakeCRUDClient struct {
	execs     []*knowledgev1.ExecuteRequest
	responses []execResponse
}

type execResponse struct {
	Resp *knowledgev1.ExecuteResponse
	Err  error
}

// Call satisfies the interface; the CRUD methods route through Execute, so
// this is unused in practice (a stray call returns an empty result).
func (f *fakeCRUDClient) Call(_ context.Context, _ string, _ json.RawMessage) (kgtools.ToolResult, error) {
	return kgtools.ToolResult{}, nil
}

// Execute records the compiled request and returns the next queued
// response. An empty queue returns (&ExecuteResponse{}, nil) — tests
// should queue one response per expected Execute.
func (f *fakeCRUDClient) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	f.execs = append(f.execs, req)
	if len(f.responses) == 0 {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	resp := f.responses[0]
	f.responses = f.responses[1:]
	return resp.Resp, resp.Err
}

// queueResp appends a successful ExecuteResponse to f's queue (FIFO).
func (f *fakeCRUDClient) queueResp(resp *knowledgev1.ExecuteResponse) {
	f.responses = append(f.responses, execResponse{Resp: resp})
}

// queueErr appends an Execute error to f's queue (FIFO).
func (f *fakeCRUDClient) queueErr(err error) {
	f.responses = append(f.responses, execResponse{Err: err})
}

// queueNodes appends an ExecuteResponse whose typed Nodes carrier holds the
// given *knowledgev1.Node payloads — the List decode path (engine.DecodeNodes).
// Built via enginetest.ResponseWithNodes so the fixture populates ONLY the
// typed Nodes field (the nodes_json blob was deleted by P2-T5).
func (f *fakeCRUDClient) queueNodes(t testingTB, ws []workers.Worker) {
	t.Helper()
	nodes := make([]*knowledgev1.Node, 0, len(ws))
	for _, w := range ws {
		n, err := WorkerToNode(w)
		if err != nil {
			t.Fatalf("queueNodes: WorkerToNode: %v", err)
		}
		nodes = append(nodes, n)
	}
	f.queueResp(enginetest.ResponseWithNodes(nodes...))
}

// testingTB is the minimal slice of *testing.T queueNodes needs (Helper +
// Fatalf), kept local so the helper file stays import-light.
type testingTB interface {
	Helper()
	Fatalf(format string, args ...any)
}
