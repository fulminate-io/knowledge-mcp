// SPDX-License-Identifier: Apache-2.0

package rerank

import (
	"errors"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// Provider is the embed/rerank provider vocabulary, re-exported by ALIAS
// from the config package. The two axes SHARE one vocabulary and one
// credential resolver; what they do not share is a registry map, because
// the value types differ — not every provider that embeds also reranks.
type Provider = config.EmbedProvider

// Provider constants, aliased from the config vocabulary.
const (
	ProviderVoyage           = config.EmbedProviderVoyage
	ProviderCohere           = config.EmbedProviderCohere
	ProviderGemini           = config.EmbedProviderGemini
	ProviderOpenAICompatible = config.EmbedProviderOpenAICompatible
	ProviderFake             = config.EmbedProviderFake
)

// Config is the input to NewReranker.
//
// InputDocs and TopK are CALLER-supplied search-tuning parameters, not
// config-file values: both production sites pass the operating pool size
// and top-K they are searching with, and the arm defensively re-caps
// InputDocs at the provider's absolute API limit. They are deliberately
// not [reranker] keys — they describe the search, not the provider.
//
// Model and BaseURL EMPTY mean "the arm's own default".
type Config struct {
	Provider  Provider
	Model     string
	APIKey    string
	BaseURL   string
	InputDocs int
	TopK      int
}

// Sentinel errors, mirroring the embed axis's.
var (
	ErrInvalidConfig         = errors.New("rerank: invalid config")
	ErrProviderNotRegistered = errors.New("rerank: provider not registered")
)

// Validate checks that c has the fields required for c.Provider.
//
// The credential rule is the SAME "key OR base_url, never neither" rule
// the embed axis enforces, and it is the same code: it delegates to
// config.ValidateEmbedCredential rather than restating it, so the two axes
// cannot drift apart in wording. See cmd/knowledge/internal/embed/config.go
// for the rule's original wording and why the fake is exempt.
//
// There is no width or dtype gate here: a reranker returns scores, not
// vectors, so the quantization knobs the embed axis refuses do not exist
// on this one.
func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("%w: nil config", ErrInvalidConfig)
	}
	if !c.Provider.IsValid() {
		return fmt.Errorf("%w: unknown provider %q", ErrInvalidConfig, string(c.Provider))
	}
	if err := config.ValidateEmbedCredential(c.Provider, c.APIKey, c.BaseURL); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	return nil
}
