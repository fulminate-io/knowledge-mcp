// SPDX-License-Identifier: Apache-2.0

package dream

import (
	"context"
	"errors"
	"io"
	"testing"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// TestLLMAdapter_GenerateForwardsContent shows the trivial happy path:
// Generate returns the substrate Response's Content under the Assistant
// role, including any tool calls.
func TestLLMAdapter_GenerateForwardsContent(t *testing.T) {
	fake := llm.NewFakeClient(&llm.Response{
		Content:      "hello",
		FinishReason: llm.FinishReasonEndTurn,
		ToolCalls: []schema.ToolCall{
			{ID: "call_1", Type: "function", Function: schema.FunctionCall{Name: "search", Arguments: `{"q":"x"}`}},
		},
	})
	a := newLLMAdapter(fake, llm.Model("test"))

	msg, err := a.Generate(context.Background(), []*schema.Message{
		{Role: schema.System, Content: "be brief"},
		{Role: schema.User, Content: "hi"},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if msg.Role != schema.Assistant {
		t.Errorf("Role = %s, want Assistant", msg.Role)
	}
	if msg.Content != "hello" {
		t.Errorf("Content = %q, want %q", msg.Content, "hello")
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Function.Name != "search" {
		t.Errorf("ToolCalls = %#v, want one search call", msg.ToolCalls)
	}
}

// TestLLMAdapter_PassesThroughError verifies upstream errors propagate.
func TestLLMAdapter_PassesThroughError(t *testing.T) {
	fake := llm.NewFakeClient()
	want := errors.New("provider exploded")
	fake.SetError(want)

	a := newLLMAdapter(fake, llm.Model("test"))
	if _, err := a.Generate(context.Background(), []*schema.Message{{Role: schema.User, Content: "x"}}); !errors.Is(err, want) {
		t.Fatalf("Generate err = %v, want %v", err, want)
	}
}

// TestLLMAdapter_AppliesBaseOptionsAndModel checks that newLLMAdapter
// baseOpts and the configured Model land in the recorded RequestOptions.
func TestLLMAdapter_AppliesBaseOptionsAndModel(t *testing.T) {
	fake := llm.NewFakeClient(&llm.Response{Content: "ok", FinishReason: llm.FinishReasonEndTurn})
	a := newLLMAdapter(fake, llm.Model("override-model"), llm.WithMaxTokens(123), llm.WithTemperature(0.42))

	if _, err := a.Generate(context.Background(), []*schema.Message{{Role: schema.User, Content: "x"}}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	calls := fake.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	got := calls[0].Options
	if got.Model != "override-model" {
		t.Errorf("Model = %q, want override-model", got.Model)
	}
	if got.MaxTokens != 123 {
		t.Errorf("MaxTokens = %d, want 123", got.MaxTokens)
	}
	if got.Temperature == nil || *got.Temperature != 0.42 {
		t.Errorf("Temperature = %v, want *0.42", got.Temperature)
	}
}

// TestLLMAdapter_WithToolsReturnsNewInstance is the central concurrency-safety
// guarantee from the eino ToolCallingChatModel contract — WithTools must NOT
// mutate the receiver.
func TestLLMAdapter_WithToolsReturnsNewInstance(t *testing.T) {
	fake := llm.NewFakeClient(
		&llm.Response{Content: "no-tools", FinishReason: llm.FinishReasonEndTurn},
		&llm.Response{Content: "with-tools", FinishReason: llm.FinishReasonEndTurn},
	)
	a := newLLMAdapter(fake, llm.Model("test"))
	tools := []*schema.ToolInfo{{Name: "search", Desc: "find code"}}

	withTools, err := a.WithTools(tools)
	if err != nil {
		t.Fatalf("WithTools: %v", err)
	}
	// Original receiver still has no tools installed.
	if _, err := a.Generate(context.Background(), []*schema.Message{{Role: schema.User, Content: "x"}}); err != nil {
		t.Fatalf("Generate base: %v", err)
	}
	if got := fake.Calls()[0].Options.Tools; len(got) != 0 {
		t.Errorf("base adapter Tools = %d, want 0", len(got))
	}
	// New instance carries the bound tools.
	if _, err := withTools.Generate(context.Background(), []*schema.Message{{Role: schema.User, Content: "x"}}); err != nil {
		t.Fatalf("Generate with tools: %v", err)
	}
	if got := fake.Calls()[1].Options.Tools; len(got) != 1 || got[0].Name != "search" {
		t.Errorf("withTools adapter Tools = %#v, want [search]", got)
	}
}

// TestLLMAdapter_StreamYieldsSingleChunk wraps the invoke-only substrate
// behind eino's Stream contract.
func TestLLMAdapter_StreamYieldsSingleChunk(t *testing.T) {
	fake := llm.NewFakeClient(&llm.Response{Content: "streamed", FinishReason: llm.FinishReasonEndTurn})
	a := newLLMAdapter(fake, llm.Model("test"))

	sr, err := a.Stream(context.Background(), []*schema.Message{{Role: schema.User, Content: "x"}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer sr.Close()

	chunk, err := sr.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if chunk.Content != "streamed" {
		t.Errorf("Content = %q, want streamed", chunk.Content)
	}
	// Subsequent Recv returns EOF.
	if _, err := sr.Recv(); !errors.Is(err, io.EOF) {
		t.Errorf("Recv #2 err = %v, want io.EOF", err)
	}
}

// TestLLMAdapter_SatisfiesInterface guards against accidental drift from the
// eino interface signatures.
func TestLLMAdapter_SatisfiesInterface(t *testing.T) {
	var _ einomodel.ToolCallingChatModel = (*llmAdapter)(nil)
	var _ einomodel.BaseChatModel = (*llmAdapter)(nil)
}
