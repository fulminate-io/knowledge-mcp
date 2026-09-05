// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// parseShape is the on-disk TOML structure. Pointer fields on the
// per-consumer sections preserve absent-vs-empty: nil means the section
// was omitted (full inheritance from [default]); non-nil with empty
// fields means the section is present but inherits per-field.
//
// SchemaVersion is at the top level (outside any [section]). Absent
// (zero) is treated as 1 — pre-versioning configs are forward-
// compatible. translateSection-equivalent at Parse time enforces
// the upper bound; configs that declare a version higher than this
// binary supports are rejected with an upgrade message.
type parseShape struct {
	SchemaVersion int `toml:"schema_version"`
	// HealthProbeInterval is the top-level health_probe_interval key (a Go
	// duration string like "10m"). Kept as a raw string here and parsed via
	// time.ParseDuration in Parse so a malformed value surfaces a clear,
	// key-named error rather than a generic TOML type error. Empty = absent.
	HealthProbeInterval string `toml:"health_probe_interval"`
	// FulminateAccountID is the top-level fulminate_account_id key: the
	// Fulminate account (tenancy) this machine's cloud calls are routed to.
	// Copied verbatim into Config — no validation, no normalization; an id
	// the gateway will reject is a state the client must be able to hold and
	// report on. Empty = absent = no selection.
	FulminateAccountID string `toml:"fulminate_account_id"`
	// AutoUpdate is the top-level auto_update key. A POINTER, not a bool,
	// because absent must stay distinguishable from an explicit false: absent
	// means automatic updates are ON, which is the default, and only an
	// explicit `auto_update = false` turns them off.
	AutoUpdate  *bool               `toml:"auto_update"`
	Default     parseSection        `toml:"default"`
	Summarizer  *parseSection       `toml:"summarizer"`
	Supervisor  *parseSection       `toml:"supervisor"`
	Topics      *parseSection       `toml:"topics"`
	Credentials *parseCredentials   `toml:"credentials"`
	Embedder    *parseEmbedSection  `toml:"embedder"`
	Reranker    *parseRerankSection `toml:"reranker"`
}

// parseEmbedSection mirrors the [embedder] TOML table. dimension and dtype
// are the quantization knobs; both are parsed and then admitted only at a
// value in this build's accepted SETS, any other value being an error
// naming the value and the vocabulary (see translateEmbedSection).
// The optional key field on both sections is the per-section credential;
// it takes precedence over the provider-resolved one. See
// EmbedSection.Key for why it exists and why it is scoped to these two
// axes only.
//
// THE TWO NESTED MAPS ARE THE NAMED-PROFILE SURFACE. [embedder.profile.<name>]
// tables land in Profile and [embedder.family.<family>] tables in Family. They
// nest under [embedder] rather than sitting at top level so the whole embed
// axis stays one block, and because the scalars above ARE a profile — the one
// named "default" — rather than a separate kind of thing.
//
// A CONFIG MAY DEFINE PROFILES WITHOUT SETTING ANY SCALAR. TOML creates the
// parent table implicitly for [embedder.profile.x], so raw.Embedder is
// non-nil with every scalar at its zero value; translateEmbedSection then
// resolves exactly the defaults an ABSENT [embedder] resolves to, which is
// the intended reading.
type parseEmbedSection struct {
	Provider  string `toml:"provider"`
	Model     string `toml:"model"`
	BaseURL   string `toml:"base_url"`
	Dimension int    `toml:"dimension"`
	Dtype     string `toml:"dtype"`
	Key       string `toml:"key"`

	Profile map[string]parseEmbedProfile `toml:"profile"`
	Family  map[string]parseEmbedFamily  `toml:"family"`
}

// parseEmbedProfile mirrors one [embedder.profile.<name>] table. Its fields
// are exactly parseEmbedSection's scalars — a profile is a complete embedder
// config, not a patch over the default one, so nothing here is inherited and
// each profile is read on its own.
type parseEmbedProfile struct {
	Provider  string `toml:"provider"`
	Model     string `toml:"model"`
	BaseURL   string `toml:"base_url"`
	Dimension int    `toml:"dimension"`
	Dtype     string `toml:"dtype"`
	Key       string `toml:"key"`
}

