// SPDX-License-Identifier: Apache-2.0

package llmproviders

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"
)

// TestResolvedEmbedIdentity_FillsTheArmsDefaultModel pins the case the whole
// seam turns on: config names NO model, which is the ordinary no-[embedder]
// deployment, and the arm supplies one.
//
// THE EXPECTATION IS SPEC-AUTHORED, not read back from the resolver: this is the
// tuple the deployment expects recorded on knowledge:default and the code graphs.
// Comparing the resolver against something the resolver itself derived would be
// an identity check that passes however wrong both sides are.
func TestResolvedEmbedIdentity_FillsTheArmsDefaultModel(t *testing.T) {
	cfg, err := config.Parse([]byte("[embedder]\nprovider = \"voyage\"\n[credentials]\nvoyage_api_key = \"test-key\"\n"))
	require.NoError(t, err)
	t.Cleanup(config.SetForTest(cfg))

	// The SECTION states no model — which is exactly why an identity built from
	// it alone would be wrong.
	sec, err := cfg.ResolveEmbedder()
	require.NoError(t, err)
	require.Empty(t, sec.Model, "fixture premise: the resolved section carries no model")

	id, err := ResolvedEmbedIdentity(embed.InputRoleDocument)
	require.NoError(t, err)
	require.NotNil(t, id)
	assert.Equal(t, "voyage", id.GetProvider())
	assert.Equal(t, "voyage-code-3", id.GetModel(),
		"an empty model would be RECORDED on the graph at first embed and is permanent short of a migration")
	assert.Equal(t, int32(256), id.GetDimension())
	assert.Equal(t, "ubinary", id.GetDtype())
}

// TestResolvedEmbedIdentity_HonorsAnExplicitModel is the known-positive for the
// literal above: change the config and the stated identity changes with it. It
// is what stops "voyage-code-3" being a value the resolver hardcodes — and a
// hardcoded model is precisely the parallel constant this seam must not have.
func TestResolvedEmbedIdentity_HonorsAnExplicitModel(t *testing.T) {
	cfg, err := config.Parse([]byte(
		"[embedder]\nprovider = \"voyage\"\nmodel = \"voyage-3-large\"\n[credentials]\nvoyage_api_key = \"test-key\"\n"))
	require.NoError(t, err)
	t.Cleanup(config.SetForTest(cfg))

	id, err := ResolvedEmbedIdentity(embed.InputRoleDocument)
	require.NoError(t, err)
	require.NotNil(t, id)
	assert.Equal(t, "voyage-3-large", id.GetModel())
}

// TestResolvedEmbedIdentity_CarriesNoCredential pins the field set an identity
// is allowed to have. An identity names what the vectors ARE; the key and the
// base_url are facts about how THIS machine reaches the provider, they differ
// per machine, and one on the wire would be a credential in a catalog response.
//
// It reads a config that SETS both, so what the identity omits is a real
// omission rather than a field nothing populated in the first place.
func TestResolvedEmbedIdentity_CarriesNoCredential(t *testing.T) {
	cfg, err := config.Parse([]byte(
		"[embedder]\nprovider = \"voyage\"\nbase_url = \"http://embed.internal:9999/v1\"\nkey = \"secret-key\"\n"))
	require.NoError(t, err)
	t.Cleanup(config.SetForTest(cfg))

	sec, err := cfg.ResolveEmbedder()
	require.NoError(t, err)
	require.NotEmpty(t, sec.BaseURL, "fixture premise: the config supplies a base_url to be omitted")
	require.NotEmpty(t, sec.ResolveEmbedKey(), "fixture premise: the config supplies a key to be omitted")

	id, err := ResolvedEmbedIdentity(embed.InputRoleDocument)
	require.NoError(t, err)
	require.NotNil(t, id)
	// The identity resolved WITHOUT either: the proto carries no credential field
	// at all, so the credential stayed behind in config, where it belongs. The
	// four fields it does carry are what the vectors ARE.
	assert.Equal(t, "voyage", id.GetProvider())
	assert.Equal(t, "voyage-code-3", id.GetModel())
	assert.Equal(t, int32(256), id.GetDimension())
	assert.Equal(t, "ubinary", id.GetDtype())
}

// TestResolvedEmbedIdentity_IsNilWhenNoEmbedderCanBeBuilt pins the tie to the
// embedder: no embedder, no vectors, no claim.
//
// It matters because the identity and the embedder must agree about whether this
// client embeds at all. An identity resolved for a client that produces nothing
// would be stated on embed-axis scans and could be REFUSED against a graph
// recorded differently — for work that was never going to happen.
func TestResolvedEmbedIdentity_IsNilWhenNoEmbedderCanBeBuilt(t *testing.T) {
	// Cleared explicitly: the provider-resolved credential falls back to the
	// environment, so a machine with a real key would otherwise build an embedder
	// and this case would never be exercised.
	t.Setenv("VOYAGE_API_KEY", "")
	cfg, err := config.Parse([]byte("[embedder]\nprovider = \"voyage\"\n"))
	require.NoError(t, err)
	t.Cleanup(config.SetForTest(cfg))

	id, err := ResolvedEmbedIdentity(embed.InputRoleDocument)
	require.NoError(t, err, "a missing credential is the documented BM25-only degrade, not an error")
	assert.Nil(t, id)
}
