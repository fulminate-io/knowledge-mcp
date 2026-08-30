// SPDX-License-Identifier: Apache-2.0

package config

import (
	"errors"
	"fmt"
	"os"
)

// providerEnvVar maps an API Provider to the environment variable that
// must hold its key. CLI providers are absent from this map.
var providerEnvVar = map[Provider]string{
	ProviderAnthropic: "ANTHROPIC_API_KEY",
	ProviderOpenAI:    "OPENAI_API_KEY",
	ProviderGemini:    "GEMINI_API_KEY",
}

// providerCLIBinary maps a CLI Provider to the canonical binary name
// the validator references in error messages and the auto-detector
// uses for first-run exec.LookPath. API providers are absent from
// this map.
var providerCLIBinary = map[Provider]string{
	ProviderClaudeCLI: "claude",
	ProviderCodexCLI:  "codex",
}

// validateCLIBin enforces the CLI-provider rules: the section's
// cli_bin field must be set, the path must exist, must not be a
// directory, and must have an executable bit. Errors name the
// consumer + the expected key so a startup-log line is enough to
// fix the config.
func validateCLIBin(consumer Consumer, section Section) error {
	canonicalBin, ok := providerCLIBinary[section.Provider]
	if !ok {
		return fmt.Errorf("config: consumer %q: internal: no binary mapping for CLI provider %q", consumer, section.Provider)
	}
	if section.CLIBin == "" {
		return fmt.Errorf("config: consumer %q uses CLI provider %q but cli_bin is not set; add `cli_bin = \"/absolute/path/to/%s\"` to the [%s] or [default] section", consumer, section.Provider, canonicalBin, consumer)
	}
	info, err := os.Stat(section.CLIBin)
	if err != nil {
		return fmt.Errorf("config: consumer %q cli_bin %q (%q): %w", consumer, section.CLIBin, section.Provider, err)
	}
	if info.IsDir() {
		return fmt.Errorf("config: consumer %q cli_bin %q (%q) is a directory; want an executable file", consumer, section.CLIBin, section.Provider)
	}
	if info.Mode()&0o111 == 0 {
		return fmt.Errorf("config: consumer %q cli_bin %q (%q) is not executable (mode %v); chmod +x or fix the path", consumer, section.CLIBin, section.Provider, info.Mode().Perm())
	}
	return nil
}

// ValidateEmbedCredential enforces the value-level rule the TOML parser
// cannot check, for one embed/rerank axis: an API provider must have a
// resolved key OR a base_url. The rule is "key OR base_url, never
// neither" — it is NOT "base_url implies keyless" and NOT "base_url is
// required". The original wording, from the LLM axis's Config.Validate, is
// carried here so the next reader can direction-check it without opening
// that file:
//
//	// A keyless local endpoint (BaseURL set, empty APIKey) is valid:
//	// the override targets an OpenAI-/anthropic-/gemini-compatible
//	// server that handles auth out-of-band.
//	if c.APIKey == "" && c.BaseURL == "" {
//	    return fmt.Errorf("%w: %s requires APIKey or BaseURL", ErrInvalidConfig, c.Provider)
//	}
//
// The deterministic fake is exempt: it makes no network call, so it needs
// neither a credential nor an endpoint.
//
// This is the SINGLE implementation of the rule for both new axes —
// embed.Config.Validate and rerank.Config.Validate call it rather than
// restating it, so the three sites cannot drift apart in wording.
//
// NOTE ON SCOPE: this is not wired into the startup Config.Validate
// consumer gate, and deliberately so. An operator with no embed
// credential at all is the DOCUMENTED BM25-only degrade (see
// VoyageAPIKey's doc comment in keys.go), not a startup failure; the
// degrade is decided before an embed.Config is ever built. This rule
// fires on a config that HAS asked for an arm, where neither a key nor an
// endpoint can serve it.
func ValidateEmbedCredential(provider EmbedProvider, apiKey, baseURL string) error {
	if !provider.IsValid() {
		return unknownEmbedProviderError(string(provider))
	}
	if !provider.IsAPI() {
		return nil
	}
	if apiKey == "" && baseURL == "" {
		return fmt.Errorf("%s requires an API key or a base_url", provider)
	}
	return nil
}

// Validate enforces the runtime requirements for each consumer in the
// list:
//
//  1. Provider and Model must resolve (per [default] inheritance).
//  2. For API providers (anthropic/openai/gemini), the corresponding
//     <PROVIDER>_API_KEY environment variable must be non-empty.
//  3. For CLI providers (claude-cli/codex-cli), the section's cli_bin
//     field MUST be set to an absolute path that exists and is
//     executable. There is NO PATH fallback — config self-contains the
//     binary path so launchd/systemd-managed servers (which run with a
//     sanitized PATH) work the same as interactive shells.
//
// Validate is parameterized so callers decide which consumers are
// active. All collected
// errors are joined and returned together so operators see every
// missing dep on a single startup pass instead of fixing one at a time.
//
// No per-consumer provider-type restriction is enforced here. Consumers
// that need a specific provider capability (e.g. tool-use, which CLI
// providers reject per internal/llm/claudecli/translate.go) guard at the
// call site rather than at startup. Earlier revisions of this validator
// rejected CLI providers for the transformer consumer, but the recipe
// transformer is rule-based and does not currently use Options.LLM —
// the rule was over-eager and made first-run UX hostile when the user
// had claude on PATH.
func (c *Config) Validate(consumers []Consumer) error {
	if c == nil {
		return errors.New("config.Validate: nil Config")
	}
	var errs []error
	for _, consumer := range consumers {
		if err := c.validateConsumer(consumer); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// validateConsumer runs the full per-consumer rule chain.
func (c *Config) validateConsumer(consumer Consumer) error {
	section, err := c.Resolve(consumer)
	if err != nil {
		return err
	}

	if section.Provider.IsAPI() {
		envVar, ok := providerEnvVar[section.Provider]
		if !ok {
			return fmt.Errorf("config: consumer %q: internal: no env-var mapping for API provider %q", consumer, section.Provider)
		}
		// Resolve the key the SAME way the runtime does (APIKeyForProvider:
		// [credentials].<provider>_api_key file-first, env var fallback) so a key
		// set only in [credentials] passes validation as it does at runtime —
		// previously this checked os.Getenv directly and rejected [credentials]-only
		// keys even though the summarizer constructed fine.
		//
		// A non-empty base_url is the keyless alternative: pointing an API
		// provider at a local/compatible endpoint that handles auth out-of-band
		// needs no key, so a resolved section with BaseURL set passes.
		if APIKeyForProvider(section.Provider) == "" && section.BaseURL == "" {
			return fmt.Errorf("config: consumer %q uses provider %q but no API key or base_url is set — set [credentials].%s_api_key, the %s env var, or [%s].base_url", consumer, section.Provider, section.Provider, envVar, consumer)
		}
		return nil
	}

	if section.Provider.IsCLI() {
		return validateCLIBin(consumer, section)
	}

	// Resolve already rejects unknown providers indirectly (translateSection
	// would have failed), but defend in depth in case Section is constructed
	// programmatically.
	return fmt.Errorf("config: consumer %q has unrecognized provider %q", consumer, section.Provider)
}
