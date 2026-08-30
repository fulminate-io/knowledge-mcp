// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// EmbedProvider names an EMBEDDING/RERANK backend by stable string
// identifier. It is deliberately a SEPARATE vocabulary from Provider (the
// LLM/summarizer enum in config.go): three of Provider's five values
// (anthropic, claude-cli, codex-cli) have no embedding surface at all —
// Anthropic does not publish an embeddings API — so reusing Provider here
// would let an operator name a provider that cannot embed.
//
// Like Provider, the type is kept local rather than importing an upstream
// package, which preserves this package as a true leaf with no upstream
// deps. Package embed re-exports it by alias.
type EmbedProvider string

// EmbedProvider constants. The string values are the accepted TOML
// vocabulary for [embedder].provider and [reranker].provider.
const (
	EmbedProviderVoyage           EmbedProvider = "voyage"
	EmbedProviderCohere           EmbedProvider = "cohere"
	EmbedProviderGemini           EmbedProvider = "gemini"
	EmbedProviderOpenAICompatible EmbedProvider = "openai-compatible"
	EmbedProviderFake             EmbedProvider = "fake"
)

// IsAPI reports whether p is one of the direct API providers
// (voyage / cohere / gemini / openai-compatible). API providers require a
// resolved key OR an explicit base_url — see the validator.
func (p EmbedProvider) IsAPI() bool {
	switch p {
	case EmbedProviderVoyage, EmbedProviderCohere, EmbedProviderGemini, EmbedProviderOpenAICompatible:
		return true
	}
	return false
}

// IsValid reports whether p is one of the recognized embed providers.
// The deterministic fake is valid but is not an API provider: it makes no
// network call and needs no credential.
func (p EmbedProvider) IsValid() bool {
	return p.IsAPI() || p == EmbedProviderFake
}

// String returns p as a plain string.
func (p EmbedProvider) String() string { return string(p) }

// EmbedSection is the [embedder] TOML table: which provider embeds the
// corpus and the search query, at what width and in what representation.
//
// It is deliberately NOT config.Section. Section carries CLIBin and
// Fallback and is threaded through Resolve/ResolveChain/inheritSection/
// perConsumerSection for the four LLM consumers; a dimension/dtype knob
// would be net-new surface at every one of those sites. The embed axis
// also has no [default] inheritance — [default] carries an LLM provider,
// which is not an EmbedProvider — so there is no Consumer constant for it
// either.
type EmbedSection struct {
	Provider  EmbedProvider
	Model     string
	BaseURL   string
	Dimension int
	Dtype     string
	// Key is an OPTIONAL per-section credential that TAKES PRECEDENCE over
	// the provider-resolved one. Empty (the common case) falls back to
	// APIKeyForEmbedProvider, unchanged.
	//
	// It exists because the provider name alone cannot identify the
	// credential for the openai-compatible arm: that arm serves a whole
	// population of third-party endpoints, and resolving its key from the
	// provider means every one of them is handed OPENAI_API_KEY. An
	// operator pointing [embedder] at a non-OpenAI compatible endpoint
	// would be sending their OpenAI key to a third party. A per-section key
	// is the only place that collision can be resolved, because the
	// endpoint is a per-section fact.
	//
	// SCOPED TO THE EMBED AND RERANK AXES ONLY, deliberately: the LLM
	// consumers' Section keeps resolving its credential from the provider
	// and that convention is NOT changed here.
	//
	// Like every other credential in this package the value is never
	// logged; assertions about it report LENGTH, never content.
	Key string
}

// RerankSection is the [reranker] TOML table. It omits Dimension and Dtype
// because a reranker returns scores, not vectors.
type RerankSection struct {
	Provider EmbedProvider
	Model    string
	BaseURL  string
	// Key is the OPTIONAL per-section credential, with the same precedence
	// and the same rationale as EmbedSection.Key — see that field. Empty
	// falls back to APIKeyForEmbedProvider.
	Key string
}

// ResolveEmbedKey returns the credential for an embed section: the
// section's own key when set, otherwise the provider-resolved one.
//
// PRECEDENCE, stated once and shared by both axes: an explicit
// per-section key is the operator's deliberate, endpoint-specific choice
// and always wins; the provider-resolved key (file-then-env, via
// APIKeyForEmbedProvider) is the fallback. Neither is an error here —
// key-or-base_url is enforced by the validator, not by resolution.
func (s EmbedSection) ResolveEmbedKey() string {
	return resolveSectionKey(s.Key, s.Provider)
}

