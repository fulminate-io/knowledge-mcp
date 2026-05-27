// SPDX-License-Identifier: Apache-2.0

// Package codexcli implements the [llm.Client] substrate by shelling out to
// OpenAI's `codex` CLI in `exec --json` (non-interactive) mode.
//
// Why a CLI provider exists alongside the OpenAI API provider: the codex CLI
// authenticates through the user's local shell login (`codex login`, ChatGPT
// account, or OPENAI_API_KEY env var) and honors organization, billing, and
// rate-limit policy enforced upstream. Knowledge bundles this provider so
// local-development workflows can reuse the user's existing codex auth
// without copying API keys into Config.
//
// Translation rules differ from the OpenAI API provider in two ways:
//
//   - Single-turn only. Codex's `exec` mode accepts one prompt on stdin (or as
//     a positional argument). A multi-turn []*schema.Message slice cannot be
//     expressed faithfully — codex sessions are persisted server-side and
//     resumed via `codex exec resume <session_id>`, which the substrate's
//     stateless Generate shape can't drive. Generate returns an LLMError when
//     the input contains assistant or tool messages. See translate.go for the
//     full validation contract.
//   - Most substrate sampling knobs are not honored. Temperature, TopP, TopK,
//     MaxTokens, StopSequences, ExtendedThinking, ThinkingBudget,
//     DisableExtendedThinking, BaseURL, and APIKey have no codex-exec flags
//     and are silently dropped (documented per-field in translate.go).
//     ReasoningEffort IS honored via `-c model_reasoning_effort=<value>`.
//     ResponseFormat with Type=="json_schema" is honored via the
//     `--output-schema <file>` flag — the schema body is written to a
//     tempfile for the duration of the call.
//     Tools are explicitly NOT supported (codex routes external tools through
//     MCP servers configured globally in `~/.codex/config.toml`, not per
//     Generate call); Generate returns an LLMError when len(opts.Tools) > 0
//     so callers expecting tool-use round-trip hit a clear failure rather
//     than silent dropping.
//
// The provider self-registers under [llm.ProviderCodexCLI] from init() so a
// side-effect import (`_ "github.com/.../domains/llm/codexcli"`) is enough
// to make `llm.NewClient(ctx, &llm.Config{Provider: llm.ProviderCodexCLI})`
// succeed.
package codexcli

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/cloudwego/eino/schema"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// init registers the codex-cli factory at package import time.
func init() {
	llm.RegisterProvider(llm.ProviderCodexCLI, NewService)
}

// Service is the codex-cli implementation of [llm.Client]. It embeds
// [*llm.BaseService] for shared Provider() identity and aggregate
// token-usage tracking.
//
// cliBin is the resolved absolute path to the `codex` binary; resolved once
// at construction so PATH lookups don't repeat per Generate call. defaultModel
// is consulted only when a Generate call doesn't supply WithModel.
type Service struct {
	*llm.BaseService

	cliBin       string
	defaultModel llm.Model
}

// Compile-time guard: Service must satisfy llm.Client. If the interface
// shape changes, the build fails here rather than at the registry call site.
var _ llm.Client = (*Service)(nil)

// NewService is the registered [llm.ProviderFactory] for codex-cli.
//
// Resolution order for the CLI binary:
//   - cfg.CLIBin if set (absolute or PATH-resolvable via exec.LookPath)
//   - exec.LookPath("codex") otherwise
//
// Returns *llm.LLMError (Reason: "cli_not_found", Transient: false) when no
// codex binary can be resolved. The substrate's Validate already accepted
// an empty CLIBin (CLI providers have no required Config fields) so the
// resolution failure surfaces here rather than at Validate time.
func NewService(_ context.Context, cfg *llm.Config) (llm.Client, error) {
	if cfg == nil {
		return nil, fmt.Errorf("%w: nil config", llm.ErrInvalidConfig)
	}
	bin, err := resolveCLIBin(cfg.CLIBin)
	if err != nil {
		return nil, &llm.LLMError{Transient: false, Reason: "cli_not_found", Cause: err}
	}
	return &Service{
		BaseService:  llm.NewBaseService(llm.ProviderCodexCLI),
		cliBin:       bin,
		defaultModel: cfg.Model,
	}, nil
}

// resolveCLIBin returns the absolute path to the codex binary, honoring an
// explicit override before falling back to PATH lookup.
//
// An override is fed back through exec.LookPath so callers can override with
// a bare binary name on a custom PATH (e.g. "fake-codex" pointing at a
// tempdir-injected stub) or with an absolute path. LookPath verifies
// executability either way.
func resolveCLIBin(override string) (string, error) {
	if override != "" {
		p, err := exec.LookPath(override)
		if err != nil {
			return "", fmt.Errorf("codex CLI override %q not executable: %w", override, err)
		}
		return p, nil
	}
	p, err := exec.LookPath("codex")
	if err != nil {
		return "", fmt.Errorf("codex CLI not found in PATH: %w", err)
	}
	return p, nil
}

// Generate executes one codex CLI invocation in `exec --json` mode.
//
// Per the [llm.Client] contract this is a single turn — there is no
// caller-hidden tool-use loop. Codex routes external tools through MCP
// servers (global config), not per-call flags, so the substrate's Tools
// field cannot be honored; non-empty Tools rejects with LLMError.
//
// Errors are returned as *llm.LLMError so callers can distinguish transient
// (subprocess timeout, codex network reconnect) from terminal (config error,
// non-zero exit, parse failure) via llm.IsTransient. The fan-out:
//
//   - resolveCLIBin failure at construction → "cli_not_found" (terminal)
//   - missing model → ErrInvalidConfig
//   - Tools non-empty → "tools_not_supported" (terminal)
//   - assistant/tool messages in input → "multi_turn_not_supported" (terminal)
//   - subprocess non-zero exit → "subprocess_failed" (terminal)
//   - subprocess timeout (ctx canceled) → "subprocess_timeout" (transient)
//   - codex turn.failed event → "turn_failed" (terminal — auth/quota errors
//     surface here; classification leans terminal because retrying without
//     operator action rarely helps for codex)
//   - parse failure → "parse_response" (terminal)
func (s *Service) Generate(ctx context.Context, messages []*schema.Message, opts ...llm.Option) (*llm.Response, error) {
	options := llm.ApplyOptions(opts...)

	model := options.Model
	if model == "" {
		model = s.defaultModel
	}
	if model == "" {
		return nil, fmt.Errorf("%w: codex-cli requires a model (set Config.Model or pass WithModel)", llm.ErrInvalidConfig)
	}

	args, stdin, schemaCleanup, err := buildArgs(model, messages, options)
	if err != nil {
		return nil, err
	}
	if schemaCleanup != nil {
		defer schemaCleanup()
	}

	stdout, err := runCLI(ctx, s.cliBin, args, stdin, options.InheritWorkdir)
	if err != nil {
		return nil, err
	}

	resp, err := parseResponse(stdout, model)
	if err != nil {
		return nil, err
	}
	s.RecordUsage(resp.Usage)
	return resp, nil
}
