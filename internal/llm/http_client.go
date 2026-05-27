package llm

import (
	"net/http"
	"time"
)

// DefaultHTTPTimeout is the per-request timeout used by API providers.
//
// 120s matches the existing knowledge summarizer pipeline
// (domains/store/summarize_openai.go) and gives long context-completion
// requests headroom while still bounding worst-case stalls.
const DefaultHTTPTimeout = 120 * time.Second

// DefaultHTTPClient returns a fresh *http.Client suitable for LLM API
// requests. Each call returns a new instance so callers can wrap, replace,
// or swap the transport without mutating shared state.
//
// Defaults: Timeout=DefaultHTTPTimeout. Transport is left nil so Go's
// http.DefaultTransport is reused — that gives connection pooling, sane
// proxy handling, and HTTP/2 negotiation without us reinventing them.
func DefaultHTTPClient() *http.Client {
	return &http.Client{
		Timeout: DefaultHTTPTimeout,
	}
}
