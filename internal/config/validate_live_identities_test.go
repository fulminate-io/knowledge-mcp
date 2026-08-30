// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConfigValidationAgainstLiveIdentities pins the truthful-inability surface:
// a recorded identity this config cannot construct an embedder for is a LOUD
// error naming the dependent graphs, never a silent BM25 degrade.
//
// FOUR LEGS, TWO FAILURES AND TWO NON-FAILURES, and the non-failures are what
// keep the gate from being satisfiable by a validator that simply errors
// whenever a profile is absent. That validator would pass both failure legs and
// fail both legality legs.
func TestConfigValidationAgainstLiveIdentities(t *testing.T) {
	// error_names_the_dependent_graphs. The profile a graph was embedded under is
	// gone AND no provider-level credential resolves, so nothing can build the
	// embedder — and the error has to say WHICH graphs, or the operator cannot
	// tell what to restore or what to migrate.
	t.Run("error_names_the_dependent_graphs", func(t *testing.T) {
		clearEmbedCredentials(t)
		// A NEAR-MISS PROFILE CARRYING A SECTION KEY. It is the same provider and
		// width as the live identity but a different model, so it does NOT match
		// and the identity still fails to resolve — and it puts real credential
		// material inside the very profile set the validator walks. Without it the
		// never-leak assertion below would be run against a fixture holding
		// nothing to leak, which is decoration however precisely it is worded.
		cfg := parseCfg(t, `
[embedder.profile.nearmiss]
provider = "voyage"
model = "voyage-code-2"
dimension = 1024
dtype = "ubinary"
key = "`+secretSentinel+`"
`)

		err := cfg.ValidateAgainstLiveIdentities([]LiveGraphIdentity{
			{GraphType: "code", Name: "alpha", Identity: voyageIdentity()},
			{GraphType: "code", Name: "beta", Identity: voyageIdentity()},
			{GraphType: "knowledge", Name: "default", Identity: voyageIdentity()},
		})
		require.Error(t, err, "a recorded identity with no constructible embedder must be an error")

		msg := err.Error()
		for _, want := range []string{"code/alpha", "code/beta", "knowledge/default"} {
			assert.Contains(t, msg, want, "the error must NAME every dependent graph")
		}
		assert.Contains(t, msg, "voyage/voyage-code-3 at 1024 ubinary",
			"and the identity they depend on, so the operator knows what to restore")
		assert.Contains(t, msg, "migrate_embed_identity",
			"and the deliberate alternative to restoring it")

		// ONE ERROR FOR THREE GRAPHS. The failures group by IDENTITY because the
		// repair is per identity: restoring one profile fixes all three.
		assert.Equal(t, 1, strings.Count(msg, "is embedded by"),
			"three graphs on one identity are ONE failure, not three")

		// NO CREDENTIAL MATERIAL, ever — and the near-miss profile above is what
		// makes this able to fail: an error that named the closest profile, or
		// rendered the profile it walked, would put that key in the message.
		assert.NotContains(t, msg, secretSentinel,
			"messages name graphs, identities and providers — never credential values")
	})

	// deleted_credential_is_an_error, and it is DISTINCT from the profile half: a
	// profile may still be defined while the credential it resolves through is
	// gone.
	t.Run("deleted_credential_is_an_error", func(t *testing.T) {
		clearEmbedCredentials(t)
		cfg := parseCfg(t, `
[embedder.profile.code3]
provider = "voyage"
model = "voyage-code-3"
dimension = 1024
dtype = "ubinary"
`)
		live := []LiveGraphIdentity{{GraphType: "code", Name: "alpha", Identity: voyageIdentity()}}

		err := cfg.ValidateAgainstLiveIdentities(live)
		require.Error(t, err, "a defined profile with no resolvable credential cannot build an embedder")
		assert.Contains(t, err.Error(), "code/alpha")
		assert.Contains(t, err.Error(), "no credential or base_url resolves")

		// KNOWN-POSITIVE, same run and same fixture: give the profile its
		// credential back and the SAME identity validates. Without this leg a
		// validator that errored on every recorded identity would pass above.
		withCfg := parseCfg(t, `
[embedder.profile.code3]
provider = "voyage"
model = "voyage-code-3"
dimension = 1024
dtype = "ubinary"
key = "`+secretSentinel+`"
`)
		require.NoError(t, withCfg.ValidateAgainstLiveIdentities(live),
			"the identity is constructible once its credential resolves")
	})

	// unreferenced_profile_is_legal: an operator staging a future migration has
	// defined a profile nothing has been embedded under yet. That is not a
	// failure, and a gate that called it one would be unusable.
	t.Run("unreferenced_profile_is_legal", func(t *testing.T) {
		clearEmbedCredentials(t)
		cfg := parseCfg(t, `
[embedder.profile.future]
provider = "cohere"
model = "embed-v4.0"
dimension = 1024
dtype = "ubinary"
`)
		// The one graph that HAS embedded is on an identity that resolves; the
		// staged profile is referenced by nothing.
		live := []LiveGraphIdentity{{
			GraphType: "code", Name: "alpha",
			Identity: RecordedIdentity{Provider: EmbedProviderFake, Model: "canned", Dimension: 256, Dtype: "ubinary"},
		}}
		require.NoError(t, cfg.ValidateAgainstLiveIdentities(live),
			"a profile referenced by no graph is an operator staging a migration, not a fault")
	})

	// graph_without_identity_is_legal: the graph has not embedded, so there is no
	// identity to be unable to construct. Its first embed will record one.
	t.Run("graph_without_identity_is_legal", func(t *testing.T) {
		clearEmbedCredentials(t)
		cfg := parseCfg(t, "")
		live := []LiveGraphIdentity{
			{GraphType: "code", Name: "alpha"},
			{GraphType: "knowledge", Name: "default"},
		}
		require.NoError(t, cfg.ValidateAgainstLiveIdentities(live),
			"a graph with no recorded identity has not embedded and cannot be broken")

		// KNOWN-POSITIVE, same run: the SAME config and the SAME graphs DO fail
		// once one of them records an identity. Without it, a validator that
		// returned nil unconditionally would pass this leg and both legality legs.
		live[0].Identity = voyageIdentity()
		require.Error(t, cfg.ValidateAgainstLiveIdentities(live),
			"the check is live: the same fixture fails the moment an identity is recorded")
	})
}

