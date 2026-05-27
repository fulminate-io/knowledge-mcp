package llm

import (
	"context"

	"github.com/cloudwego/eino/schema"
)

// Client is the substrate's single LLM-call abstraction.
//
// One method, by design. Streaming, ReAct/tool-use loops, retry policies, and
// caller-side context windowing are concerns that live above the substrate.
// Anything richer than "messages in, response out" goes through the caller.
//
// Implementations MUST NOT truncate or drop message content before sending it
// to the upstream provider. Truncation has been a recurring source of silent
// data loss in summarizer pipelines (see feedback_no_truncation_for_llm). If a
// caller hands the client more tokens than the model can accept, the
// implementation returns an error rather than quietly dropping content.
//
// Implementations register themselves with the package-level registry from
// init() so callers obtain a Client via NewClient(provider, cfg) without
// importing every provider package directly.
type Client interface {
	// Generate executes one non-streaming LLM call.
	//
	// `messages` is an ordered conversation in eino's schema.Message shape
	// (Role, Content, ToolCalls, ToolCallID, etc.). The caller owns turn
	// construction; implementations forward verbatim.
	//
	// Variadic Option values configure per-request behavior (model, max
	// tokens, temperature, system prompt, tools, etc.). See request.go.
	Generate(ctx context.Context, messages []*schema.Message, opts ...Option) (*Response, error)
}

// Option configures a single Generate request.
//
// Functional-options pattern: each With* helper returns an Option that mutates
// the populated RequestOptions before the implementation reads it. See
// request.go for the full set of helpers and the populated field list.
type Option func(*RequestOptions)