// parseEmbedFamily mirrors one [embedder.family.<family>] table: a reference
// to a profile BY NAME, used as the creation-time default for graphs of that
// family. It carries a name and not an inline embedder config deliberately —
// an inline copy would be a second definition of an embedder that could drift
// from the profile it duplicates.
type parseEmbedFamily struct {
	Profile string `toml:"profile"`
}

// parseRerankSection mirrors the [reranker] TOML table. No dimension or
// dtype: a reranker returns scores, not vectors.
type parseRerankSection struct {
	Provider string `toml:"provider"`
	Model    string `toml:"model"`
	BaseURL  string `toml:"base_url"`
	Key      string `toml:"key"`
}

// parseCredentials mirrors the optional [credentials] TOML table. Pointer
// on parseShape preserves absent-vs-present; every field inside is an
// optional string that falls back to its env var when empty.
type parseCredentials struct {
	VoyageAPIKey    string `toml:"voyage_api_key"`
	LinearAPIKey    string `toml:"linear_api_key"`
	AnthropicAPIKey string `toml:"anthropic_api_key"`
	OpenAIAPIKey    string `toml:"openai_api_key"`
	GeminiAPIKey    string `toml:"gemini_api_key"`
	CohereAPIKey    string `toml:"cohere_api_key"`
}

// parseSection mirrors a single TOML table. Four keys:
//   - provider: stable LLM provider identifier
//   - model:    provider-specific model name
//   - cli_bin:  absolute path to the CLI binary (claude-cli/codex-cli only;
//     required for CLI providers, ignored for API providers)
//   - base_url: optional override of the API provider endpoint (API providers
//     only; empty falls back to the provider's default URL)
type parseSection struct {
	Provider string `toml:"provider"`
	Model    string `toml:"model"`
	CLIBin   string `toml:"cli_bin"`
	BaseURL  string `toml:"base_url"`
	// Fallback nests the ordered [[summarizer.fallback]] tables under a
	// consumer's [section] table. go-toml/v2 maps an array-of-tables to a
	// slice, so two [[summarizer.fallback]] blocks become a len-2 slice in
	// document order. Absent (the common case) leaves this nil.
	Fallback []parseSection `toml:"fallback"`
}

// Load reads path and parses the TOML body. Returns the wrapped
// os.ReadFile / Parse error verbatim so callers can distinguish
// not-found, permission, and malformed-file conditions.
//
// On parse success, Load installs the parsed *Config into the package
// singleton via setActive so subsequent calls to Active() observe it.
// Validation is a separate step (parameterized by consumer list); the
// orchestrator LoadOrAutoDetect calls Load then Validate before allowing
// the server to proceed. If Validate fails the caller is expected to
// hard-exit, so a populated singleton does no harm.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config.Load: read %s: %w", path, err)
	}
	cfg, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("config.Load: %s: %w", path, err)
	}
	setActive(cfg)
	return cfg, nil
}

