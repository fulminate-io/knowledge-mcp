// SPDX-License-Identifier: Apache-2.0

package codexcli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/cloudwego/eino/schema"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// codex CLI argv used on every Generate call. Pinned here (not built up
// conditionally per option) so the wire shape is auditable in one place.
//
//   - "exec"        — non-interactive subcommand. The interactive form requires a TTY.
//   - "--json"      — emit one JSON event per line on stdout. The success path
//     ends with a turn.completed event we can parse for usage.
//   - "--skip-git-repo-check" — codex normally refuses to run outside a git
//     repo; the substrate's Generate has no opinion about cwd, so we waive
//     the check.
//   - "--dangerously-bypass-approvals-and-sandbox" — Generate is a single
//     prompt-in / response-out call with no shell command execution intent
//     (the substrate doesn't surface codex's tool use at all). The bypass
//     suppresses approval prompts that would otherwise hang stdin. The
//     "dangerously" name is codex's own; the runtime risk for our use case
//     is bounded because we don't ship a tool list and codex's prompt-only
//     turns can't synthesize shell commands without one.
//   - "--ephemeral" — don't persist the session to ~/.codex/sessions. The
//     substrate's Generate is stateless; persistence would just litter the
//     user's home dir with single-turn artifacts.
//   - "--ignore-user-config" — skip ~/.codex/config.toml. The substrate
//     never wants the user's MCP servers loaded (we don't surface codex
//     tool calls in the Response shape, and a misconfigured MCP entry
//     would silently slow every Generate). auth still resolves via
//     $CODEX_HOME, which the substrate inherits.
//   - "--ignore-rules" — skip .rules files in the cwd. Same reasoning as
//     --ignore-user-config: per-project rules would only affect Generate
//     calls in ways callers can't observe through the substrate's
//     stateless interface.
//
// Trailing "-" is the prompt argument; codex reads stdin when the prompt is
// "-", which is what our subprocess feeds it.
var baseArgs = []string{
	"exec",
	"--json",
	"--skip-git-repo-check",
	"--dangerously-bypass-approvals-and-sandbox",
	"--ephemeral",
	"--ignore-user-config",
	"--ignore-rules",
}

// leanConfigOverrides are -c key=value pairs codex always honors via the
// general TOML override mechanism. Disable the default-on tools the
// substrate never invokes through (codex routes tool use through MCP
// servers in user config, which we've already stripped via
// --ignore-user-config) so they don't inflate the request's tool list and
// reasoning-summary stream is suppressed (we don't surface ReasoningContent
// in the Response).
//
// Note on image_gen: codex bundles an image_gen tool that the responses
// API rejects under reasoning.effort="minimal" with "The following tools
// cannot be used with reasoning.effort 'minimal': image_gen." Disabling
// the documented `tools.image_gen` / `features.image_gen` keys does NOT
// remove image_gen from the request — codex hard-codes it. "low" is the
// practical floor for reasoning_effort.
var leanConfigOverrides = []string{
	`features.shell_tool=false`,
	`features.unified_exec=false`,
	`tools.view_image=false`,
	`web_search="disabled"`,
	`project_doc_max_bytes=0`,
	`model_reasoning_summary="none"`,
	`hide_agent_reasoning=true`,
}

// defaultReasoningEffort is the substrate's floor when the caller does
// not pin ReasoningEffort. codex's per-model default leans medium for
// frontier models and is wasteful for the substrate's schema-constrained
// single-shot Generate path (summarizer, precheck). "low" is the
// cheapest setting compatible with codex's hard-coded image_gen tool;
// callers needing deeper reasoning supply WithReasoningEffort explicitly.
const defaultReasoningEffort = "low"

// systemPromptDelimiter separates the system prompt from the user prompt
// when both are present. Codex has no `--system` flag (verified against
// `codex exec --help` for v0.128.0), so the substrate's SystemPrompt field
// is folded into the prompt body with a clearly labeled section.
//
// We deliberately format this as plain English headers rather than e.g. a
// special token — codex's prompt parser is just a string, and named
// sections tend to round-trip more cleanly through whatever in-flight
// reformatting codex applies.
const systemPromptDelimiter = "\n\n----\n\n"

