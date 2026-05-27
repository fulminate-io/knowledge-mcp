// SPDX-License-Identifier: Apache-2.0

package claudecli

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// userMsg is a tiny helper so the table-test rows stay readable.
func userMsg(content string) []*schema.Message {
	return []*schema.Message{{Role: schema.User, Content: content}}
}

// TestBuildArgs_RejectsMultiTurn pins the documented contract: -p mode
// only accepts a single user prompt.
func TestBuildArgs_RejectsMultiTurn(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.User, Content: "first"},
		{Role: schema.Assistant, Content: "reply"},
		{Role: schema.User, Content: "second"},
	}
	_, _, err := buildArgs("haiku", messages, &llm.RequestOptions{})
	if err == nil {
		t.Fatalf("expected error for multi-turn input, got nil")
	}
	var llmErr *llm.LLMError
	if !errors.As(err, &llmErr) || llmErr.Reason != "config" {
		t.Fatalf("expected config LLMError, got %v", err)
	}
}

// TestBuildArgs_ToolsEmitMCPConfig pins the tool-use translation: when
// opts.Tools is non-empty, buildArgs emits --mcp-config (inline JSON
// pointing the CLI back at the running binary) plus --allowedTools
// listing each tool by its mcp__knowledge__<name> qualified form.
func TestBuildArgs_ToolsEmitMCPConfig(t *testing.T) {
	tools := []*schema.ToolInfo{{Name: "search"}, {Name: "thoughts"}}
	args, _, err := buildArgs("haiku", userMsg("hi"), &llm.RequestOptions{Tools: tools})
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	got := strings.Join(args, " ")
	if !contains(args, "--mcp-config") {
		t.Errorf("args missing --mcp-config; got: %s", got)
	}
	if !contains(args, "--allowedTools") {
		t.Errorf("args missing --allowedTools; got: %s", got)
	}
	mcpIdx := -1
	for i, a := range args {
		if a == "--mcp-config" {
			mcpIdx = i
			break
		}
	}
	if mcpIdx < 0 || mcpIdx+1 >= len(args) {
		t.Fatalf("--mcp-config has no value; args: %s", got)
	}
	mcpJSON := args[mcpIdx+1]
	if !strings.Contains(mcpJSON, `"mcpServers"`) || !strings.Contains(mcpJSON, `"knowledge"`) {
		t.Errorf("--mcp-config JSON missing mcpServers.knowledge; got: %s", mcpJSON)
	}
	allowedIdx := -1
	for i, a := range args {
		if a == "--allowedTools" {
			allowedIdx = i
			break
		}
	}
	if allowedIdx < 0 || allowedIdx+1 >= len(args) {
		t.Fatalf("--allowedTools has no value; args: %s", got)
	}
	wantAllowed := "mcp__knowledge__search,mcp__knowledge__thoughts"
	if args[allowedIdx+1] != wantAllowed {
		t.Errorf("--allowedTools = %q, want %q", args[allowedIdx+1], wantAllowed)
	}
}

// TestBuildArgs_RejectsNonUserRole pins the user-role-only contract.
func TestBuildArgs_RejectsNonUserRole(t *testing.T) {
	messages := []*schema.Message{{Role: schema.System, Content: "I am the system"}}
	_, _, err := buildArgs("haiku", messages, &llm.RequestOptions{})
	if err == nil {
		t.Fatalf("expected error for system-role input, got nil")
	}
}

// TestBuildArgs_RejectsEmpty pins zero-message rejection.
func TestBuildArgs_RejectsEmpty(t *testing.T) {
	_, _, err := buildArgs("haiku", nil, &llm.RequestOptions{})
	if err == nil {
		t.Fatalf("expected error for empty messages, got nil")
	}
}

