package llm

import "fmt"

// Config is the input to NewClient. One flat struct covers every supported
// provider; required fields differ by Provider:
//
//   - ProviderOpenAI / ProviderAnthropic / ProviderGemini require APIKey
//     OR BaseURL — a keyless local endpoint sets BaseURL with an empty
//     APIKey (the compatible server handles auth out-of-band).
//   - ProviderClaudeCLI / ProviderCodexCLI require nothing — the CLI
//     binary is resolved via PATH lookup (CLIBin overrides if set).
//
// BaseURL is optional for API providers (lets callers point at an LLM
// gateway, regional override, or local proxy). Model selects the default
// model when a Generate call doesn't supply WithModel; per-call WithModel
// always wins.
//
// Knowledge intentionally exposes a small surface here. Per-provider knobs
// (extended thinking, organization id, vertex project id) live with the
// provider implementation, not the substrate config. If a knob proves
// common across providers it can be lifted up later.
type Config struct {
	// Provider picks which registered factory to dispatch to.
	Provider Provider
	// APIKey credentials the API providers; empty for CLI providers.
	APIKey string
	// BaseURL overrides the API endpoint (optional). Ignored for CLI providers.
	BaseURL string
	// Model is the default model used when a Generate call doesn't set WithModel.
	Model Model
	// CLIBin overrides PATH-resolution for CLI providers. Ignored for API providers.
	CLIBin string
}

// Validate checks that c has the fields required for c.Provider.
//
// Returns nil on success; on failure returns an error wrapping
// ErrInvalidConfig with a human-readable detail (e.g. "openai requires
// APIKey"). NewClient calls Validate before dispatching to a factory.
func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("%w: nil config", ErrInvalidConfig)
	}
	if !c.Provider.IsValid() {
		return fmt.Errorf("%w: unknown provider %q", ErrInvalidConfig, string(c.Provider))
	}
	switch c.Provider {
	case ProviderOpenAI, ProviderAnthropic, ProviderGemini:
		// A keyless local endpoint (BaseURL set, empty APIKey) is valid:
		// the override targets an OpenAI-/anthropic-/gemini-compatible
		// server that handles auth out-of-band.
		if c.APIKey == "" && c.BaseURL == "" {
			return fmt.Errorf("%w: %s requires APIKey or BaseURL", ErrInvalidConfig, c.Provider)
		}
	case ProviderClaudeCLI, ProviderCodexCLI:
		// No required fields; CLIBin is optional and PATH-resolved when empty.
	}
	return nil
}