// Parse unmarshals data as TOML and translates it into a Config.
//
// Provider strings are lowercased and checked against the known set;
// any non-empty unrecognized provider returns an error. Empty provider
// strings are permitted at parse time — they become the responsibility
// of Resolve / Validate, which decide whether [default] supplies the
// value or the consumer is genuinely missing one.
func Parse(data []byte) (*Config, error) {
	var raw parseShape
	if err := toml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("config.Parse: %w", err)
	}

	// Schema version: absent = 1 (pre-versioning compatibility).
	// Higher than CurrentSchemaVersion = config was written by a
	// newer build of knowledge — fail loudly with an upgrade hint.
	// Lower than current is accepted; future migrations would land
	// here as version-conditional translation steps.
	version := raw.SchemaVersion
	if version == 0 {
		version = 1
	}
	if version > CurrentSchemaVersion {
		return nil, fmt.Errorf("config.Parse: schema_version %d is newer than this binary supports (max %d) — upgrade knowledge or downgrade the config", version, CurrentSchemaVersion)
	}

	cfg := &Config{SchemaVersion: version}

	// fulminate_account_id: copied verbatim, exactly as the credential strings
	// below are. Validation belongs to the CLI pre-checks and the gateway, not
	// to parsing — the client must be able to hold (and report on) a selection
	// the gateway will reject.
	cfg.FulminateAccountID = raw.FulminateAccountID

	// auto_update: copied as a POINTER so nil (absent) stays distinct from an
	// explicit false. nil means enabled; only `auto_update = false` disables.
	cfg.AutoUpdate = raw.AutoUpdate

	// health_probe_interval: a Go duration string. Empty leaves the field zero
	// (the prober defaults it downstream); a malformed value is a hard Parse
	// error that names the key so the operator knows what to fix.
	if raw.HealthProbeInterval != "" {
		d, err := time.ParseDuration(raw.HealthProbeInterval)
		if err != nil {
			return nil, fmt.Errorf("config.Parse: health_probe_interval %q is not a valid duration: %w", raw.HealthProbeInterval, err)
		}
		cfg.HealthProbeInterval = d
	}

	def, err := translateSection("default", raw.Default)
	if err != nil {
		return nil, err
	}
	cfg.Default = def

	if raw.Summarizer != nil {
		s, err := translateSection(string(ConsumerSummarizer), *raw.Summarizer)
		if err != nil {
			return nil, err
		}
		cfg.Summarizer = &s
	}
	if raw.Supervisor != nil {
		s, err := translateSection(string(ConsumerSupervisor), *raw.Supervisor)
		if err != nil {
			return nil, err
		}
		cfg.Supervisor = &s
	}
	if raw.Topics != nil {
		s, err := translateSection(string(ConsumerTopics), *raw.Topics)
		if err != nil {
			return nil, err
		}
		cfg.Topics = &s
	}
	if err := applyEmbedAxes(cfg, raw); err != nil {
		return nil, err
	}
	if raw.Credentials != nil {
		cfg.Credentials = &Credentials{
			VoyageAPIKey:    raw.Credentials.VoyageAPIKey,
			LinearAPIKey:    raw.Credentials.LinearAPIKey,
			AnthropicAPIKey: raw.Credentials.AnthropicAPIKey,
			OpenAIAPIKey:    raw.Credentials.OpenAIAPIKey,
			GeminiAPIKey:    raw.Credentials.GeminiAPIKey,
			CohereAPIKey:    raw.Credentials.CohereAPIKey,
		}
	}
	return cfg, nil
}

// translateSection lowercases the provider string and checks it against
// the known Provider constants. An empty provider is allowed and
// returned as Provider("") so per-field inheritance can fill it later.
// CLIBin and BaseURL are copied verbatim — CLIBin's value-level validation
// (must be a real executable file when the provider is CLI) happens in
// Validate; BaseURL has no required-field gate (optional for all providers).
//
// Nested raw.Fallback entries are translated through this SAME primitive (one
// recursive call per entry) so each fallback entry gets the identical
// provider-normalize + IsValid gate — validation is not duplicated. A fallback
// entry with an unknown provider returns the same unknown-provider error as a
// top-level section. Each entry is named "<name>.fallback[i]" so the error
// pinpoints which entry is bad.
// applyEmbedAxes translates the two optional embed/rerank tables onto cfg.
// An absent table leaves its pointer nil, which the resolvers read as "use
// the defaults". Split out of Parse purely to keep that function under the
// repo's statement cap; it is one step of the same translation sequence.
func applyEmbedAxes(cfg *Config, raw parseShape) error {
	if raw.Embedder != nil {
		s, err := translateEmbedSection(*raw.Embedder)
		if err != nil {
			return err
		}
		cfg.Embedder = &s
		if err := applyEmbedProfiles(cfg, *raw.Embedder); err != nil {
			return err
		}
	}
	if raw.Reranker != nil {
		s, err := translateRerankSection(*raw.Reranker)
		if err != nil {
			return err
		}
		cfg.Reranker = &s
	}
	return nil
}