// TestBuildArgs_HappyPath_Defaults verifies the static-flag set.
func TestBuildArgs_HappyPath_Defaults(t *testing.T) {
	args, stdin, err := buildArgs("", userMsg("hello world"), &llm.RequestOptions{})
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	if stdin != "hello world" {
		t.Fatalf("stdin = %q, want %q", stdin, "hello world")
	}
	wantContains := []string{
		"-p",
		"--output-format", "json",
		"--no-session-persistence",
		"--disable-slash-commands",
	}
	// No --tools flag is emitted in the no-Tools path. With opts.Tools
	// empty there's nothing to allowlist.
	if contains(args, "--tools") {
		t.Errorf("args should not include --tools (Tools field empty); got: %s", strings.Join(args, " "))
	}
	// The no-Tools path (e.g. the summarizer) pins an empty, strict MCP
	// config so the CLI loads none of the user's configured MCP servers.
	if !contains(args, "--strict-mcp-config") {
		t.Errorf("no-tools args should include --strict-mcp-config; got: %s", strings.Join(args, " "))
	}
	mcpVal := ""
	for i, a := range args {
		if a == "--mcp-config" && i+1 < len(args) {
			mcpVal = args[i+1]
		}
	}
	if mcpVal != "{}" {
		t.Errorf("no-tools --mcp-config = %q, want %q; args: %s", mcpVal, "{}", strings.Join(args, " "))
	}
	got := strings.Join(args, " ")
	for i := range wantContains {
		if !contains(args, wantContains[i]) {
			t.Errorf("args missing %q; got: %s", wantContains[i], got)
		}
	}
}

// TestBuildArgs_HappyPath_Substrate verifies model + system + json-schema
// translation.
func TestBuildArgs_HappyPath_Substrate(t *testing.T) {
	rawSchema := json.RawMessage(`{"type":"object","properties":{"x":{"type":"string"}}}`)
	opts := &llm.RequestOptions{
		Model:        "claude-sonnet-4-7",
		SystemPrompt: "you are terse",
		ResponseFormat: &llm.ResponseFormat{
			Type:   "json_schema",
			Schema: rawSchema,
		},
	}
	args, _, err := buildArgs(opts.Model, userMsg("hi"), opts)
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}

	if !flagHasValue(args, "--model", "claude-sonnet-4-7") {
		t.Errorf("--model not threaded through: %v", args)
	}
	if !flagHasValue(args, "--system-prompt", "you are terse") {
		t.Errorf("--system-prompt not threaded through: %v", args)
	}
	if !flagHasValue(args, "--json-schema", string(rawSchema)) {
		t.Errorf("--json-schema not threaded through: %v", args)
	}
}

// TestBuildArgs_IgnoresUnsupported documents that fields the CLI does
// not honor are silently dropped without error. This pins behavior so
// future maintainers can't quietly add an error path that would break
// callers passing Temperature etc.
func TestBuildArgs_IgnoresUnsupported(t *testing.T) {
	temp := float32(0.5)
	topP := float32(0.9)
	topK := int32(40)
	opts := &llm.RequestOptions{
		Temperature:             &temp,
		TopP:                    &topP,
		TopK:                    &topK,
		MaxTokens:               1000,
		StopSequences:           []string{"STOP"},
		ExtendedThinking:        true,
		ThinkingBudget:          2048,
		DisableExtendedThinking: true,
		ReasoningEffort:         "high",
		BaseURL:                 "https://example.invalid",
		APIKey:                  "sk-should-be-ignored",
	}
	args, _, err := buildArgs("haiku", userMsg("hi"), opts)
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	for _, sub := range []string{
		"--temperature", "--top-p", "--top-k", "--max-tokens",
		"--stop", "--extended-thinking", "--reasoning-effort",
		"--base-url", "--api-key",
	} {
		if contains(args, sub) {
			t.Errorf("args unexpectedly contains %q (should be silently dropped): %v", sub, args)
		}
	}
}

