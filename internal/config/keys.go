// SPDX-License-Identifier: Apache-2.0

package config

import "os"

// credentials returns the loaded config's [credentials] section, or nil
// when no config has been loaded yet OR the section was absent. Callers
// fall back to the env var on nil. Using Loaded() (not Active()) keeps
// the accessors panic-free in tests and early-bootstrap code paths that
// run before LoadOrAutoDetect.
func credentials() *Credentials {
	if !Loaded() {
		return nil
	}
	return Active().Credentials
}

// credOrEnv returns configVal if it is non-empty, otherwise os.Getenv(env).
// This is the precedence rule for every [credentials] key: the file wins
// (it is the deliberate, persistent choice), the env var is the fallback,
// and an empty result simply disables the feature — never an error.
func credOrEnv(configVal, env string) string {
	if configVal != "" {
		return configVal
	}
	return os.Getenv(env)
}

// VoyageAPIKey resolves the Voyage AI key: [credentials].voyage_api_key if
// set, else VOYAGE_API_KEY, else "". Empty means BM25-only search (no
// binary-vector embeddings, no cross-encoder rerank) — a documented,
// non-error degrade.
//
// NOTE: this reads the loaded config singleton, so it only returns the
// [credentials] value AFTER LoadOrAutoDetect has run. Callers that need
// the key before config load (e.g. flag parsing) must defer resolution
// until after the load.
func VoyageAPIKey() string {
	var c string
	if cr := credentials(); cr != nil {
		c = cr.VoyageAPIKey
	}
	return credOrEnv(c, "VOYAGE_API_KEY")
}

// LinearAPIKey resolves the Linear personal API key: [credentials].
// linear_api_key if set, else LINEAR_API_KEY, else "". Empty means the
// Linear backend is disabled.
func LinearAPIKey() string {
	var c string
	if cr := credentials(); cr != nil {
		c = cr.LinearAPIKey
	}
	return credOrEnv(c, "LINEAR_API_KEY")
}

// APIKeyForProvider returns the API key for an API provider, or the empty
// string for CLI providers (which authenticate via the user's local CLI
// login). Resolution: [credentials].<provider>_api_key if set, else the
// matching env var, else "". Closed-set switch — adding a new provider
// requires a deliberate edit here, which is the right spot for a
// hard-coded env-var convention to live.
//
// Knowledge is OSS Apache 2.0 BYOK — operators bring their own
// Anthropic/OpenAI/Gemini key (via [credentials] or env var); the
// substrate never resells credentials.
//
// This function lives in the config package so its callers — today the
// agent-flow synthesizer in
// cmd/knowledge/internal/transcriptanalytics/synthesis.go — resolve the key
// straight off the loaded config with no further dependency.
func APIKeyForProvider(p Provider) string {
	cr := credentials()
	switch p {
	case ProviderAnthropic:
		var c string
		if cr != nil {
			c = cr.AnthropicAPIKey
		}
		return credOrEnv(c, "ANTHROPIC_API_KEY")
	case ProviderOpenAI:
		var c string
		if cr != nil {
			c = cr.OpenAIAPIKey
		}
		return credOrEnv(c, "OPENAI_API_KEY")
	case ProviderGemini:
		var c string
		if cr != nil {
			c = cr.GeminiAPIKey
		}
		return credOrEnv(c, "GEMINI_API_KEY")
	case ProviderClaudeCLI, ProviderCodexCLI:
		return ""
	default:
		return ""
	}
}
