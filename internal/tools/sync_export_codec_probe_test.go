// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
)

// codecProbeHandler is a minimal EngineServiceHandler that records the inbound
// target selector and echoes a fixed byte payload back as GraphBytes. It embeds
// UnimplementedEngineServiceHandler so only ExportGraph is overridden.
type codecProbeHandler struct {
	knowledgev1connect.UnimplementedEngineServiceHandler
	gotGraph string
	gotRepo  string
	payload  []byte
}

func (h *codecProbeHandler) ExportGraph(
	_ context.Context,
	req *connect.Request[knowledgev1.ExportGraphRequest],
) (*connect.Response[knowledgev1.ExportGraphResponse], error) {
	if t := req.Msg.GetTarget(); t != nil {
		h.gotGraph = t.GetGraph()
		h.gotRepo = t.GetRepo()
	}
	return connect.NewResponse(&knowledgev1.ExportGraphResponse{GraphBytes: h.payload}), nil
}

// TestExportGraphJSONCodecProbe is the EARLY codec-acceptance assertion (review
// advisory T3-2): it proves the REAL gen-generated EngineService Connect handler
// (NewEngineServiceHandler, mounted with no WithCodecs) accepts a hand-rolled raw
// Connect-JSON POST — exactly the wire the agent's exportGraphFromCloud produces
// (no fulminate-io/knowledge/gen, no connectrpc.com/connect dependency on the
// agent side) — and returns a response whose "graphBytes" key base64-StdEncoding-
// decodes to the original bytes. This pins codec acceptance + the response parse
// shape BEFORE the whole envelope/GCS stack is built on it.
func TestExportGraphJSONCodecProbe(t *testing.T) {
	knownBytes := []byte("KGV4\x00\x01\x02\x03 a representative serialized graph blob")
	stub := &codecProbeHandler{payload: knownBytes}

	path, handler := knowledgev1connect.NewEngineServiceHandler(stub)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Hand-build the SAME JSON wire the agent emits: Content-Type application/json,
	// a {"target":{...}} body. This is a knowledge-graph pull (graph empty/knowledge,
	// name omitted) plus a code target field to assert selector decode.
	reqBody := `{"target":{"graph":"code","repo":"myrepo"}}`
	resp, err := http.Post(
		srv.URL+knowledgev1connect.EngineServiceExportGraphProcedure,
		"application/json",
		strings.NewReader(reqBody),
	)
	if err != nil {
		t.Fatalf("POST ExportGraph: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ExportGraph status = %d, body = %s", resp.StatusCode, string(body))
	}

	// The hand-rolled selector must have decoded server-side.
	if stub.gotGraph != "code" || stub.gotRepo != "myrepo" {
		t.Fatalf("server decoded target graph=%q repo=%q, want code/myrepo", stub.gotGraph, stub.gotRepo)
	}

	// Parse exactly as the agent does: JSON-decode the lowerCamelCase "graphBytes"
	// key, then base64-StdEncoding-decode it into []byte.
	var parsed struct {
		GraphBytes string `json:"graphBytes"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("decode response JSON %q: %v", string(body), err)
	}
	if parsed.GraphBytes == "" {
		t.Fatalf("response carried no graphBytes key: %s", string(body))
	}
	got, err := base64.StdEncoding.DecodeString(parsed.GraphBytes)
	if err != nil {
		t.Fatalf("base64-StdEncoding-decode graphBytes: %v", err)
	}
	if string(got) != string(knownBytes) {
		t.Fatalf("round-tripped bytes = %q, want %q", string(got), string(knownBytes))
	}
}