// TestParseResponse_HappyPath verifies the envelope mapping for a
// success reply with usage and stop_reason.
func TestParseResponse_HappyPath(t *testing.T) {
	body := []byte(`{
		"type":"result","subtype":"success","is_error":false,
		"result":"hello caller","stop_reason":"end_turn",
		"usage":{"input_tokens":12,"output_tokens":34}
	}`)
	resp, err := parseResponse(body, "claude-sonnet-4-7")
	if err != nil {
		t.Fatalf("parseResponse: %v", err)
	}
	if resp.Content != "hello caller" {
		t.Fatalf("Content = %q, want %q", resp.Content, "hello caller")
	}
	if resp.FinishReason != llm.FinishReasonEndTurn {
		t.Fatalf("FinishReason = %q, want %q", resp.FinishReason, llm.FinishReasonEndTurn)
	}
	if resp.Usage.InputTokens != 12 || resp.Usage.OutputTokens != 34 {
		t.Fatalf("Usage = %+v, want {12, 34}", resp.Usage)
	}
	if resp.Provider != llm.ProviderClaudeCLI {
		t.Fatalf("Provider = %q, want %q", resp.Provider, llm.ProviderClaudeCLI)
	}
	if resp.Model != llm.Model("claude-sonnet-4-7") {
		t.Fatalf("Model = %q, want %q", resp.Model, "claude-sonnet-4-7")
	}
	if string(resp.Raw) != string(body) {
		t.Fatalf("Raw must round-trip the verbatim body bytes")
	}
	if resp.ThinkingContent != "" || resp.ReasoningContent != "" || len(resp.ToolCalls) != 0 {
		t.Fatalf("CLI provider must not surface thinking/reasoning/tools: %+v", resp)
	}
}

// TestParseResponse_StructuredOutputPreferred verifies that when the CLI
// populates structured_output (because --json-schema was supplied), the
// parser uses the validated JSON for Content rather than the free-form
// result string. Real-world incident: claude-cli returned a markdown
// commentary in `result` plus the schema-conformant payload in
// `structured_output`; the summarizer JSON-parsed the commentary and
// failed 500+ chunks before the bug was caught.
func TestParseResponse_StructuredOutputPreferred(t *testing.T) {
	structured := `{"items":[{"summary":"first","keywords":["a","b","c"]}]}`
	body := []byte(`{
		"type":"result","subtype":"success","is_error":false,
		"result":"Done! I summarized the chunks for you.",
		"structured_output":` + structured + `,
		"stop_reason":"end_turn",
		"usage":{"input_tokens":10,"output_tokens":20}
	}`)
	resp, err := parseResponse(body, "claude-haiku-4-5")
	if err != nil {
		t.Fatalf("parseResponse: %v", err)
	}
	if resp.Content != structured {
		t.Errorf("Content = %q, want structured_output payload %q", resp.Content, structured)
	}
}

// TestParseResponse_IsErrorIsClassified verifies that envelope-level
// errors surface as LLMError with appropriate transient classification.
func TestParseResponse_IsErrorIsClassified(t *testing.T) {
	body := []byte(`{"is_error":true,"result":"HTTP 429 rate limit exceeded"}`)
	_, err := parseResponse(body, "haiku")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	var llmErr *llm.LLMError
	if !errors.As(err, &llmErr) {
		t.Fatalf("expected *llm.LLMError, got %T", err)
	}
	if llmErr.Reason != "cli_response_error" {
		t.Fatalf("Reason = %q, want %q", llmErr.Reason, "cli_response_error")
	}
	if !llmErr.Transient {
		t.Fatalf("rate-limit envelope must be transient")
	}
}

// TestParseResponse_BadJSON pins the parse-error path.
func TestParseResponse_BadJSON(t *testing.T) {
	_, err := parseResponse([]byte("not json"), "haiku")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	var llmErr *llm.LLMError
	if !errors.As(err, &llmErr) || llmErr.Reason != "parse_cli_response" {
		t.Fatalf("expected parse_cli_response LLMError, got %v", err)
	}
}

