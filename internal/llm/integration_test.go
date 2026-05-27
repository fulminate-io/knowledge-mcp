package llm_test

import (
	"context"
	"errors"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
	_ "github.com/fulminate-io/knowledge-mcp/internal/llm/anthropic"
	_ "github.com/fulminate-io/knowledge-mcp/internal/llm/claudecli"
	_ "github.com/fulminate-io/knowledge-mcp/internal/llm/codexcli"
	_ "github.com/fulminate-io/knowledge-mcp/internal/llm/gemini"
	_ "github.com/fulminate-io/knowledge-mcp/internal/llm/openai"
)

// TestProvidersRegisterOnImport pins the substrate's promise: blank-importing a
// provider sub-package registers its factory. If a future refactor moves a
// provider's init() out of its package root, this test is the early warning.
func TestProvidersRegisterOnImport(t *testing.T) {
	for _, p := range []llm.Provider{
		llm.ProviderOpenAI,
		llm.ProviderAnthropic,
		llm.ProviderGemini,
		llm.ProviderClaudeCLI,
		llm.ProviderCodexCLI,
	} {
		if !llm.HasProvider(p) {
			t.Errorf("provider %q not registered after blank import", p)
		}
	}
	if got, want := len(llm.ListProviders()), 5; got != want {
		t.Errorf("ListProviders() len = %d, want %d (got %v)", got, want, llm.ListProviders())
	}
}

// TestNewClientWithFakeFactory shows the cross-package testing pattern: a
// caller registers a factory that hands out a FakeClient under a real
// Provider id, then exercises NewClient → Generate end-to-end. Tests in
// other packages that want their code-under-test to call NewClient (rather
// than receive an llm.Client directly) follow this pattern.
//
// We swap ProviderClaudeCLI specifically because its Validate path has no
// APIKey requirement, which keeps the example minimal. The override is
// destructive for the rest of this test binary, but that's fine — every
// later test either constructs its own provider Service directly or runs in
// a different test binary.
func TestNewClientWithFakeFactory(t *testing.T) {
	fake := llm.NewFakeClient(&llm.Response{
		Content:      "fake reply",
		FinishReason: llm.FinishReasonEndTurn,
		Usage:        llm.TokenUsage{InputTokens: 7, OutputTokens: 3},
	})

	llm.RegisterProvider(llm.ProviderClaudeCLI, func(_ context.Context, _ *llm.Config) (llm.Client, error) {
		return fake, nil
	})

	client, err := llm.NewClient(context.Background(), &llm.Config{
		Provider: llm.ProviderClaudeCLI,
		Model:    "claude-test",
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	resp, err := client.Generate(context.Background(),
		[]*schema.Message{{Role: schema.User, Content: "hello"}},
		llm.WithMaxTokens(8),
		llm.WithTemperature(0.1),
		llm.WithTopP(0.9),
		llm.WithExtendedThinking(2048),
	)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Content != "fake reply" {
		t.Errorf("content = %q, want %q", resp.Content, "fake reply")
	}
	if resp.FinishReason != llm.FinishReasonEndTurn {
		t.Errorf("finish = %s, want %s", resp.FinishReason, llm.FinishReasonEndTurn)
	}

	calls := fake.Calls()
	if len(calls) != 1 {
		t.Fatalf("Calls() len = %d, want 1", len(calls))
	}
	got := calls[0].Options
	if got.MaxTokens != 8 {
		t.Errorf("recorded MaxTokens = %d, want 8", got.MaxTokens)
	}
	if got.Temperature == nil || *got.Temperature != 0.1 {
		t.Errorf("recorded Temperature = %v, want *0.1", got.Temperature)
	}
	if got.TopP == nil || *got.TopP != 0.9 {
		t.Errorf("recorded TopP = %v, want *0.9", got.TopP)
	}
	if !got.ExtendedThinking || got.ThinkingBudget != 2048 {
		t.Errorf("recorded ExtendedThinking = %v ThinkingBudget = %d, want true / 2048", got.ExtendedThinking, got.ThinkingBudget)
	}
}

// TestNewClient_InvalidConfig verifies the validation path runs before the
// factory is consulted.
func TestNewClient_InvalidConfig(t *testing.T) {
	_, err := llm.NewClient(context.Background(), &llm.Config{
		Provider: llm.ProviderOpenAI,
		// APIKey intentionally omitted — Validate() must reject before any
		// factory lookup.
	})
	if !errors.Is(err, llm.ErrInvalidConfig) {
		t.Fatalf("err = %v, want errors.Is(ErrInvalidConfig)", err)
	}
}