// translateEmbedSection lowercases the provider string and checks it
// against the EmbedProvider vocabulary — the same normalize-then-validate
// primitive translateSection applies to the LLM axis, instanced for the
// embed one. An empty provider defaults to voyage.
//
// THE ADMISSION GATE. An absent/zero dimension defaults to the accepted
// width and an absent/empty dtype to the accepted dtype; a value outside the
// accepted SETS is AN ERROR naming the value and the accepted vocabulary. It
// is a refusal, not a coercion and not a warn-and-continue. The sets widened
// once the format carried its own width and the builder refused a mixed
// batch — the hazard the single-value refusal existed for — but widening what
// is accepted did not soften the rule for anything outside it.
func translateEmbedSection(raw parseEmbedSection) (EmbedSection, error) {
	return translateEmbedShape("embedder", embedShapeFields{
		Provider:  raw.Provider,
		Model:     raw.Model,
		BaseURL:   raw.BaseURL,
		Dimension: raw.Dimension,
		Dtype:     raw.Dtype,
		Key:       raw.Key,
	})
}

// embedShapeFields is the raw scalar set an embedder table carries, shared by
// [embedder] and every [embedder.profile.<name>] so the two cannot drift in
// what they default or what they refuse.
type embedShapeFields struct {
	Provider  string
	Model     string
	BaseURL   string
	Dimension int
	Dtype     string
	Key       string
}

// translateEmbedShape is the one normalize-then-validate implementation for
// an embedder table. section names the table for the error prefix, so a
// refusal points at the profile that carries the bad value rather than at
// "[embedder]" generically.
func translateEmbedShape(section string, raw embedShapeFields) (EmbedSection, error) {
	out := EmbedSection{
		Provider:  EmbedProviderVoyage,
		Model:     raw.Model,
		BaseURL:   raw.BaseURL,
		Key:       raw.Key,
		Dimension: AcceptedEmbedDimension,
		Dtype:     AcceptedEmbedDtype,
	}
	if raw.Provider != "" {
		out.Provider = EmbedProvider(strings.ToLower(raw.Provider))
	}
	if raw.Dimension != 0 {
		out.Dimension = raw.Dimension
	}
	if raw.Dtype != "" {
		out.Dtype = strings.ToLower(raw.Dtype)
	}
	if err := validateEmbedShape(out.Provider, out.Dimension, out.Dtype); err != nil {
		return EmbedSection{}, fmt.Errorf("config: section [%s]: %w", section, err)
	}
	return out, nil
}

// (applyEmbedProfiles, which consumes the two nested maps above, lives in
// embed_profiles.go beside the profile surface it populates.)

// translateRerankSection is translateEmbedSection for the [reranker] table,
// minus the quantization knobs the rerank axis does not have.
func translateRerankSection(raw parseRerankSection) (RerankSection, error) {
	out := RerankSection{Provider: EmbedProviderVoyage, Model: raw.Model, BaseURL: raw.BaseURL, Key: raw.Key}
	if raw.Provider != "" {
		out.Provider = EmbedProvider(strings.ToLower(raw.Provider))
	}
	if !out.Provider.IsValid() {
		return RerankSection{}, fmt.Errorf("config: section [reranker]: %w", unknownEmbedProviderError(string(out.Provider)))
	}
	return out, nil
}

func translateSection(name string, raw parseSection) (Section, error) {
	out := Section{Model: raw.Model, CLIBin: raw.CLIBin, BaseURL: raw.BaseURL}
	if raw.Provider != "" {
		p := Provider(strings.ToLower(raw.Provider))
		if !p.IsValid() {
			return Section{}, fmt.Errorf("config: section [%s]: unknown provider %q (want one of: anthropic, openai, gemini, claude-cli, codex-cli)", name, raw.Provider)
		}
		out.Provider = p
	}
	for i, fb := range raw.Fallback {
		sec, err := translateSection(fmt.Sprintf("%s.fallback[%d]", name, i), fb)
		if err != nil {
			return Section{}, err
		}
		out.Fallback = append(out.Fallback, sec)
	}
	return out, nil
}
