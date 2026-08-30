// SPDX-License-Identifier: Apache-2.0

package embed

import (
	"errors"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// Provider is the embed/rerank provider vocabulary, re-exported by ALIAS
// from the config package so package embed's callers can name providers
// without importing config directly. config is a leaf with no upstream
// deps, which is why the vocabulary is declared there and aliased here —
// the same mechanism llm/types.go uses for the LLM provider enum.
type Provider = config.EmbedProvider

// Provider constants, aliased from the config vocabulary.
const (
	ProviderVoyage           = config.EmbedProviderVoyage
	ProviderCohere           = config.EmbedProviderCohere
	ProviderGemini           = config.EmbedProviderGemini
	ProviderOpenAICompatible = config.EmbedProviderOpenAICompatible
	ProviderFake             = config.EmbedProviderFake
)

// InputRole is the SEMANTIC role of the text being embedded: corpus text
// being indexed, or search-time query text. It is fixed for the lifetime
// of one embedder — the index pipeline and the search-time query embedder
// are separate construction sites — which is why it is a construction
// parameter rather than a per-call argument, and why BinaryEmbedder keeps
// its three methods instead of gaining EmbedQuery variants.
//
// Each arm maps this single role onto ITS OWN provider vocabulary; the
// spellings differ per provider and no arm reads another's.
type InputRole string

// InputRole constants.
const (
	InputRoleDocument InputRole = "document" // corpus text being indexed
	InputRoleQuery    InputRole = "query"    // search-time query text
)

// Config is the input to NewEmbedder. One flat struct covers every
// registered arm; which fields matter differs by Provider.
//
// Model and BaseURL EMPTY mean "the arm's own default" — config carries no
// provider model name, so an operator with no [embedder] section gets the
// arm's default model and endpoint.
type Config struct {
	// Provider picks which registered factory to dispatch to.
	Provider Provider
	// Model is the provider-specific model name. Empty = the arm's default.
	Model string
	// APIKey credentials an API provider; empty for the fake, and empty is
	// also valid for a keyless BaseURL endpoint.
	APIKey string
	// BaseURL overrides the arm's endpoint (optional). Empty = the arm's
	// default.
	BaseURL string
	// Dimension is the vector width in BITS.
	Dimension int
	// Dtype is the output representation.
	Dtype string
	// InputRole is the semantic role of the text this embedder will be
	// asked to embed. Empty defaults to InputRoleDocument, reproducing the
	// single hardcoded role this package had before the split.
	InputRole InputRole
}

// Sentinel errors, mirroring llm's.
var (
	ErrInvalidConfig         = errors.New("embed: invalid config")
	ErrProviderNotRegistered = errors.New("embed: provider not registered")
)

// errNilConfig is the shared nil-config guard every arm's factory returns.
// NewEmbedder never passes nil (Validate rejects it first), so this fires
// only for a factory invoked directly.
func errNilConfig() error { return fmt.Errorf("%w: nil config", ErrInvalidConfig) }

// Validate checks that c has the fields required for c.Provider, and that
// the width and representation are ones this build can actually emit.
//
// Rules, in order: a nil config errors; an invalid Provider errors naming
// the value; an API provider with neither an APIKey nor a BaseURL errors;
// a Dimension or Dtype outside the accepted SETS errors. An empty
// InputRole DEFAULTS to InputRoleDocument — defaulting and refusing are
// the same kind of act at the same seam, so both live here.
//
// The credential rule is "key OR base_url, never neither". The original
// wording, from the LLM axis's Config.Validate, so the next reader can
// direction-check it without opening that file:
//
//	// A keyless local endpoint (BaseURL set, empty APIKey) is valid:
//	// the override targets an OpenAI-/anthropic-/gemini-compatible
//	// server that handles auth out-of-band.
//	if c.APIKey == "" && c.BaseURL == "" {
//	    return fmt.Errorf("%w: %s requires APIKey or BaseURL", ErrInvalidConfig, c.Provider)
//	}
//
// ProviderFake requires neither and is exempt. The rule itself is
// config.ValidateEmbedCredential — one implementation for both axes and
// both layers rather than three copies that can drift.
//
// THE WIDTH REFUSAL IS ENFORCED HERE AS WELL AS IN THE TOML PARSER, and
// the second enforcement point is not redundant: the parser only sees
// configs that came from a file, and a Config built in Go never passes
// through it. IT ROUTES THROUGH config.ValidateEmbedShape rather than
// re-deriving the rule, because two enforcement points with two
// implementations is how this layer came to refuse 1024 while its own
// message listed 1024 as accepted. The proven instance is embed.Config{Provider: ProviderFake}
// — it satisfies the credential rule (the fake is exempt), carries
// Dimension 0, and a width of 0/8 makes the fake return an EMPTY vector
// for every text. The pipeline's segment builder skips zero-length
// vectors SILENTLY, so an end-to-end run would index nothing with every
// gate still green. The package that EMITS the bytes must not accept a
// config it cannot honor. The message text is shared with the parser so
// an operator cannot tell which layer refused them.
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
	if err := config.ValidateEmbedShape(c.Provider, c.Dimension, c.Dtype); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	if c.InputRole == "" {
		c.InputRole = InputRoleDocument
	}
	return nil
}
