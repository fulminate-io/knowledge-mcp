// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"
)

// TestAPIKeyForEmbedProvider_ClosedSwitch walks EVERY declared
// EmbedProvider value plus an unregistered one and asserts three things at
// once: the vocabulary is complete (each constant is IsValid), the API/fake
// split is right, and the closed key switch routes each provider to its own
// credential rather than to a shared one. The shared-VOYAGE_API_KEY trap
// this ticket dissolves is exactly a routing bug, so routing is what the
// table asserts.
func TestAPIKeyForEmbedProvider_ClosedSwitch(t *testing.T) {
	t.Setenv("VOYAGE_API_KEY", "env-voyage")
	t.Setenv("COHERE_API_KEY", "env-cohere")
	t.Setenv("GEMINI_API_KEY", "env-gemini")
	t.Setenv("OPENAI_API_KEY", "env-openai")
	t.Cleanup(SetForTest(nil))

	cases := []struct {
		provider EmbedProvider
		wantKey  string
		wantAPI  bool
		wantOK   bool
	}{
		{EmbedProviderVoyage, "env-voyage", true, true},
		{EmbedProviderCohere, "env-cohere", true, true},
		{EmbedProviderGemini, "env-gemini", true, true},
		{EmbedProviderOpenAICompatible, "env-openai", true, true},
		{EmbedProviderFake, "", false, true},
		{EmbedProvider("anthropic"), "", false, false},
	}
	if len(cases) != 6 {
		t.Fatalf("table lost a case: %d rows", len(cases))
	}
	for _, tc := range cases {
		if got := APIKeyForEmbedProvider(tc.provider); got != tc.wantKey {
			t.Errorf("APIKeyForEmbedProvider(%q) = %q; want %q", tc.provider, got, tc.wantKey)
		}
		if got := tc.provider.IsAPI(); got != tc.wantAPI {
			t.Errorf("%q.IsAPI() = %v; want %v", tc.provider, got, tc.wantAPI)
		}
		if got := tc.provider.IsValid(); got != tc.wantOK {
			t.Errorf("%q.IsValid() = %v; want %v", tc.provider, got, tc.wantOK)
		}
		if got := tc.provider.String(); got != string(tc.provider) {
			t.Errorf("%q.String() = %q", tc.provider, got)
		}
	}

	// Config wins over env, per credOrEnv — asserted on the NEW key so the
	// added credential field is wired through credentials(), not just
	// declared. A known-positive against the env value above: if the field
	// were unread this would return "env-cohere".
	t.Cleanup(SetForTest(&Config{Credentials: &Credentials{CohereAPIKey: "file-cohere"}}))
	if got := APIKeyForEmbedProvider(EmbedProviderCohere); got != "file-cohere" {
		t.Errorf("with [credentials].cohere_api_key set, APIKeyForEmbedProvider(cohere) = %q; want %q", got, "file-cohere")
	}
	if got := APIKeyForEmbedProvider(EmbedProviderVoyage); got != "env-voyage" {
		t.Errorf("voyage with only a cohere credential set = %q; want the env value %q", got, "env-voyage")
	}
}

