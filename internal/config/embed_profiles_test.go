// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// profilesTOML defines three named profiles plus a family default, and sets no
// [embedder] scalars at all — so it also exercises the shape where the parent
// table exists only because TOML created it for the nested ones.
const profilesTOML = `
[default]
provider = "anthropic"
model    = "claude-haiku-5"

[embedder.profile.code4]
provider  = "voyage"
model     = "voyage-code-4"
dimension = 1024
dtype     = "float32"

[embedder.profile.text]
provider = "cohere"
model    = "embed-v4.0"

[embedder.profile.local]
provider = "openai-compatible"
base_url = "http://127.0.0.1:8080"
key      = "local-key"

[embedder.family.code]
profile = "code4"
`

// TestEmbedderProfiles covers the four duties of the named-profile surface,
// one subtest each, so a change that lands three of them fails and says which.
func TestEmbedderProfiles(t *testing.T) {
	t.Run("named profiles parse", func(t *testing.T) {
		cfg, err := Parse([]byte(profilesTOML))
		require.NoError(t, err)

		assert.Equal(t, []string{"code4", "default", "local", "text"}, cfg.EmbedProfileNames(),
			"every declared profile is defined, and so is the implicit default")

		// EACH PROFILE IS READ ON ITS OWN, not as a patch over the default:
		// text declares no dimension/dtype and gets the DEFAULTS, not code4's
		// 1024/float32. Asserting a second profile's values is what makes that
		// a real claim rather than a single-row lookup.
		code4, err := cfg.EmbedProfileByName("code4")
		require.NoError(t, err)
		assert.Equal(t, "code4", code4.Name)
		assert.Equal(t, EmbedProviderVoyage, code4.Provider)
		assert.Equal(t, "voyage-code-4", code4.Model)
		assert.Equal(t, 1024, code4.Dimension)
		assert.Equal(t, "float32", code4.Dtype)

		text, err := cfg.EmbedProfileByName("text")
		require.NoError(t, err)
		assert.Equal(t, EmbedProviderCohere, text.Provider)
		assert.Equal(t, AcceptedEmbedDimension, text.Dimension,
			"a profile that declares no dimension takes the default, NOT a sibling profile's")
		assert.Equal(t, AcceptedEmbedDtype, text.Dtype)

		// The per-section key precedence carries onto profiles unchanged,
		// which is the whole reason a profile is an EmbedSection.
		local, err := cfg.EmbedProfileByName("local")
		require.NoError(t, err)
		assert.Equal(t, EmbedProviderOpenAICompatible, local.Provider)
		assert.Equal(t, "http://127.0.0.1:8080", local.BaseURL)
		assert.Len(t, local.ResolveEmbedKey(), len("local-key"),
			"the profile's own key wins over the provider-resolved one (length, never content)")

		// A bad value inside a profile is refused, and the error names the
		// PROFILE rather than "[embedder]" — without this, the shape check
		// could be running only on the default section.
		_, err = Parse([]byte("[embedder.profile.wide]\ndimension = 384\n"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "[embedder.profile.wide]")
		assert.Contains(t, err.Error(), "384")
	})

	t.Run("single embedder section is the default profile", func(t *testing.T) {
		// BACK-COMPAT IS THE CLAIM: a config written before profiles existed
		// defines exactly one profile, named "default", carrying its values.
		cfg, err := Parse([]byte("[embedder]\nprovider = \"fake\"\nmodel = \"m\"\ndimension = 512\n"))
		require.NoError(t, err)
		assert.Equal(t, []string{"default"}, cfg.EmbedProfileNames())

		def, err := cfg.EmbedProfileByName("default")
		require.NoError(t, err)
		assert.Equal(t, "default", def.Name)
		assert.Equal(t, EmbedProviderFake, def.Provider)
		assert.Equal(t, "m", def.Model)
		assert.Equal(t, 512, def.Dimension)

		// An empty name is ABSENT, not wrong, and resolves to the same profile.
		byEmpty, err := cfg.EmbedProfileByName("")
		require.NoError(t, err)
		assert.Equal(t, def, byEmpty)

		// An ABSENT [embedder] still defines the default profile, at the
		// documented defaults — there is no config with no profile.
		bare, err := Parse([]byte("[default]\nprovider = \"anthropic\"\nmodel = \"m\"\n"))
		require.NoError(t, err)
		assert.Equal(t, []string{"default"}, bare.EmbedProfileNames())
		bareDef, err := bare.EmbedProfileByName("default")
		require.NoError(t, err)
		assert.Equal(t, EmbedProviderVoyage, bareDef.Provider)
		assert.Equal(t, AcceptedEmbedDimension, bareDef.Dimension)
		assert.Equal(t, AcceptedEmbedDtype, bareDef.Dtype)

		// "default" may not be REDEFINED as a named profile: one name, two
		// definitions, and lookup order deciding which a family reaches.
		_, err = Parse([]byte("[embedder.profile.default]\nprovider = \"cohere\"\n"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reserved")
	})

	t.Run("family default resolves by profile name", func(t *testing.T) {
		cfg, err := Parse([]byte(profilesTOML))
		require.NoError(t, err)

		// The family WITH a default reaches its named profile...
		got, err := cfg.ResolveEmbedProfileForFamily("code")
		require.NoError(t, err)
		assert.Equal(t, "code4", got.Name)
		assert.Equal(t, 1024, got.Dimension)

		// ...and a family WITHOUT one falls to the default profile. Both legs
		// are needed: a resolver that returned the default for everything
		// would satisfy the second alone, and one that errored on every
		// unlisted family would satisfy the first alone.
		fallback, err := cfg.ResolveEmbedProfileForFamily("knowledge")
		require.NoError(t, err)
		assert.Equal(t, DefaultEmbedProfileName, fallback.Name)
		assert.Equal(t, AcceptedEmbedDimension, fallback.Dimension)

		// Case and surrounding space are normalized, as everywhere else in
		// this package's vocabulary handling.
		upper, err := cfg.ResolveEmbedProfileForFamily("  CODE ")
		require.NoError(t, err)
		assert.Equal(t, "code4", upper.Name)

		// A family outside the accepted set is an ERROR naming the value and
		// the vocabulary — not a silent fall through to the default, which
		// would create graphs under an identity nobody chose.
		_, err = cfg.ResolveEmbedProfileForFamily("linkage")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "linkage")
		assert.Contains(t, err.Error(), "transformers", "the refusal lists the accepted families")

		_, err = Parse([]byte("[embedder.family.nope]\nprofile = \"default\"\n"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "nope")

		// A family table exists only to name a profile, so an empty reference
		// is a refusal rather than an implicit default.
		_, err = Parse([]byte("[embedder.family.code]\nprofile = \"\"\n"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "profile is required")
	})

	t.Run("unknown profile error names defined profiles", func(t *testing.T) {
		cfg, err := Parse([]byte(profilesTOML))
		require.NoError(t, err)

		_, err = cfg.EmbedProfileByName("code5")
		require.Error(t, err)
		assert.Contains(t, err.Error(), `"code5"`, "the refusal names the value it rejected")
		for _, defined := range []string{"code4", "default", "local", "text"} {
			assert.Contains(t, err.Error(), defined,
				"the refusal lists every DEFINED profile, because the set is the operator's own "+
					"and a typo is otherwise indistinguishable from a profile they forgot to write")
		}

		// A dangling family reference fails at LOAD, where the operator is
		// looking — not at the first embed of the next new graph of that
		// family, which is the moment an identity gets recorded.
		_, err = Parse([]byte("[embedder.profile.code4]\nprovider = \"voyage\"\n" +
			"[embedder.family.code]\nprofile = \"typo\"\n"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), `"typo"`)
		assert.Contains(t, err.Error(), "code4", "and it lists what IS defined")
	})
}

// TestEmbedProfiles_StarterExamplesParse uncomments the profile and family
// examples from the RENDERED starter config and parses them.
//
// IT READS THE REAL ARTIFACT, not a copy of it, and that is the whole point. A
// fixture that restates what I believe the template says tests my belief; this
// takes the bytes an operator's ~/.knowledge/config actually receives, strips
// the leading "# ", and hands the result to the same Parse a daemon runs. A
// documented example that would be REFUSED by the parser is a defect that ships
// straight into the first thing every new user reads, and nothing else in the
// suite would see it: the examples are comments, so the round-trip test parses
// the file happily with them inert.
func TestEmbedProfiles_StarterExamplesParse(t *testing.T) {
	rendered, err := Render(DetectedProvider{Provider: ProviderAnthropic, Model: "claude-haiku-4-5-20251001"})
	require.NoError(t, err)

	var body strings.Builder
	inExample := false
	for line := range strings.SplitSeq(rendered, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "# [embedder.profile.") || strings.HasPrefix(trimmed, "# [embedder.family."):
			inExample = true
		case trimmed == "#" || trimmed == "":
			inExample = false
			continue
		case !strings.HasPrefix(trimmed, "#"):
			inExample = false
			continue
		}
		if inExample {
			body.WriteString(strings.TrimPrefix(strings.TrimPrefix(trimmed, "#"), " "))
			body.WriteString("\n")
		}
	}

	// KNOWN-POSITIVE: the extraction found the examples at all. Without it a
	// selector that matched nothing would "parse" an empty document and pass,
	// which is precisely the vacuous shape this test exists to avoid.
	extracted := body.String()
	require.Contains(t, extracted, "[embedder.profile.code4]", "the starter must document a profile example")
	require.Contains(t, extracted, "[embedder.family.code]", "and a family example")
	require.Contains(t, extracted, "[embedder.profile.local]")

	cfg, err := Parse([]byte(extracted))
	require.NoError(t, err, "the starter's own examples must be a config this build accepts:\n%s", extracted)

	// And they mean what the surrounding prose says they mean.
	prof, err := cfg.ResolveEmbedProfileForFamily("code")
	require.NoError(t, err)
	assert.Equal(t, "code4", prof.Name)
	assert.Equal(t, 1024, prof.Dimension)
	assert.Equal(t, "float32", prof.Dtype)
}

// TestEmbedProfiles_ConfigLoadWritesNoIdentity is the prohibition the whole
// design rests on, asserted rather than asserted-in-prose: parsing a config
// that defines profiles and family defaults produces CONFIG STATE ONLY.
//
// WHAT THIS CAN AND CANNOT SHOW, stated plainly so it is not read as more than
// it is. There is no identity writer in this package to observe, so this
// cannot watch a write not happen. What it pins is the surface: the parse
// result carries the profile map and the family map and nothing that names or
// holds a graph identity, and the package exports no function that takes one.
// The step that introduces the identity record owns the behavioral half.
func TestEmbedProfiles_ConfigLoadWritesNoIdentity(t *testing.T) {
	cfg, err := Parse([]byte(profilesTOML))
	require.NoError(t, err)

	// Known-positive: the parse really did populate the profile surface, so
	// the absence asserted below is not the absence of a parse.
	require.Len(t, cfg.EmbedProfiles, 3)
	require.Equal(t, map[string]string{"code": "code4"}, cfg.EmbedFamilyProfiles)

	// The family map holds a profile NAME, never a resolved identity: the
	// reference is late-bound at first embed, so an edit to the profile it
	// names cannot chase graphs already embedded under it.
	assert.Equal(t, "code4", cfg.EmbedFamilyProfiles["code"])
}