// ResolveRerankKey returns the credential for a rerank section, with the
// same precedence EmbedSection.ResolveEmbedKey documents.
func (s RerankSection) ResolveRerankKey() string {
	return resolveSectionKey(s.Key, s.Provider)
}

// resolveSectionKey is the one implementation of the per-section-key
// precedence rule, shared so the two axes cannot drift.
func resolveSectionKey(sectionKey string, provider EmbedProvider) string {
	if sectionKey != "" {
		return sectionKey
	}
	return APIKeyForEmbedProvider(provider)
}

// AcceptedEmbedDimension and AcceptedEmbedDtype are the DEFAULTS an absent
// [embedder] section resolves to. They are no longer the only accepted values —
// see AcceptedEmbedDimensions / AcceptedEmbedDtypes for the accepted SETS.
//
// THE DEFAULT DELIBERATELY DOES NOT MOVE. Widening what a build accepts is not
// the same as changing what an operator already runs, and a graph's embed
// identity is sticky: no config change may trigger a corpus-scale re-embed
// spend. An existing deployment that never touches its config keeps the exact
// width and dtype it has today.
const (
	AcceptedEmbedDimension = 256
	AcceptedEmbedDtype     = "ubinary"
)

// AcceptedEmbedDimensions is the set of vector widths this build accepts.
//
// THE REFUSAL THESE REPLACE WAS TEMPORARY AND SAID SO. The knobs were parsed
// and refused because a wider vector reached the index build with no length
// check — measured silent truncation at 16 bytes and a panic in the distance
// kernel at 64 and 128. That hazard is what closed: the format now carries its
// own width, the builder derives width and dtype from the documents and refuses
// a batch that mixes them, and the image section declares the width it holds.
// The refusal is lifted because its stated cause is gone, not because the knobs
// became desirable.
//
// THE FOUR WIDTHS ARE NOT ARBITRARY: they are the widths every model in the
// evaluated matrix supports AND the widths the kernel's pin table is keyed by,
// so an accepted width is one that is both producible and performance-gated.
var AcceptedEmbedDimensions = []int{256, 512, 1024, 2048}

// AcceptedEmbedDtypes is the set of vector representations this build accepts.
var AcceptedEmbedDtypes = []string{"ubinary", "float32"}

// UnsupportedEmbedDimensionError is the ONE refusal message for an
// off-width dimension, shared by the TOML parser and by
// embed.Config.Validate so an operator cannot tell which layer refused
// them. Both enforcement points are needed: the parser only sees configs
// that came from a file, and a Config built in Go never passes through it.
//
// WIDENING THE SET DID NOT SOFTEN THE RULE. A value outside the set still
// errors, and the message still names the offending value AND lists the
// accepted vocabulary — the unknownEmbedProviderError shape — so the operator
// does not have to guess the spelling.
func UnsupportedEmbedDimensionError(dim int) error {
	return fmt.Errorf("dimension %d is not supported (accepted: %s)", dim, joinInts(AcceptedEmbedDimensions))
}

// UnsupportedEmbedDtypeError is the ONE refusal message for an off-dtype
// value, shared by the TOML parser and by embed.Config.Validate. See
// UnsupportedEmbedDimensionError for why both layers enforce it.
func UnsupportedEmbedDtypeError(dtype string) error {
	return fmt.Errorf("dtype %q is not supported (accepted: %s)", dtype, strings.Join(AcceptedEmbedDtypes, ", "))
}

// joinInts renders an accepted-value set for an error message.
func joinInts(vals []int) string {
	parts := make([]string, len(vals))
	for i, v := range vals {
		parts[i] = strconv.Itoa(v)
	}
	return strings.Join(parts, ", ")
}

// ResolveEmbedder returns the effective [embedder] section.
//
// An ABSENT section resolves to the voyage provider at the accepted width
// and dtype with Model EMPTY. Empty is deliberate and is a contract
// difference from Section.Resolve, which ERRORS on an empty model: the
// embed ARM owns its own default model, so config carries no provider
// model name. Read the omission as "the arm's default", not as a bug.
func (c *Config) ResolveEmbedder() (EmbedSection, error) {
	out := EmbedSection{Provider: EmbedProviderVoyage, Dimension: AcceptedEmbedDimension, Dtype: AcceptedEmbedDtype}
	if c != nil && c.Embedder != nil {
		per := *c.Embedder
		out.Provider = coalesceEmbedProvider(per.Provider, out.Provider)
		out.Model = per.Model
		out.BaseURL = per.BaseURL
		out.Key = per.Key
		if per.Dimension != 0 {
			out.Dimension = per.Dimension
		}
		out.Dtype = coalesce(per.Dtype, out.Dtype)
	}
	if err := validateEmbedShape(out.Provider, out.Dimension, out.Dtype); err != nil {
		return EmbedSection{}, fmt.Errorf("config: section [embedder]: %w", err)
	}
	return out, nil
}