// buildArgs translates messages + RequestOptions into codex argv + stdin.
// Returns the argv to pass after the binary, the stdin body to write, an
// optional cleanup func (non-nil when buildArgs allocated a tempfile for
// --output-schema), and any translation error wrapped in *llm.LLMError.
//
// Knobs intentionally NOT translated and the reason for each:
//
//   - Tools — codex configures external tools via MCP servers in
//     ~/.codex/config.toml, not per-call flags. We refuse non-empty Tools
//     rather than silently drop, because callers expecting tool-use
//     round-trip would otherwise get a confusing "no tool calls in
//     response" failure mode.
//   - Temperature — codex exec has no --temperature flag and exposes no
//     `-c` config key for sampling temperature. Documented as not
//     surfaced. Callers needing temperature should use the openai
//     provider.
//   - TopP — same as Temperature; no codex flag, no config key.
//   - TopK — same as Temperature; no codex flag, no config key.
//   - MaxTokens — codex exec has no max-output-tokens flag and the
//     responses-API path it drives doesn't expose one either at this
//     layer. Documented as not surfaced.
//   - StopSequences — no codex flag.
//   - ExtendedThinking / ThinkingBudget / DisableExtendedThinking — codex
//     doesn't model thinking as an enabled/budget pair; reasoning is
//     always-on for o-series codex models. Documented as not surfaced.
//   - BaseURL / APIKey — codex resolves auth from `codex login` (ChatGPT
//     account) or the OPENAI_API_KEY environment variable. Per-call
//     credential override would require setting env on the subprocess;
//     we currently inherit the caller's environment unmodified. Documented
//     as not surfaced.
//
// Knobs that ARE translated:
//
//   - Model → -m <model>
//   - SystemPrompt → folded into the stdin body before the user prompt
//     (codex has no --system flag, see systemPromptDelimiter above)
//   - ResponseFormat with Type=="json_schema" → schema body written to a
//     tempfile, --output-schema <path> appended to argv. Cleanup func
//     deletes the tempfile after the call. Other ResponseFormat types
//     (e.g. "json_object") are dropped since codex requires a schema body
//     for the --output-schema flag — there's no flag-only "json mode".
//   - ReasoningEffort → -c model_reasoning_effort=<value>. Codex respects
//     low/medium/high; an empty value falls through (drop, leaving codex
//     default).
//   - Tools → -c mcp_servers.knowledge.{url,enabled_tools} overrides.
//     codex has no inline MCP-config flag (claude-cli's --mcp-config), so the
//     dream-worker tool surface is injected as config: a streamable-HTTP MCP
//     server "knowledge" whose url is the shared knowledge daemon's loopback
//     /mcp endpoint (no spawned child — the daemon runs one shared runtime).
//     enabled_tools is the bare-name allowlist (codex scopes it to the server
//     id). See buildMCPOverrides. --ignore-user-config in baseArgs means ONLY
//     this injected server loads — the codex equivalent of claude's
//     --strict-mcp-config.
func buildArgs(model llm.Model, messages []*schema.Message, options *llm.RequestOptions) ([]string, string, func(), error) {
	system, user, err := translateMessages(options.SystemPrompt, messages)
	if err != nil {
		return nil, "", nil, &llm.LLMError{
			Transient: false,
			Reason:    "translate_request",
			Cause:     err,
		}
	}

	stdin := buildPromptBody(system, user)

	args := append([]string{}, baseArgs...)
	for _, kv := range leanConfigOverrides {
		args = append(args, "-c", kv)
	}

	// Tool-use (dream worker) → inject the knowledge MCP server as config.
	// The summarizer / precheck paths pass no tools and skip this entirely.
	if len(options.Tools) > 0 {
		mcpOverrides, err := buildMCPOverrides(options.Tools)
		if err != nil {
			return nil, "", nil, err
		}
		args = append(args, mcpOverrides...)
	}

	args = append(args, "-m", string(model))

	// ReasoningEffort: explicit caller value wins; otherwise apply the
	// substrate floor so codex's default-medium doesn't quietly inflate
	// the bill on schema-constrained single-shot calls.
	effort := options.ReasoningEffort
	if effort == "" {
		effort = defaultReasoningEffort
	}
	// `-c key=value` accepts a TOML literal on the value side; for a
	// bare identifier we pass it quoted so codex parses it as a string
	// rather than a bare TOML token (low/medium/high are valid bare
	// idents but explicit quoting keeps the contract robust if codex
	// ever adds an effort variant that isn't a bare ident).
	args = append(args, "-c", fmt.Sprintf("model_reasoning_effort=%q", effort))

	cleanup := func() {}
	if options.ResponseFormat != nil && options.ResponseFormat.Type == "json_schema" && options.ResponseFormat.Schema != nil {
		path, schemaCleanup, err := writeSchemaTempfile(options.ResponseFormat.Schema)
		if err != nil {
			return nil, "", nil, &llm.LLMError{
				Transient: false,
				Reason:    "translate_request",
				Cause:     fmt.Errorf("write output schema tempfile: %w", err),
			}
		}
		args = append(args, "--output-schema", path)
		cleanup = schemaCleanup
	}

	// Trailing "-" forces codex to read the prompt from stdin instead of
	// expecting it as the trailing positional argument. Keeps very long
	// prompts off the argv (some platforms cap argv length).
	args = append(args, "-")

	return args, stdin, cleanup, nil
}

