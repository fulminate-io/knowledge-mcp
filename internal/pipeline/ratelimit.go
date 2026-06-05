// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"errors"
	"time"

	"connectrpc.com/connect"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// rateLimitHint classifies err as a rate-limit / transient backend failure and
// extracts the server's stated Retry-After delay when present. It unifies the
// two error shapes the pipeline sees when it talks to a remote backend:
//
//   - a *connect.Error from the remote backend — a per-IP 429 surfaces as
//     CodeUnavailable ("Too many requests") or CodeResourceExhausted, with the
//     Retry-After in the error's response metadata. Used by the scan loop (#3)
//     and the writeback retry, which both ride the remote backend.
//   - a *llm.LLMError from a provider — voyage / anthropic / openai / gemini
//     wrap their 429/503 as Transient with the parsed Retry-After. Included so
//     a single helper covers any backend error the pipeline routes through.
//
// ok == false means "not a rate limit" — the caller should NOT retry it as one
// (the writeback returns it unchanged; the scan loop still backs off on any
// error, it just won't have a server hint). retryAfter is 0 when the server
// gave no hint, and the caller falls back to its own backoff.
func rateLimitHint(err error) (retryAfter time.Duration, ok bool) {
	if err == nil {
		return 0, false
	}
	if ce, cok := errors.AsType[*connect.Error](err); cok {
		switch ce.Code() {
		case connect.CodeResourceExhausted, connect.CodeUnavailable:
			return llm.ParseRetryAfter(ce.Meta()), true
		default:
			return 0, false
		}
	}
	if le, lok := errors.AsType[*llm.LLMError](err); lok && le.Transient {
		return le.RetryAfter, true
	}
	return 0, false
}
