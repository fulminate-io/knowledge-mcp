// SPDX-License-Identifier: Apache-2.0

// Package claudecli implements the [llm.Client] substrate by shelling out to
// Anthropic's `claude` CLI in `-p` (prompt) mode.
//
// Why a CLI provider exists alongside the API providers: the claude CLI
// authenticates through the user's local shell login (`claude login`) and
// honors organization, billing, and rate-limit policy enforced upstream.
// Knowledge bundles this provider so local-development workflows can use the
// same login the user already has, without copying API keys into Config.
//
// Translation rules differ from the API providers in two ways:
//
//   - Single-turn only. The CLI's `-p` mode accepts one user prompt on stdin.
//     A multi-turn []*schema.Message slice cannot be expressed; Generate
//     returns an LLMError when more than one user message is supplied. See
//     translate.go for the full validation contract.
//   - Many substrate fields are not honored. Temperature, TopP, TopK,
//     MaxTokens, StopSequences, ExtendedThinking, ThinkingBudget,
//     DisableExtendedThinking, ReasoningEffort, BaseURL, and APIKey have no
//     CLI flags and are silently dropped (documented per-field in
//     translate.go).
//
// Tool-use is supported via --mcp-config: when opts.Tools is non-empty,
// buildArgs emits an MCP config pointing the CLI at the shared knowledge
// daemon's loopback HTTP MCP endpoint (which fronts the knowledge graph)
// plus --allowedTools to pre-authorize each tool. The CLI runs its own
// ReAct loop and returns a single final text response; intermediate
// tool_use blocks do not round-trip to the substrate. eino's
// react.NewAgent sees one text response and
// terminates the loop, treating the CLI's answer as the final output.
//
// The provider self-registers under [llm.ProviderClaudeCLI] from init() so a
// side-effect import (`_ "github.com/.../domains/llm/claudecli"`) is enough
// to make `llm.NewClient(ctx, &llm.Config{Provider: llm.ProviderClaudeCLI})`
// succeed.
package claudecli

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/cloudwego/eino/schema"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// init registers the claude-cli factory at package import time.
func init() {
	llm.RegisterProvider(llm.ProviderClaudeCLI, NewService)
}

// Service is the claude-cli implementation of [llm.Client]. It embeds
// [*llm.BaseService] for shared Provider() identity and aggregate
// token-usage tracking.
//
// cliBin is the resolved absolute path to the `claude` binary; resolved once
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

// NewService is the registered [llm.ProviderFactory] for claude-cli.
//
// Resolution order for the CLI binary:
//   - cfg.CLIBin if set (absolute or PATH-resolvable via exec.LookPath)
//   - exec.LookPath("claude") otherwise
//
// Returns *llm.LLMError (Reason: "cli_not_found", Transient: false) when no
// claude binary can be resolved. The substrate's Validate already accepted
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
		BaseService:  llm.NewBaseService(llm.ProviderClaudeCLI),
		cliBin:       bin,
		defaultModel: cfg.Model,
	}, nil
}

// resolveCLIBin returns the absolute path to the claude binary, honoring an
// explicit override before falling back to PATH lookup.
//
// An override that is an absolute path is returned verbatim if the file
// exists and is executable; otherwise the override is fed back through
// exec.LookPath so callers can override with a bare binary name on a custom
// PATH (e.g. "fake-claude" pointing at a tempdir-injected stub).
func resolveCLIBin(override string) (string, error) {
	if override != "" {
		// LookPath accepts both bare names and absolute paths and verifies
		// executability either way. Using it uniformly avoids a second
		// "is it executable" check.
		p, err := exec.LookPath(override)
		if err != nil {
			return "", fmt.Errorf("claude CLI override %q not executable: %w", override, err)
		}
		return p, nil
	}
	p, err := exec.LookPath("claude")
	if err != nil {
		return "", fmt.Errorf("claude CLI not found in PATH: %w", err)
	}
	return p, nil
}

// Generate executes one claude CLI invocation in `-p` (prompt) mode.
//
// The full implementation is wired in subsequent steps (subprocess.go +
// translate.go). The skeleton checkpoint preserves the [llm.Client] shape so
// the registry/factory pipeline compiles end-to-end before the subprocess
// glue lands.
func (s *Service) Generate(ctx context.Context, messages []*schema.Message, opts ...llm.Option) (*llm.Response, error) {
	options := llm.ApplyOptions(opts...)

	model := options.Model
	if model == "" {
		model = s.defaultModel
	}

	args, stdin, err := buildArgs(model, messages, options)
	if err != nil {
		return nil, err
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
