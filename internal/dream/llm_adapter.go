// SPDX-License-Identifier: Apache-2.0

package dream

import (
	"context"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// llmAdapter satisfies eino's ToolCallingChatModel interface on top of a
// substrate-level llm.Client. Generate forwards verbatim; Stream wraps the
// generated message in a single-chunk Pipe (the substrate is non-streaming
// at the Client interface, so eino streaming downgrades to invoke + one
// chunk); WithTools returns a new adapter with the model's tool list
// installed via llm.WithTools on every subsequent Generate call.
//
// The adapter has NO sysPrompt field — the caller (runner_react) is
// responsible for prepending a schema.System message to the conversation.
// Centralizing the system prompt in messages[0] keeps Generate() pure
// (one input → one output) and avoids two competing source-of-truth
// configurations for the prompt.
type llmAdapter struct {
	client    llm.Client
	modelName llm.Model
	baseOpts  []llm.Option
	tools     []*schema.ToolInfo
}

// newLLMAdapter constructs an adapter that calls c.Generate with modelName
// and any baseOpts on every Generate / Stream invocation. The two required
// arguments (Client, Model) are positional; everything else (max tokens,
// temperature, system prompt — usually injected by the caller via
// messages[0]) goes through baseOpts.
func newLLMAdapter(c llm.Client, modelName llm.Model, baseOpts ...llm.Option) *llmAdapter {
	return &llmAdapter{
		client:    c,
		modelName: modelName,
		baseOpts:  baseOpts,
	}
}

// Compile-time guarantee: llmAdapter satisfies the eino tool-calling chat
// model interface. ReAct.NewAgent rejects the adapter with a confusing
// error if this ever drifts; the var-blank assignment surfaces the break
// at compile time instead.
var _ einomodel.ToolCallingChatModel = (*llmAdapter)(nil)

// Generate forwards messages to the underlying llm.Client and packs the
// substrate Response back into eino's *schema.Message shape. ToolCalls
// already share the same struct (schema.ToolCall) on both sides, so the
// translation is a verbatim copy.
//
// The opts vararg is the eino model.Option list; we ignore it. eino
// callers configure per-call behavior through Generate options at the
// model level only when no compatible substrate option exists. The
// adapter's contract is: substrate options are set via newLLMAdapter +
// baseOpts and via WithTools; ReAct's per-step options (none in v0.8.13
// that we care about) are dropped.
func (a *llmAdapter) Generate(ctx context.Context, messages []*schema.Message, _ ...einomodel.Option) (*schema.Message, error) {
	opts := make([]llm.Option, 0, len(a.baseOpts)+2)
	opts = append(opts, a.baseOpts...)
	opts = append(opts, llm.WithModel(a.modelName))
	if len(a.tools) > 0 {
		opts = append(opts, llm.WithTools(a.tools))
	}
	resp, err := a.client.Generate(ctx, messages, opts...)
	if err != nil {
		return nil, err
	}
	return &schema.Message{
		Role:      schema.Assistant,
		Content:   resp.Content,
		ToolCalls: resp.ToolCalls,
	}, nil
}

// Stream wraps Generate in a single-chunk Pipe so eino's streaming graph
// orchestration sees the same shape as a native streaming model. The
// substrate Client is invoke-only; streaming-from-the-substrate would
// require a parallel interface that no caller actually consumes today.
//
// The Pipe capacity is 1 to match the single chunk we send; Send is
// non-blocking under cap=1 with one consumer.
func (a *llmAdapter) Stream(ctx context.Context, messages []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := a.Generate(ctx, messages, opts...)
	if err != nil {
		return nil, err
	}
	sr, sw := schema.Pipe[*schema.Message](1)
	go func() {
		defer sw.Close()
		sw.Send(msg, nil)
	}()
	return sr, nil
}

// WithTools returns a new adapter carrying tools. Per the eino
// ToolCallingChatModel contract, this MUST NOT mutate the receiver — a
// caller may share a base adapter across goroutines and derive per-request
// variants with different tool sets.
func (a *llmAdapter) WithTools(tools []*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	cp := *a
	cp.tools = tools
	return &cp, nil
}
