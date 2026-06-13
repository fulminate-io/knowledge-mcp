// SPDX-License-Identifier: Apache-2.0

package llmproviders

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// capturedRequest is the raw outbound request a conformance row's fake endpoint
// recorded. API rows populate httpBody (the POST body bytes); CLI rows populate
// argv (one entry per CLI argument) and stdin (the prompt body piped in). The
// schemaFile closure, set only for codex-cli, reads the tempfile the
// --output-schema flag points at — the schema does not ride argv inline there.
type capturedRequest struct {
	httpBody   []byte
	argv       []string
	stdin      string
	schemaFile func(t *testing.T) string
	// binDir is set for CLI rows: the tempdir holding the argv/stdin recorder
	// files the fake bin writes during Generate. finish reads them once the
	// SummarizeBatch round trip returns. Empty for API rows (finish no-ops).
	binDir string
}

// finish loads any deferred capture state. CLI rows record argv + stdin to
// files DURING the Generate call, so finish reads those files after the round
// trip; API rows already captured the body in the handler and finish is a
// no-op. Every conformance test calls cap.finish(t) before asserting the wire.
func (c *capturedRequest) finish(t *testing.T) {
	t.Helper()
	if c.binDir == "" {
		return
	}
	c.argv = readArgvFile(t, filepath.Join(c.binDir, "argv"))
	data, err := os.ReadFile(filepath.Join(c.binDir, "stdin")) //nolint:gosec // path is the test's own tempfile
	if err == nil {
		c.stdin = string(data)
	}
}

// conformanceClient pairs a real llm.Client (built through the PUBLIC seam,
// llm.NewClient with Config) with the capture handle its fake endpoint writes.
// Each conformance row's newClient returns one so a single SummarizeBatch round
// trip both exercises the provider end-to-end AND records what went on the wire.
type conformanceClient struct {
	client  llm.Client
	capture *capturedRequest
}

// newAPIClient builds a real provider client whose Config.BaseURL points at an
// httptest.Server that records the request body and serves respBody with a 200.
// The three API providers (anthropic, openai, gemini) all resolve Config.BaseURL
// through llm.NewClient — anthropic/gemini at construct time, openai at request
// time — so the same httptest seam drives all three with no transport injection.
func newAPIClient(t *testing.T, provider llm.Provider, model, respBody string) conformanceClient {
	t.Helper()
	cap := &capturedRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := readAll(r)
		cap.httpBody = body
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(respBody))
	}))
	t.Cleanup(srv.Close)

	client, err := llm.NewClient(t.Context(), &llm.Config{
		Provider: provider,
		Model:    llm.Model(model),
		BaseURL:  srv.URL,
		// A keyless BaseURL config is valid (Config.Validate allows it for the
		// API providers) — the httptest server handles "auth" out of band.
	})
	if err != nil {
		t.Fatalf("%s: llm.NewClient: %v", provider, err)
	}
	return conformanceClient{client: client, capture: cap}
}

// readAll drains an HTTP request body, returning the bytes. Split out so the
// handler closure stays terse and the (rare) read error is surfaced as empty
// bytes rather than a panic inside the server goroutine.
func readAll(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	defer r.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Body.Read(buf)
		if n > 0 {
			sb.Write(buf[:n])
		}
		if err != nil {
			if err.Error() == "EOF" {
				return []byte(sb.String()), nil
			}
			return []byte(sb.String()), err
		}
	}
}