// TestEmbedSection_RefusesUnknownProviderDtypeDimension drives the
// admission gate from TOML: an unknown provider, an off dtype and an off
// dimension are each REFUSED with an error naming the offending value.
//
// The known-positive control is the accepted body at the top: the same
// parser call on (256, ubinary, voyage) succeeds, so a red here means the
// gate refused something, not that the whole path is broken.
func TestEmbedSection_RefusesUnknownProviderDtypeDimension(t *testing.T) {
	const head = "[default]\nprovider = \"anthropic\"\nmodel = \"claude-haiku-5\"\n"

	accepted := head + "[embedder]\nprovider = \"voyage\"\ndimension = 256\ndtype = \"ubinary\"\n"
	cfg, err := Parse([]byte(accepted))
	if err != nil {
		t.Fatalf("control body must parse: %v", err)
	}
	if cfg.Embedder == nil || cfg.Embedder.Dimension != 256 || cfg.Embedder.Dtype != "ubinary" {
		t.Fatalf("control body parsed to %+v; want the accepted shape", cfg.Embedder)
	}

	refusals := []struct {
		name string
		body string
		want string
	}{
		// CONVERTED, NOT DELETED. Dimension 1024 and dtype float32 used to be
		// refusals here and are now ACCEPTED — the index that was named as what
		// would lift the restriction has shipped. The cases stay as refusals with
		// values that are genuinely outside the widened set, because deleting
		// them would remove the only evidence the gate is still armed.
		{"unknown embed provider", head + "[embedder]\nprovider = \"anthropic\"\n", `unknown provider "anthropic"`},
		{"off dtype", head + "[embedder]\ndtype = \"int8\"\n", `dtype "int8" is not supported`},
		{"off dimension", head + "[embedder]\ndimension = 3072\n", "dimension 3072 is not supported"},
		{"unknown rerank provider", head + "[reranker]\nprovider = \"anthropic\"\n", `unknown provider "anthropic"`},
	}
	for _, tc := range refusals {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.body))
			if err == nil {
				t.Fatalf("Parse accepted %s; want a refusal", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not name the offending value (want substring %q)", err, tc.want)
			}
			// EVERY refusal lists the accepted vocabulary — the width/dtype ones
			// no longer point at a future release, because it arrived.
			if !strings.Contains(err.Error(), "accepted:") && !strings.Contains(err.Error(), "want one of:") {
				t.Errorf("refusal %q does not list the accepted vocabulary", err)
			}
			if strings.Contains(err.Error(), "float-native vector index") {
				t.Errorf("refusal %q still promises a release that has shipped", err)
			}
		})
	}
}

// TestResolveEmbedderReranker_LegacyDefaults: with NO [embedder] and NO
// [reranker] section, both axes resolve to the voyage provider at the
// accepted width and dtype, with Model EMPTY so the arm supplies its own
// default. That is what keeps an operator with no new sections on
// byte-identical behavior.
func TestResolveEmbedderReranker_LegacyDefaults(t *testing.T) {
	cfg, err := Parse([]byte("[default]\nprovider = \"anthropic\"\nmodel = \"claude-haiku-5\"\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Embedder != nil || cfg.Reranker != nil {
		t.Fatalf("absent sections must stay nil; got %+v / %+v", cfg.Embedder, cfg.Reranker)
	}

	emb, err := cfg.ResolveEmbedder()
	if err != nil {
		t.Fatalf("ResolveEmbedder: %v", err)
	}
	if emb.Provider != EmbedProviderVoyage {
		t.Errorf("Provider = %q; want voyage", emb.Provider)
	}
	if emb.Dimension != 256 {
		t.Errorf("Dimension = %d; want 256", emb.Dimension)
	}
	if emb.Dtype != "ubinary" {
		t.Errorf("Dtype = %q; want ubinary", emb.Dtype)
	}
	if emb.Model != "" {
		t.Errorf("Model = %q; want empty (the arm's default)", emb.Model)
	}
	if emb.BaseURL != "" {
		t.Errorf("BaseURL = %q; want empty (the arm's default)", emb.BaseURL)
	}

	rr, err := cfg.ResolveReranker()
	if err != nil {
		t.Fatalf("ResolveReranker: %v", err)
	}
	if rr.Provider != EmbedProviderVoyage {
		t.Errorf("rerank Provider = %q; want voyage", rr.Provider)
	}
	if rr.Model != "" || rr.BaseURL != "" {
		t.Errorf("rerank Model/BaseURL = %q/%q; want empty (the arm's defaults)", rr.Model, rr.BaseURL)
	}

	// A PRESENT section overrides — the known-positive that proves the
	// assertions above are reading resolution output rather than a
	// hardcoded struct.
	cfg2, err := Parse([]byte("[default]\nprovider = \"anthropic\"\nmodel = \"claude-haiku-5\"\n[embedder]\nprovider = \"fake\"\nmodel = \"m\"\n[reranker]\nprovider = \"cohere\"\nmodel = \"r\"\n"))
	if err != nil {
		t.Fatalf("Parse override: %v", err)
	}
	emb2, err := cfg2.ResolveEmbedder()
	if err != nil {
		t.Fatalf("ResolveEmbedder override: %v", err)
	}
	if emb2.Provider != EmbedProviderFake || emb2.Model != "m" {
		t.Errorf("override resolved to %+v; want fake/m", emb2)
	}
	rr2, err := cfg2.ResolveReranker()
	if err != nil {
		t.Fatalf("ResolveReranker override: %v", err)
	}
	if rr2.Provider != EmbedProviderCohere || rr2.Model != "r" {
		t.Errorf("override resolved to %+v; want cohere/r", rr2)
	}
}

// TestValidateEmbedSection_KeyOrBaseURL drives the value-level credential
// rule: NEITHER a key nor a base_url is refused for an API provider,
// base_url ALONE is accepted (a keyless local endpoint that handles auth
// out-of-band), a key alone is accepted, and the deterministic fake is
// exempt from both.
func TestValidateEmbedSection_KeyOrBaseURL(t *testing.T) {
	cases := []struct {
		name     string
		provider EmbedProvider
		apiKey   string
		baseURL  string
		wantErr  bool
	}{
		{"api provider with neither", EmbedProviderVoyage, "", "", true},
		{"api provider with base_url alone", EmbedProviderVoyage, "", "http://127.0.0.1:8000", false},
		{"api provider with key alone", EmbedProviderVoyage, "k", "", false},
		{"api provider with both", EmbedProviderCohere, "k", "http://127.0.0.1:8000", false},
		{"openai-compatible with neither", EmbedProviderOpenAICompatible, "", "", true},
		{"fake is exempt", EmbedProviderFake, "", "", false},
		{"unknown provider", EmbedProvider("anthropic"), "k", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateEmbedCredential(tc.provider, tc.apiKey, tc.baseURL)
			if tc.wantErr && err == nil {
				t.Fatalf("ValidateEmbedCredential(%q, %q, %q) = nil; want a refusal", tc.provider, tc.apiKey, tc.baseURL)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateEmbedCredential(%q, %q, %q) = %v; want nil", tc.provider, tc.apiKey, tc.baseURL, err)
			}
			if tc.wantErr && !strings.Contains(err.Error(), string(tc.provider)) {
				t.Errorf("refusal %q does not name the provider %q", err, tc.provider)
			}
		})
	}
}