// buildMCPOverrides returns the `-c` config-override flag pairs that register
// the knowledge MCP server for a tool-using (dream-worker) Generate call.
// codex has no inline MCP-config flag, so the server is injected as config:
//
//	mcp_servers.knowledge.url           = <daemon loopback /mcp url>
//	mcp_servers.knowledge.enabled_tools = [<bare tool names>]
//
// The server is the shared `knowledge serve` daemon's loopback streamable-
// HTTP endpoint (the same url editors register via `codex mcp add --url`),
// NOT a per-call stdio child of this process — so no os.Executable / argv
// inheritance / fork-bomb guard is needed here (the daemon runs a single
// shared runtime). enabled_tools carries BARE tool names (search, thoughts,
// …): codex scopes the allowlist to the server id, unlike claude-cli's
// --allowedTools which needs the mcp__knowledge__ qualifier.
//
// Encoding: the `-c value` side is parsed as TOML. A JSON array of strings is
// also a valid TOML array of strings (both use the same double-quote +
// backslash escape set for ASCII tool names), so json.Marshal is the encoder
// for enabled_tools; %q quotes the url as a TOML basic string.
func buildMCPOverrides(tools []*schema.ToolInfo) ([]string, error) {
	url := fmt.Sprintf("http://127.0.0.1:%d/mcp", graphclient.DefaultMCPHTTPPort)
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		if t == nil || t.Name == "" {
			continue
		}
		names = append(names, t.Name)
	}
	namesJSON, err := json.Marshal(names)
	if err != nil {
		return nil, &llm.LLMError{Transient: false, Reason: "config", Cause: fmt.Errorf("codex-cli: marshal enabled_tools: %w", err)}
	}
	return []string{
		"-c", fmt.Sprintf("mcp_servers.knowledge.url=%q", url),
		"-c", "mcp_servers.knowledge.enabled_tools=" + string(namesJSON),
	}, nil
}