// ResolveReranker returns the effective [reranker] section. An absent
// section resolves to the voyage provider with Model EMPTY — the arm's
// default, exactly as ResolveEmbedder documents.
func (c *Config) ResolveReranker() (RerankSection, error) {
	out := RerankSection{Provider: EmbedProviderVoyage}
	if c != nil && c.Reranker != nil {
		per := *c.Reranker
		out.Provider = coalesceEmbedProvider(per.Provider, out.Provider)
		out.Model = per.Model
		out.BaseURL = per.BaseURL
		out.Key = per.Key
	}
	if !out.Provider.IsValid() {
		return RerankSection{}, fmt.Errorf("config: section [reranker]: %w", unknownEmbedProviderError(string(out.Provider)))
	}
	return out, nil
}

// ValidateEmbedShape is the provider/dimension/dtype gate, exported for the
// SECOND enforcement layer.
//
// BOTH LAYERS MUST ASK THE SAME QUESTION, and for a while they did not. The
// TOML translator and embed.Config.Validate are deliberately separate
// enforcement points — the parser only sees configs that came from a file, and
// a Config built in Go never passes through it — but they were separate
// IMPLEMENTATIONS too, and when the accepted values widened from a single pair
// to two sets only the parser's copy moved. The result was a refusal that
// contradicted itself in the operator's face: `dimension 1024 is not supported
// (accepted: 256, 512, 1024, 2048)`, because the message rendered the set while
// the comparison still read the default. Exporting the gate is what makes the
// two layers structurally incapable of drifting again.
func ValidateEmbedShape(p EmbedProvider, dim int, dtype string) error {
	return validateEmbedShape(p, dim, dtype)
}

// validateEmbedShape is the provider/dimension/dtype gate shared by
// ResolveEmbedder and the TOML translator.
func validateEmbedShape(p EmbedProvider, dim int, dtype string) error {
	if !p.IsValid() {
		return unknownEmbedProviderError(string(p))
	}
	if !slices.Contains(AcceptedEmbedDimensions, dim) {
		return UnsupportedEmbedDimensionError(dim)
	}
	if !slices.Contains(AcceptedEmbedDtypes, dtype) {
		return UnsupportedEmbedDtypeError(dtype)
	}
	return nil
}

// unknownEmbedProviderError reproduces translateSection's unknown-provider
// error shape (parser.go): name the offending value AND list the accepted
// vocabulary, so the operator does not have to guess the spelling.
func unknownEmbedProviderError(got string) error {
	return fmt.Errorf("unknown provider %q (want one of: voyage, cohere, gemini, openai-compatible, fake)", got)
}

// coalesceEmbedProvider is coalesce for the EmbedProvider type.
func coalesceEmbedProvider(override, fallback EmbedProvider) EmbedProvider {
	if override != "" {
		return override
	}
	return fallback
}

// APIKeyForEmbedProvider returns the API key for an embed/rerank provider,
// or the empty string for the fake (which authenticates nothing).
// Resolution: [credentials].<provider>_api_key if set, else the matching
// env var, else "". Closed-set switch — adding a new provider requires a
// deliberate edit here, which is the right spot for a hard-coded env-var
// convention to live.
//
// This is the function that dissolves the shared-VOYAGE_API_KEY trap: every
// axis resolves its key FROM ITS OWN RESOLVED PROVIDER, so an operator
// running Voyage embeddings and Cohere rerank supplies two keys and neither
// axis reads the other's.
func APIKeyForEmbedProvider(p EmbedProvider) string {
	cr := credentials()
	switch p {
	case EmbedProviderVoyage:
		var c string
		if cr != nil {
			c = cr.VoyageAPIKey
		}
		return credOrEnv(c, "VOYAGE_API_KEY")
	case EmbedProviderCohere:
		var c string
		if cr != nil {
			c = cr.CohereAPIKey
		}
		return credOrEnv(c, "COHERE_API_KEY")
	case EmbedProviderGemini:
		var c string
		if cr != nil {
			c = cr.GeminiAPIKey
		}
		return credOrEnv(c, "GEMINI_API_KEY")
	case EmbedProviderOpenAICompatible:
		var c string
		if cr != nil {
			c = cr.OpenAIAPIKey
		}
		return credOrEnv(c, "OPENAI_API_KEY")
	case EmbedProviderFake:
		return ""
	default:
		return ""
	}
}