// TestEmbedSectionKey_TakesPrecedence pins the per-section key on the
// EMBED axis: a set [embedder].key WINS over the provider-resolved
// credential, and an absent one falls back to it unchanged.
//
// The collision this closes is concrete: the openai-compatible arm serves
// a whole population of third-party endpoints, so resolving its key from
// the provider name alone hands OPENAI_API_KEY to whatever base_url the
// operator configured. The env value below is the known-positive that
// makes the precedence assertion meaningful — without it, "the section key
// won" and "no key was resolved at all" would look identical.
func TestEmbedSectionKey_TakesPrecedence(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "env-openai")
	t.Setenv("VOYAGE_API_KEY", "env-voyage")
	t.Cleanup(SetForTest(nil))

	// ABSENT key -> the provider-resolved credential, unchanged.
	fallback := EmbedSection{Provider: EmbedProviderOpenAICompatible}
	if got := fallback.ResolveEmbedKey(); got != "env-openai" {
		t.Errorf("with no section key, ResolveEmbedKey() = %q; want the provider-resolved %q", got, "env-openai")
	}

	// PRESENT key -> wins, and the provider key is NOT consulted.
	explicit := EmbedSection{Provider: EmbedProviderOpenAICompatible, Key: "together-key"}
	if got := explicit.ResolveEmbedKey(); got != "together-key" {
		t.Errorf("with a section key, ResolveEmbedKey() = %q; want the section's own %q", got, "together-key")
	}
	if got := explicit.ResolveEmbedKey(); got == "env-openai" {
		t.Error("the section key must not fall through to the provider credential")
	}

	// The precedence holds per-provider, not just for openai-compatible.
	voyage := EmbedSection{Provider: EmbedProviderVoyage, Key: "section-voyage"}
	if got := voyage.ResolveEmbedKey(); got != "section-voyage" {
		t.Errorf("ResolveEmbedKey() = %q; want %q", got, "section-voyage")
	}

	// Through TOML, end to end: the key field parses and resolves.
	body := "[default]\nprovider = \"anthropic\"\nmodel = \"m\"\n[embedder]\nprovider = \"openai-compatible\"\nbase_url = \"https://api.together.xyz\"\nkey = \"toml-key\"\n"
	cfg, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	sec, err := cfg.ResolveEmbedder()
	if err != nil {
		t.Fatalf("ResolveEmbedder: %v", err)
	}
	if sec.Key != "toml-key" {
		t.Errorf("parsed [embedder].key = %q; want %q", sec.Key, "toml-key")
	}
	if got := sec.ResolveEmbedKey(); got != "toml-key" {
		t.Errorf("resolved key = %q; want the section's %q (len %d)", got, "toml-key", len(got))
	}
}

