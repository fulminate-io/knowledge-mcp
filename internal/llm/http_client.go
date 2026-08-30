package llm

import (
	"net/http"
	"time"
)

// DefaultHTTPTimeout is the per-request timeout used by API providers.
//
// 120s gives long context-completion requests headroom while still bounding
// worst-case stalls. The value was originally chosen to match a separate
// hand-rolled summarizer pipeline that predated this package; that pipeline is
// gone and the summarizer is now a CONSUMER of this constant — it drives an
// llm.Client whose provider builds its transport from DefaultHTTPClient below
// (llmproviders/summarizer_llm.go) — so this is the definition rather than a
// copy of one, and changing it moves the summarizer's timeout with it.
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