// translateMessages collapses the substrate's message list into a single
// system prompt + single user prompt pair.
//
// Codex's exec mode is single-turn — the caller speaks once, codex replies
// once. Multi-turn []*schema.Message is incompatible with that model: there
// is no codex-side surface for assistant or tool messages from a prior turn
// other than `codex exec resume <session_id>`, which the substrate's
// stateless Generate cannot drive. Rather than silently rewrite the
// conversation (e.g. concatenate every assistant message into the user
// prompt) we refuse — silent rewrites are the kind of substrate divergence
// that surprises callers later.
//
// The accepted shape:
//
//   - Zero or more schema.System messages → concatenated with the
//     RequestOptions.SystemPrompt (which lands first if non-empty)
//   - Exactly one schema.User message → the user prompt body
//   - Zero schema.Assistant / schema.Tool messages → multi-turn would have
//     to round-trip through codex resume, which the substrate doesn't
//     model
//
// Empty messages return an error — codex needs something to chew on.
func translateMessages(systemPrompt string, messages []*schema.Message) (string, string, error) {
	system := strings.TrimSpace(systemPrompt)
	var user string
	userSeen := false

	for i, msg := range messages {
		if msg == nil {
			return "", "", fmt.Errorf("messages[%d] is nil", i)
		}
		switch msg.Role {
		case schema.System:
			if system == "" {
				system = msg.Content
			} else {
				system = system + "\n\n" + msg.Content
			}
		case schema.User:
			if userSeen {
				return "", "", fmt.Errorf("messages[%d]: codex-cli accepts a single user message; got %d", i, countByRole(messages, schema.User))
			}
			user = msg.Content
			userSeen = true
		case schema.Assistant, schema.Tool:
			return "", "", &multiTurnError{role: string(msg.Role), index: i}
		default:
			return "", "", fmt.Errorf("messages[%d]: unsupported role %q", i, msg.Role)
		}
	}

	if !userSeen && system == "" {
		return "", "", fmt.Errorf("codex-cli requires a non-empty user message or system prompt")
	}

	return system, user, nil
}

// multiTurnError surfaces as an LLMError with Reason "multi_turn_not_supported"
// when the input contains assistant or tool messages. We keep this typed so
// the orchestrator's errors.As check is precise — bare fmt.Errorf would
// require substring sniffing.
type multiTurnError struct {
	role  string
	index int
}

func (e *multiTurnError) Error() string {
	return fmt.Sprintf("messages[%d]: codex-cli does not accept %q messages — exec mode is single-turn", e.index, e.role)
}

// countByRole counts messages with the given role, ignoring nils. Helper for
// translateMessages's diagnostic message.
func countByRole(messages []*schema.Message, role schema.RoleType) int {
	n := 0
	for _, m := range messages {
		if m != nil && m.Role == role {
			n++
		}
	}
	return n
}

// buildPromptBody folds the system prompt and user prompt into the single
// stdin body codex consumes. When both are present they are joined with
// systemPromptDelimiter and clearly labeled. When only one is present the
// body is exactly that string (no decoration).
func buildPromptBody(system, user string) string {
	system = strings.TrimSpace(system)
	user = strings.TrimSpace(user)
	switch {
	case system == "" && user == "":
		return ""
	case system == "":
		return user
	case user == "":
		return "SYSTEM:\n" + system
	default:
		return "SYSTEM:\n" + system + systemPromptDelimiter + "USER:\n" + user
	}
}

// writeSchemaTempfile marshals schema to JSON and writes it to a tempfile
// for codex's --output-schema flag. Returns the path and a cleanup func
// that deletes the tempfile. Cleanup is safe to call multiple times.
func writeSchemaTempfile(schemaBody any) (string, func(), error) {
	data, err := json.Marshal(schemaBody)
	if err != nil {
		return "", nil, fmt.Errorf("marshal schema: %w", err)
	}
	f, err := os.CreateTemp("", "codex-output-schema-*.json")
	if err != nil {
		return "", nil, fmt.Errorf("create tempfile: %w", err)
	}
	path := f.Name()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", nil, fmt.Errorf("write schema: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", nil, fmt.Errorf("close schema tempfile: %w", err)
	}
	cleanup := func() { _ = os.Remove(path) }
	return path, cleanup, nil
}

// JSONL event parsing for codex's --json output lives in parse.go (the
// codexEvent / parseResponse pair). Translation (request-side) is the
// concern of this file.