// TestConfigValidationAgainstLiveIdentities_EndpointHalf pins the failure that
// would otherwise pass SILENTLY, which is the worst kind here.
//
// "openai-compatible" NAMES A PROTOCOL, NOT A SERVICE. Its arm falls back to
// OpenAI's own endpoint when no base_url is given, so a graph embedded against
// some other compatible server — whose profile has since been deleted — would be
// queried against a DIFFERENT vector space with every call reporting success.
// A resolved key is therefore NOT sufficient for this provider.
func TestConfigValidationAgainstLiveIdentities_EndpointHalf(t *testing.T) {
	clearEmbedCredentials(t)
	t.Setenv("OPENAI_API_KEY", secretSentinel)

	compat := RecordedIdentity{
		Provider: EmbedProviderOpenAICompatible, Model: "bge-m3", Dimension: 1024, Dtype: "float32",
	}
	live := []LiveGraphIdentity{{GraphType: "code", Name: "alpha", Identity: compat}}

	err := parseCfg(t, "").ValidateAgainstLiveIdentities(live)
	require.Error(t, err,
		"a resolved key is not enough for a provider whose endpoint only a profile can name")
	assert.Contains(t, err.Error(), "code/alpha")
	assert.Contains(t, err.Error(), "which endpoint produced those vectors")
	assert.NotContains(t, err.Error(), secretSentinel)

	// KNOWN-POSITIVE, same run: restore a profile that MATCHES the identity and
	// names the endpoint, and the same graph validates.
	ok := parseCfg(t, `
[embedder.profile.local]
provider = "openai-compatible"
model = "bge-m3"
dimension = 1024
dtype = "float32"
base_url = "http://localhost:8080/v1"
`)
	require.NoError(t, ok.ValidateAgainstLiveIdentities(live),
		"a matching profile names the endpoint, so the identity is constructible")

	// AND A PROFILE THAT DOES NOT MATCH DOES NOT COUNT. Same provider, different
	// model: it describes a different endpoint's vectors, so it cannot stand in.
	mismatched := parseCfg(t, `
[embedder.profile.other]
provider = "openai-compatible"
model = "some-other-model"
dimension = 1024
dtype = "float32"
base_url = "http://localhost:9999/v1"
`)
	require.Error(t, mismatched.ValidateAgainstLiveIdentities(live),
		"the match is on the FULL identity tuple; a same-provider profile for a different model "+
			"names a different endpoint's vector space")
}

// secretSentinel is a value that would be unmistakable in an error message. Its
// only purpose is to make the never-name-credential-material assertions able to
// fail: an assertion that nothing leaked, run against a fixture with nothing to
// leak, is decoration.
const secretSentinel = "SENTINEL-SECRET-VALUE-abc123"

func voyageIdentity() RecordedIdentity {
	return RecordedIdentity{Provider: EmbedProviderVoyage, Model: "voyage-code-3", Dimension: 1024, Dtype: "ubinary"}
}

func parseCfg(t *testing.T, body string) *Config {
	t.Helper()
	cfg, err := Parse([]byte(body))
	require.NoError(t, err)
	return cfg
}

// clearEmbedCredentials empties every environment credential the embed providers
// resolve through, so a key on the developer's own machine cannot make a failure
// leg pass. It also points the credentials-file lookup at an empty directory for
// the same reason.
func clearEmbedCredentials(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"VOYAGE_API_KEY", "COHERE_API_KEY", "GEMINI_API_KEY", "OPENAI_API_KEY",
	} {
		t.Setenv(k, "")
	}
	t.Setenv("HOME", t.TempDir())
}
