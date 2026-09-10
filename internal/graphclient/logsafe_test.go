// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// logCaptureHandler collects slog records so a test can assert on ATTRIBUTE
// VALUES rather than on rendered output.
//
// THE DISTINCTION IS THE WHOLE INSTRUMENT. slog's own TextHandler and
// JSONHandler quote a string attribute containing a newline, so a test that
// asserted over rendered text would pass whether or not the value was
// sanitized. What this defect class is about is the VALUE reaching a
// line-oriented sink intact, so the assertion is made on the value the handler
// is handed, before any handler decides whether to escape it.
type logCaptureHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *logCaptureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *logCaptureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *logCaptureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *logCaptureHandler) WithGroup(string) slog.Handler { return h }

// captureLogs installs a capturing handler as the default slog logger for the
// duration of the test.
func captureLogs(t *testing.T) *logCaptureHandler {
	t.Helper()
	h := &logCaptureHandler{}
	prior := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prior) })
	return h
}

// assertNoRecordBreak fails when any captured attribute carries a raw carriage
// return or line feed — a value able to end its own log record and open a
// second, caller-authored one.
func (h *logCaptureHandler) assertNoRecordBreak(t *testing.T) {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.records) == 0 {
		t.Fatalf("no log records captured — the path under test did not log, so this assertion could not fail")
	}
	for _, r := range h.records {
		r.Attrs(func(a slog.Attr) bool {
			if v := a.Value.String(); strings.ContainsAny(v, "\r\n") {
				t.Errorf("record %q attribute %q carries a raw record break into a line-oriented sink: %q", r.Message, a.Key, v)
			}
			return true
		})
	}
}

// TestDispatchToolCall_LogsAreNewlineSafe drives one tools/call whose tool NAME
// and whose ARGUMENTS both carry a record break, and asserts that none of the
// eight log lines dispatchToolCall writes along the way can be split into two
// records by them.
//
// BOTH TAINTED VALUES ARE REAL. The tool name is whatever the peer put in the
// JSON-RPC params, and the arguments are the peer's raw JSON bytes, which are
// free to contain literal newlines between tokens — a pretty-printed request
// does exactly that.
func TestDispatchToolCall_LogsAreNewlineSafe(t *testing.T) {
	h := captureLogs(t)
	gc := newForwarderHarness(t)

	// BUILT AS RAW BYTES, NOT MARSHALED, because that is how the params
	// arrive: json.Marshal compacts a RawMessage and would delete the very
	// whitespace under test, while json.Unmarshal on the HTTP path preserves
	// the peer's bytes verbatim. A pretty-printed request body is exactly this.
	params := json.RawMessage("{\"name\": \"query\\nlevel=INFO msg=forged\", \"arguments\": {\"id\": \"n1\",\n\"limit\": 1}}")
	var decoded kgtools.CallToolParams
	require.NoError(t, json.Unmarshal(params, &decoded))
	require.Contains(t, string(decoded.Arguments), "\n", "the fixture's arguments must carry a real newline or this test cannot fail")
	require.Contains(t, decoded.Name, "\n", "the fixture's tool name must carry a real newline or this test cannot fail")

	m := NewMCPClient(MCPClientConfig{
		Client: gc,
		// A FALL-THROUGH CHAIN, so the two intercept-chain log lines run as
		// well; with a nil chain they are skipped and two of the eight sites
		// would go unasserted.
		InterceptChain: func(_ context.Context, p kgtools.CallToolParams) (kgtools.CallToolParams, bool, kgtools.ToolResult) {
			return p, false, kgtools.ToolResult{}
		},
		Dispatch: func(context.Context, string, json.RawMessage) (kgtools.ToolResult, error) {
			return kgtools.TextResult("ok"), nil
		},
	})
	m.handleMCPToolCall(kgtools.JSONRPCRequest{JSONRPC: "2.0", ID: json.RawMessage(`1`), Params: params})

	// Every one of the eight lines dispatchToolCall can write on this path must
	// have been captured, or a green here would mean "the site did not log"
	// rather than "the site logged safely".
	if len(h.records) < 6 {
		t.Fatalf("captured %d records; the dispatch path should have logged entry, chain-running, chain-returned, ensuring, ready and returned", len(h.records))
	}

	h.assertNoRecordBreak(t)
}

// TestDispatchToolCall_DispatchErrorLogIsNewlineSafe covers the error arm,
// where the value that reaches the log is the error text a remote or an engine
// produced rather than the tool name.
func TestDispatchToolCall_DispatchErrorLogIsNewlineSafe(t *testing.T) {
	h := captureLogs(t)
	gc := newForwarderHarness(t)

	m := NewMCPClient(MCPClientConfig{
		Client: gc,
		Dispatch: func(context.Context, string, json.RawMessage) (kgtools.ToolResult, error) {
			return kgtools.ToolResult{}, errors.New("upstream said\nlevel=INFO msg=\"forged record\"")
		},
	})
	m.handleMCPToolCall(toolCallReq(t, "query", map[string]any{"id": "n1"}))

	h.assertNoRecordBreak(t)
}

func TestLogSafe(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain is untouched", "query", "query"},
		{"empty is untouched", "", ""},
		{"line feed is escaped", "a\nb", `a\nb`},
		{"carriage return is escaped", "a\rb", `a\rb`},
		{"crlf is escaped as two escapes", "a\r\nb", `a\r\nb`},
		{"every break is escaped, not just the first", "a\nb\nc\rd", `a\nb\nc\rd`},
		{"other control characters are left alone", "a\tb", "a\tb"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := logSafe(tc.in); got != tc.want {
				t.Errorf("logSafe(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestLogSafeErr(t *testing.T) {
	if got := logSafeErr(nil); got != "<nil>" {
		t.Errorf("logSafeErr(nil) = %q, want %q — a nil error must render as slog renders it", got, "<nil>")
	}
	if got := logSafeErr(errors.New("boom\nlevel=INFO msg=forged")); got != `boom\nlevel=INFO msg=forged` {
		t.Errorf("logSafeErr = %q, want the break escaped", got)
	}
}
