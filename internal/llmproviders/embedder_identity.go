// SPDX-License-Identifier: Apache-2.0

package llmproviders

import (
	"context"
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"
)

// embedder_identity.go builds an embedder for a GRAPH'S RECORDED IDENTITY rather
// than for the local configuration.
//
// WHY THE DISTINCTION IS THE WHOLE POINT. BuildEmbedder beside this resolves
// what THIS MACHINE is configured to embed with, which is the right answer when
// producing new vectors. It is the wrong answer when embedding a SEARCH QUERY:
// the stored vectors were produced by whatever embedder the graph recorded at
// its first embed, and a query embedded by anything else is a distance between
// two different vector spaces — a number that looks like a rank and means
// nothing. A graph stays on its recorded identity until an explicit migration,
// so config moving underneath it is expected rather than exceptional.
//
// CONFIG STILL SUPPLIES THE CREDENTIAL, and only the credential. An identity
// names provider, model, dimension and dtype — what the vectors ARE. It
// deliberately carries no key and no base_url, because those are facts about
// how THIS machine reaches that provider, they differ per machine, and a
// credential on the wire would be a credential in a catalog response.

// BuildEmbedderForIdentity constructs the embedder a graph's recorded identity
// names, resolving the credential and endpoint from local config.
//
// IT ERRORS RATHER THAN RETURNING NIL when the identity cannot be constructed.
// That is the deliberate difference from BuildEmbedder, which returns (nil, nil)
// for "no credential configured" — a documented BM25-only degrade that predates
// this and is correct for the WRITE side, where no credential simply means this
// machine produces no vectors. On the QUERY side the same silence is a lie: the
// graph HAS vectors, the caller asked for a semantic search, and answering with
// BM25 results returns a worse answer while reporting success. The caller is
// told what is missing so an operator can supply it.
func BuildEmbedderForIdentity(
	ctx context.Context, id *knowledgev1.EmbedIdentity, role embed.InputRole,
) (embed.BinaryEmbedder, error) {
	if id == nil {
		return nil, fmt.Errorf("build embedder for identity: no identity given")
	}
	provider := config.EmbedProvider(id.GetProvider())
	if !provider.IsValid() {
		return nil, fmt.Errorf(
			"graph is embedded by provider %q, which this build does not know how to construct; "+
				"its vectors cannot be searched semantically by this client",
			id.GetProvider())
	}

	key, baseURL := credentialForIdentity(id, provider)
	if provider.IsAPI() && key == "" && baseURL == "" {
		return nil, fmt.Errorf(
			"graph is embedded by %s/%s and no credential or base_url is configured for %s, so this "+
				"client cannot embed a query for it — set the credential, or define an [embedder.profile] "+
				"naming that provider",
			id.GetProvider(), id.GetModel(), id.GetProvider())
	}

	e, err := embed.NewEmbedder(ctx, &embed.Config{
		Provider:  provider,
		Model:     id.GetModel(),
		APIKey:    key,
		BaseURL:   baseURL,
		Dimension: int(id.GetDimension()),
		Dtype:     id.GetDtype(),
		InputRole: role,
	})
	if err != nil {
		return nil, fmt.Errorf("build embedder for %s/%s at %d %s: %w",
			id.GetProvider(), id.GetModel(), id.GetDimension(), id.GetDtype(), err)
	}
	return e, nil
}

// credentialForIdentity resolves the key and base_url for an identity, through
// the ONE implementation of the precedence rule.
//
// THE RULE LIVES IN config, NOT HERE, because the config validator asks the
// same question in order to report — before anything is searched — that an
// identity CANNOT be constructed. Two copies would eventually disagree, and the
// worst available shape of that disagreement is a validator that says the config
// is fine and a query path that then cannot embed. See
// config.ResolveIdentityCredential for the precedence itself.
func credentialForIdentity(id *knowledgev1.EmbedIdentity, provider config.EmbedProvider) (key, baseURL string) {
	cfg := activeConfig()
	if cfg == nil {
		return config.APIKeyForEmbedProvider(provider), ""
	}
	key, baseURL, _ = cfg.ResolveIdentityCredential(recordedIdentity(id, provider))
	return key, baseURL
}

// recordedIdentity translates the wire identity into the config package's own
// vocabulary. config is a leaf with no upstream deps, so the translation
// happens at this boundary rather than by config importing the generated types.
func recordedIdentity(id *knowledgev1.EmbedIdentity, provider config.EmbedProvider) config.RecordedIdentity {
	return config.RecordedIdentity{
		Provider:  provider,
		Model:     id.GetModel(),
		Dimension: int(id.GetDimension()),
		Dtype:     id.GetDtype(),
	}
}

// activeConfig returns the loaded config, or nil when none is loaded. Split out
// so the profile walk above reads as one thing.
func activeConfig() *config.Config {
	if !config.Loaded() {
		return nil
	}
	return config.Active()
}

// SortedIdentityKey renders an identity as a stable string, so callers can GROUP
// graphs by identity without comparing four fields at every site.
//
// THE ORDER IS FIXED AND THE SEPARATOR IS A NUL, for the same reason the cache
// key digest uses one: a model name containing the separator would otherwise let
// two different identities render the same string, and a grouping key that
// collides silently merges two vector spaces into one embed call.
func SortedIdentityKey(id *knowledgev1.EmbedIdentity) string {
	if id == nil {
		return ""
	}
	return fmt.Sprintf("%s\x00%s\x00%d\x00%s",
		id.GetProvider(), id.GetModel(), id.GetDimension(), id.GetDtype())
}