// TestMapFinishReason spot-checks the stop_reason → FinishReason mapping.
func TestMapFinishReason(t *testing.T) {
	cases := map[string]llm.FinishReason{
		"end_turn":      llm.FinishReasonEndTurn,
		"tool_use":      llm.FinishReasonToolUse,
		"max_tokens":    llm.FinishReasonMaxTokens,
		"stop_sequence": llm.FinishReasonStopSequence,
		"":              llm.FinishReasonOther,
		"weird":         llm.FinishReasonOther,
	}
	for in, want := range cases {
		if got := mapFinishReason(in); got != want {
			t.Errorf("mapFinishReason(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestGenerate_EndToEnd is the criterion test for step 3 / criterion
// d381c01c87ba03742801bc74f18c3ba0: "Generate end-to-end test with fake
// claude binary on PATH passes". Wires a fake claude binary on PATH that
// emits the real CLI's JSON envelope shape; verifies the substrate
// Generate call returns a populated llm.Response.
func TestGenerate_EndToEnd(t *testing.T) {
	envelope := `{"type":"result","subtype":"success","is_error":false,` +
		`"result":"the answer is 42","stop_reason":"end_turn",` +
		`"usage":{"input_tokens":7,"output_tokens":11}}`
	bin := writeFakeClaudeBin(t, `cat > /dev/null
cat <<'EOF'
`+envelope+`
EOF`)

	cfg := &llm.Config{
		Provider: llm.ProviderClaudeCLI,
		CLIBin:   bin,
		Model:    "claude-sonnet-4-7",
	}
	c, err := llm.NewClient(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	resp, err := c.Generate(
		context.Background(),
		userMsg("what is the answer to life?"),
		llm.WithSystemPrompt("be terse"),
	)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if resp.Content != "the answer is 42" {
		t.Fatalf("Content = %q, want %q", resp.Content, "the answer is 42")
	}
	if resp.FinishReason != llm.FinishReasonEndTurn {
		t.Fatalf("FinishReason = %q, want %q", resp.FinishReason, llm.FinishReasonEndTurn)
	}
	if resp.Usage.InputTokens != 7 || resp.Usage.OutputTokens != 11 {
		t.Fatalf("Usage = %+v, want {7, 11}", resp.Usage)
	}
	if resp.Model != llm.Model("claude-sonnet-4-7") {
		t.Fatalf("Model = %q, want %q", resp.Model, "claude-sonnet-4-7")
	}
	if resp.Provider != llm.ProviderClaudeCLI {
		t.Fatalf("Provider = %q, want %q", resp.Provider, llm.ProviderClaudeCLI)
	}

	// Usage tracking must accumulate on the BaseService.
	svc := c.(*Service)
	if got := svc.GetUsage(); got.InputTokens != 7 || got.OutputTokens != 11 {
		t.Fatalf("svc.GetUsage() = %+v, want {7, 11}", got)
	}
}

// TestGenerate_PropagatesArgs is a structural test verifying the
// translate layer's argv reaches the subprocess. The fake echoes its
// argv on stdout so the assertion can run on the substrate boundary.
//
// We can't simultaneously echo argv AND emit a valid JSON envelope from
// the same fake (Generate would fail to parse), so this test inspects
// the LLMError's parse failure for the echoed args rather than running
// Generate to completion.
func TestGenerate_PropagatesArgs(t *testing.T) {
	bin := writeFakeClaudeBin(t, `cat > /dev/null
echo "$@"`)

	cfg := &llm.Config{
		Provider: llm.ProviderClaudeCLI,
		CLIBin:   bin,
		Model:    "haiku",
	}
	c, err := llm.NewClient(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = c.Generate(
		context.Background(),
		userMsg("ignored"),
		llm.WithSystemPrompt("the-system-prompt"),
	)
	if err == nil {
		t.Fatalf("expected parse error, got nil")
	}
	// The echoed argv shows up in the parse_cli_response error chain.
	if !strings.Contains(err.Error(), "--system-prompt") {
		t.Fatalf("expected --system-prompt in echoed argv, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "the-system-prompt") {
		t.Fatalf("expected system-prompt value in echoed argv, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "haiku") {
		t.Fatalf("expected --model haiku in echoed argv, got %q", err.Error())
	}
}

// contains is a small helper for argv inspection.
func contains(args []string, want string) bool {
	return slices.Contains(args, want)
}

// flagHasValue checks that args contains `flag` followed immediately by
// `value` — the shape every --flag/value pair takes.
func flagHasValue(args []string, flag, value string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}