// newCLIClient builds a real CLI provider client whose Config.CLIBin points at a
// generated fake executable. The fake records argv (one arg per line) and stdin
// to files in a tempdir, then emits respBody on stdout — the provider's native
// reply envelope. CLI rows t.Skip on windows (the fake relies on a POSIX shell
// script + the executable bit, neither of which the windows exec path honors).
//
// This is a local adaptation of codexcli/helpers_test.go's recordingFakeCodex:
// test helpers stay beside their consumers and AGENTS.md forbids a shared
// hand-written package, so the recorder is copied here rather than imported.
// The codex original recorded only argv+stdin; this copy adds schemaFile so the
// wire-shape assertion can follow codex's --output-schema argv path to the
// tempfile it references and read the schema body that does not ride argv inline.
func newCLIClient(t *testing.T, provider llm.Provider, model, respBody string) conformanceClient {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("CLI conformance rows rely on a POSIX shell fake bin + executable bit")
	}
	dir := t.TempDir()
	argvFile := filepath.Join(dir, "argv")
	stdinFile := filepath.Join(dir, "stdin")
	schemaFile := filepath.Join(dir, "schema")
	binPath := filepath.Join(dir, "fake-cli")

	// Encode the canned stdout so embedded newlines and quotes pass cleanly
	// through the shell; the script decodes it via the system base64 tool.
	//
	// The script ALSO captures the --output-schema tempfile's CONTENTS at exec
	// time: codex-cli writes the schema to a tempfile and Generate `defer`s its
	// cleanup, so the file is gone by the time the test asserts. Reading it here,
	// while the fake bin runs inside Generate (before cleanup), is the only point
	// the contents still exist — same execution-time capture as argv/stdin.
	encoded := base64Encode(respBody)
	script := fmt.Sprintf(`#!/bin/sh
prev=""
for a in "$@"; do
  printf '%%s\n' "$a" >> %q
  if [ "$prev" = "--output-schema" ]; then
    cat "$a" > %q 2>/dev/null
  fi
  prev="$a"
done
cat > %q
printf '%%s' %q | base64 -d
`, argvFile, schemaFile, stdinFile, encoded)
	if err := os.WriteFile(binPath, []byte(script), 0o700); err != nil { //nolint:gosec // test fixture must be executable
		t.Fatalf("write fake cli bin: %v", err)
	}

	cap := &capturedRequest{
		schemaFile: func(t *testing.T) string {
			t.Helper()
			// The fake bin copied the --output-schema tempfile's contents into
			// the schema recorder file at exec time (the original is deleted by
			// Generate's deferred cleanup). Read the recorder.
			data, err := os.ReadFile(schemaFile) //nolint:gosec // path is the test's own tempfile
			if err != nil {
				t.Fatalf("%s: no captured --output-schema contents (recorder %q): %v", provider, schemaFile, err)
			}
			return string(data)
		},
	}

	cap.binDir = dir

	client, err := llm.NewClient(t.Context(), &llm.Config{
		Provider: provider,
		Model:    llm.Model(model),
		CLIBin:   binPath,
	})
	if err != nil {
		t.Fatalf("%s: llm.NewClient: %v", provider, err)
	}
	return conformanceClient{client: client, capture: cap}
}

