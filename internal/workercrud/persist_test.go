// SPDX-License-Identifier: Apache-2.0

package workercrud

import (
	"reflect"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	workers "github.com/fulminate-io/knowledge-mcp/internal/workers"
)

// sampleWorker returns a fully populated Worker so the round-trip test
// covers every persisted field at once.
func sampleWorker() workers.Worker {
	return workers.Worker{
		Name:                "round-trip",
		Description:         "exercises every persisted field",
		SystemPrompt:        "You are a round-trip test worker. Do nothing.",
		Provider:            config.ProviderAnthropic,
		Model:               "claude-haiku-4-5-20251001",
		BaseURL:             "http://127.0.0.1:1234/v1",
		ToolAllowlist:       []string{"search", "think", "mutate"},
		Triggers:            []workers.Trigger{{Event: workers.EventManual}, {Event: workers.EventToolCompleted, Filter: map[string]string{"tool": "search", "status": "ok"}}},
		MaxIterations:       12,
		MaxWallclockSeconds: 75,
		Enabled:             true,
	}
}

// TestWorkerToNode_NodeToWorker_RoundTrip pins the pure data-marshaling
// contract between WorkerToNode and NodeToWorker — every persisted field
// survives encode → decode without an intermediate store hop. The wire-
// loopback impl on the client side has no in-process store engine so this
// test stays a pure transform test.
func TestWorkerToNode_NodeToWorker_RoundTrip(t *testing.T) {
	in := sampleWorker()
	node, err := WorkerToNode(in)
	if err != nil {
		t.Fatalf("WorkerToNode: %v", err)
	}

	got, err := NodeToWorker(node)
	if err != nil {
		t.Fatalf("NodeToWorker: %v", err)
	}

	assertWorkerEqual(t, got, in)
}

// TestNodeToWorker_RejectsWrongType makes sure the decoder refuses to
// produce a Worker from a node of an unrelated type — silent drift here
// would let the registry "load" arbitrary nodes as workers.
func TestNodeToWorker_RejectsWrongType(t *testing.T) {
	n := &knowledgev1.Node{Type: string(kgtypes.NodeLogBackend), SymbolName: "not-a-worker"}
	if _, err := NodeToWorker(n); err == nil {
		t.Fatalf("NodeToWorker accepted wrong type")
	}
}

// TestNodeToWorker_NodeJSONRoundTrip pins the wire decode path List
// walks via the Execute carrier seam: WorkerToNode produces a
// *knowledgev1.Node with the persisted metadata shape; the engine.DecodeNodes
// carrier carries the full *knowledgev1.Node over the wire. Round-trip the node
// through protojson and decode it back via NodeToWorker — the exact path List
// now walks for every decoded node.
func TestNodeToWorker_NodeJSONRoundTrip(t *testing.T) {
	in := sampleWorker()
	node, err := WorkerToNode(in)
	if err != nil {
		t.Fatalf("WorkerToNode: %v", err)
	}
	// Round-trip the *knowledgev1.Node through protojson to flush out any
	// encoding-side surprises in the per-field marshalers. protojson is the
	// proto-correct codec for a *knowledgev1.Node (encoding/json mishandles
	// proto internals).
	b, err := protojson.Marshal(node)
	if err != nil {
		t.Fatalf("marshal node: %v", err)
	}
	decoded := &knowledgev1.Node{}
	if err := protojson.Unmarshal(b, decoded); err != nil {
		t.Fatalf("unmarshal node: %v", err)
	}

	got, err := NodeToWorker(decoded)
	if err != nil {
		t.Fatalf("NodeToWorker: %v", err)
	}

	assertWorkerEqual(t, got, in)
}

// assertWorkerEqual checks every persisted field on two workers. Used
// by both the WorkerToNode/NodeToWorker round-trip tests above so the
// field list stays in one place.
func assertWorkerEqual(t *testing.T, got, want workers.Worker) {
	t.Helper()
	if got.Name != want.Name {
		t.Errorf("Name: got %q, want %q", got.Name, want.Name)
	}
	if got.Description != want.Description {
		t.Errorf("Description: got %q, want %q", got.Description, want.Description)
	}
	if got.SystemPrompt != want.SystemPrompt {
		t.Errorf("SystemPrompt: got %q, want %q", got.SystemPrompt, want.SystemPrompt)
	}
	if got.Provider != want.Provider {
		t.Errorf("Provider: got %q, want %q", got.Provider, want.Provider)
	}
	if got.Model != want.Model {
		t.Errorf("Model: got %q, want %q", got.Model, want.Model)
	}
	if got.BaseURL != want.BaseURL {
		t.Errorf("BaseURL: got %q, want %q", got.BaseURL, want.BaseURL)
	}
	if !reflect.DeepEqual(got.ToolAllowlist, want.ToolAllowlist) {
		t.Errorf("ToolAllowlist: got %v, want %v", got.ToolAllowlist, want.ToolAllowlist)
	}
	if !reflect.DeepEqual(got.Triggers, want.Triggers) {
		t.Errorf("Triggers: got %v, want %v", got.Triggers, want.Triggers)
	}
	if got.MaxIterations != want.MaxIterations {
		t.Errorf("MaxIterations: got %d, want %d", got.MaxIterations, want.MaxIterations)
	}
	if got.MaxWallclockSeconds != want.MaxWallclockSeconds {
		t.Errorf("MaxWallclockSeconds: got %d, want %d", got.MaxWallclockSeconds, want.MaxWallclockSeconds)
	}
	if got.Enabled != want.Enabled {
		t.Errorf("Enabled: got %v, want %v", got.Enabled, want.Enabled)
	}
}