// TestRerankSectionKey_TakesPrecedence pins the same per-section key
// precedence on the RERANK axis, so an operator can rerank against one
// endpoint's credential while embedding against another's.
func TestRerankSectionKey_TakesPrecedence(t *testing.T) {
	t.Setenv("COHERE_API_KEY", "env-cohere")
	t.Cleanup(SetForTest(nil))

	fallback := RerankSection{Provider: EmbedProviderCohere}
	if got := fallback.ResolveRerankKey(); got != "env-cohere" {
		t.Errorf("with no section key, ResolveRerankKey() = %q; want the provider-resolved %q", got, "env-cohere")
	}

	explicit := RerankSection{Provider: EmbedProviderCohere, Key: "section-cohere"}
	if got := explicit.ResolveRerankKey(); got != "section-cohere" {
		t.Errorf("with a section key, ResolveRerankKey() = %q; want the section's own %q", got, "section-cohere")
	}

	body := "[default]\nprovider = \"anthropic\"\nmodel = \"m\"\n[reranker]\nprovider = \"cohere\"\nkey = \"toml-rerank-key\"\n"
	cfg, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	sec, err := cfg.ResolveReranker()
	if err != nil {
		t.Fatalf("ResolveReranker: %v", err)
	}
	if sec.Key != "toml-rerank-key" {
		t.Errorf("parsed [reranker].key = %q; want %q", sec.Key, "toml-rerank-key")
	}
	if got := sec.ResolveRerankKey(); got != "toml-rerank-key" {
		t.Errorf("resolved key = %q; want the section's own (len %d)", got, len(got))
	}

	// THE AXES DO NOT SHARE THE SECTION KEY. An [embedder].key must not
	// leak into the rerank axis — that is the same shape as the shared-key
	// trap this ticket dissolves, one level down.
	both, err := Parse([]byte("[default]\nprovider = \"anthropic\"\nmodel = \"m\"\n[embedder]\nkey = \"embed-only\"\n[reranker]\nprovider = \"cohere\"\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rr, err := both.ResolveReranker()
	if err != nil {
		t.Fatalf("ResolveReranker: %v", err)
	}
	if rr.Key != "" {
		t.Errorf("[reranker].Key = %q; an [embedder].key must not leak across axes", rr.Key)
	}
	if got := rr.ResolveRerankKey(); got != "env-cohere" {
		t.Errorf("rerank resolved %q; want its own provider credential %q", got, "env-cohere")
	}
}

// TestParse_CohereCredential pins the third wiring site: the TOML tag and
// the Parse copy block. Declaring the struct field without the copy leaves
// the key silently unreadable from a config file.
func TestParse_CohereCredential(t *testing.T) {
	body := `
[default]
provider = "anthropic"
model = "claude-haiku-5"

[credentials]
cohere_api_key = "co-xxx"
`
	cfg, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Credentials == nil {
		t.Fatal("Credentials is nil; expected populated")
	}
	if cfg.Credentials.CohereAPIKey != "co-xxx" {
		t.Errorf("CohereAPIKey = %q; want %q", cfg.Credentials.CohereAPIKey, "co-xxx")
	}
}