// readArgvFile reads the per-line argv recorder file and returns the args. The
// fake bin writes one arg per line via printf '%s\n'; an empty file (no args)
// returns nil.
func readArgvFile(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // path is the test's own tempfile
	if err != nil {
		t.Fatalf("read argv file %q: %v", path, err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	return lines
}

// argvValue returns the argument immediately following flag in argv, or "" when
// flag is absent or is the final entry. Used to follow codex's
// --output-schema <path> pair to the schema tempfile.
func argvValue(argv []string, flag string) string {
	for i, a := range argv {
		if a == flag && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	return ""
}

// argvHas reports whether argv contains flag as a standalone entry.
func argvHas(argv []string, flag string) bool {
	return slices.Contains(argv, flag)
}

// base64Encode renders body as standard RFC 4648 base64 with padding, for
// embedding in the fake CLI bin's shell script (decoded back via the system
// base64 tool). Implemented inline so the helper file is self-contained when
// read in isolation, mirroring codexcli/helpers_test.go's encodeStdout.
func base64Encode(body string) string {
	const tab = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	src := []byte(body)
	var sb strings.Builder
	for i := 0; i < len(src); i += 3 {
		var b [3]byte
		n := copy(b[:], src[i:])
		sb.WriteByte(tab[b[0]>>2])
		sb.WriteByte(tab[((b[0]&0x03)<<4)|(b[1]>>4)])
		if n > 1 {
			sb.WriteByte(tab[((b[1]&0x0f)<<2)|(b[2]>>6)])
		} else {
			sb.WriteByte('=')
		}
		if n > 2 {
			sb.WriteByte(tab[b[2]&0x3f])
		} else {
			sb.WriteByte('=')
		}
	}
	return sb.String()
}

// --- Per-provider native success-envelope builders --------------------------
//
// Each builder wraps the SAME itemsJSON ({"items":[...]}) payload in the
// provider's REAL native reply envelope, so the success-response fixture flows
// through the provider's actual parseResponse before reaching the summarizer's
// parseSummariesContent. Verified against each provider's parse layer:
// anthropic content[0].text, openai choices[0].message.content, gemini
// candidates[0].content.parts[0].text, claude-cli structured_output, codex-cli
// a JSONL item.completed + turn.completed event stream.

// anthropicEnvelope wraps text as the content[0].text block of a /v1/messages
// reply (response_parse.go reads content[].text). text is JSON-quoted so an
// items-JSON body lands verbatim in resp.Content.
func anthropicEnvelope(text string) string {
	return `{"id":"m","type":"message","role":"assistant","model":"claude-haiku-4-5",` +
		`"content":[{"type":"text","text":` + strconv.Quote(text) + `}],` +
		`"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
}

// openaiEnvelope wraps text as choices[0].message.content of a
// /v1/chat/completions reply (openai/response.go reads that field).
func openaiEnvelope(text string) string {
	return `{"id":"c","object":"chat.completion","model":"gpt-5-mini",` +
		`"choices":[{"index":0,"message":{"role":"assistant","content":` + strconv.Quote(text) + `},` +
		`"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`
}

// geminiEnvelope wraps text as candidates[0].content.parts[0].text of a
// generateContent reply (gemini/response.go reads that path).
func geminiEnvelope(text string) string {
	return `{"candidates":[{"content":{"role":"model","parts":[{"text":` + strconv.Quote(text) + `}]},` +
		`"finishReason":"STOP","index":0}],` +
		`"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1}}`
}

// claudeCLIEnvelope wraps text as the structured_output payload of the CLI's
// --output-format json result envelope. parseResponse PREFERS structured_output
// over result (translate.go:291-294), so the schema-validated JSON rides there
// on the success path. text is the raw JSON object string (not re-quoted —
// structured_output is itself a JSON value).
func claudeCLIEnvelope(text string) string {
	return `{"type":"result","subtype":"success","is_error":false,` +
		`"result":"here you go","stop_reason":"end_turn",` +
		`"structured_output":` + text + `,` +
		`"usage":{"input_tokens":1,"output_tokens":1}}`
}

// claudeCLITextEnvelope wraps text as the free-form result string with NO
// structured_output key, so the text-content fallback carries it. This is the
// negative-fixture shape: it reproduces the original commentary-instead-of-
// structured_output failure mode where the CLI returned prose in result.
func claudeCLITextEnvelope(text string) string {
	return `{"type":"result","subtype":"success","is_error":false,` +
		`"result":` + strconv.Quote(text) + `,"stop_reason":"end_turn",` +
		`"usage":{"input_tokens":1,"output_tokens":1}}`
}

// codexCLIEnvelope wraps text as a JSONL event stream: an item.completed
// agent_message carrying text, then the REQUIRED turn.completed event (parse.go
// needs turn.completed for a non-error outcome). text is JSON-quoted so an
// items-JSON body survives as the event's text field.
func codexCLIEnvelope(text string) string {
	return `{"type":"thread.started","thread_id":"t"}` + "\n" +
		`{"type":"turn.started"}` + "\n" +
		`{"type":"item.completed","item":{"item_type":"agent_message","text":` + strconv.Quote(text) + `}}` + "\n" +
		`{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}` + "\n"
}

// --- Per-provider wire-shape assertions -------------------------------------
//
// Each asserts the provider's structured-output mechanism is present on the
// captured outbound request, with a failure message naming the provider and the
// missing mechanism. Anthropic asserts output_config PRESENCE ONLY and
// cross-references the anthropic package's TestStructuredOutputWireShape for the
// deep shape (stripped schema, no tool_choice) — coordinate, do not duplicate.

func assertAnthropicWire(t *testing.T, cap *capturedRequest) {
	t.Helper()
	if !jsonHasKey(t, cap.httpBody, "output_config") {
		t.Errorf("anthropic: outbound request is missing the native structured-output mechanism (output_config); "+
			"applyResponseFormat native path not wired. body: %s", cap.httpBody)
	}
}

func assertOpenAIWire(t *testing.T, cap *capturedRequest) {
	t.Helper()
	if !jsonHasKey(t, cap.httpBody, "response_format") {
		t.Errorf("openai: outbound request is missing the structured-output mechanism (response_format). body: %s", cap.httpBody)
	}
}

func assertGeminiWire(t *testing.T, cap *capturedRequest) {
	t.Helper()
	if !bodyContainsKey(cap.httpBody, "responseSchema") {
		t.Errorf("gemini: outbound request is missing the structured-output mechanism (responseSchema). body: %s", cap.httpBody)
	}
}

func assertClaudeCLIWire(t *testing.T, cap *capturedRequest) {
	t.Helper()
	if !argvHas(cap.argv, "--json-schema") {
		t.Errorf("claude-cli: argv is missing the structured-output mechanism (--json-schema). argv: %v", cap.argv)
		return
	}
	schema := argvValue(cap.argv, "--json-schema")
	if !strings.Contains(schema, `"items"`) {
		t.Errorf("claude-cli: --json-schema payload does not carry the summarizer schema. value: %s", schema)
	}
}

func assertCodexCLIWire(t *testing.T, cap *capturedRequest) {
	t.Helper()
	if !argvHas(cap.argv, "--output-schema") {
		t.Errorf("codex-cli: argv is missing the structured-output mechanism (--output-schema). argv: %v", cap.argv)
		return
	}
	schema := cap.schemaFile(t)
	if !strings.Contains(schema, `"items"`) {
		t.Errorf("codex-cli: --output-schema tempfile does not carry the summarizer schema. content: %s", schema)
	}
}

// jsonHasKey reports whether body decodes as a JSON object carrying key at the
// top level. Used by the API wire assertions to confirm the mechanism field is
// present (not merely a substring of some nested value).
func jsonHasKey(t *testing.T, body []byte, key string) bool {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("decode outbound body as JSON object: %v (body: %s)", err, body)
	}
	_, ok := m[key]
	return ok
}

// bodyContainsKey reports whether body carries key as a JSON object key
// somewhere in the request tree. gemini nests responseSchema under
// generationConfig, so a top-level key check would miss it; the quoted-key
// substring is sufficient because the key name does not collide with any value
// text the summarizer schema produces.
func bodyContainsKey(body []byte, key string) bool {
	return strings.Contains(string(body), `"`+key+`"`)
}
