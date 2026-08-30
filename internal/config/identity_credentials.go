// SPDX-License-Identifier: Apache-2.0

package config

import "fmt"

// identity_credentials.go answers ONE question, in ONE place: given an embed
// identity a graph has recorded, what credential and endpoint does THIS
// machine's config offer for it?
//
// IT LIVES HERE, NOT AT THE CONSTRUCTION SITE, because two callers ask it and
// they must not drift. The query path asks in order to BUILD an embedder for a
// graph's recorded identity; the validator asks in order to report, before
// anything is searched, that it CANNOT be built. Two copies of the precedence
// rule would eventually disagree, and the shape of that disagreement is the
// worst one available: a validator that says the config is fine and a query
// path that then cannot embed.
//
// AN IDENTITY CARRIES NO CREDENTIAL, deliberately — it names provider, model,
// dimension and dtype, which are facts about what the vectors ARE. How this
// machine reaches that provider is a local fact, differs per machine, and would
// be a credential in a catalog response if it rode along.

// RecordedIdentity is one graph's recorded embed identity, in this package's
// own vocabulary.
//
// IT IS NOT THE PROTOBUF TYPE. This package is a leaf with no upstream deps
// (the same reason EmbedProvider and EmbedFamily are declared locally rather
// than imported), so callers translate at the boundary.
type RecordedIdentity struct {
	Provider  EmbedProvider
	Model     string
	Dimension int
	Dtype     string
}

// String renders an identity for an operator: the four fields in a fixed order,
// in the same spelling manage(migrate_embed_identity) reports a transition in.
func (id RecordedIdentity) String() string {
	return fmt.Sprintf("%s/%s at %d %s", id.Provider, id.Model, id.Dimension, id.Dtype)
}

// EmbedProfileForIdentity returns the defined profile whose FULL identity tuple
// matches, and whether one did.
//
// THE MATCH IS ON ALL FOUR FIELDS, not just the provider, because a config may
// define several profiles for one provider — that is the point of profiles —
// and matching on the provider alone would hand a graph the credential and
// endpoint of a different one. It is also what makes the answer specific for
// the openai-compatible arm, which serves a whole population of third-party
// endpoints under one provider name.
func (c *Config) EmbedProfileForIdentity(id RecordedIdentity) (EmbedProfile, bool) {
	if c == nil {
		return EmbedProfile{}, false
	}
	for _, name := range c.EmbedProfileNames() {
		prof, err := c.EmbedProfileByName(name)
		if err != nil {
			// A profile that does not resolve cannot serve an identity. It is not
			// silently skipped in the operator's view: c.Validate reports a
			// malformed profile on its own, and reporting it twice from here would
			// attach a profile's shape error to a graph that merely failed to match
			// it.
			continue
		}
		if prof.Provider == id.Provider && prof.Model == id.Model &&
			prof.Dimension == id.Dimension && prof.Dtype == id.Dtype {
			return prof, true
		}
	}
	return EmbedProfile{}, false
}

// ResolveIdentityCredential returns the credential and endpoint this config
// offers for a recorded identity, and whether they came from a matching PROFILE.
//
// PROFILE FIRST, because a profile is where an operator states how to reach a
// SPECIFIC endpoint. PROVIDER-RESOLVED SECOND is not a degrade but this
// package's standing precedence (an explicit section key wins, else file-then-env
// by provider); a config naming its credential in [credentials] rather than in a
// profile is the ordinary case, not a broken one.
//
// fromProfile IS PART OF THE ANSWER, not a debugging extra. For a provider whose
// name does not determine its endpoint, "a key resolved" is not the same as "the
// right endpoint is known", and only the caller that knows which providers those
// are can act on the difference.
func (c *Config) ResolveIdentityCredential(id RecordedIdentity) (key, baseURL string, fromProfile bool) {
	if prof, ok := c.EmbedProfileForIdentity(id); ok {
		return prof.ResolveEmbedKey(), prof.BaseURL, true
	}
	return APIKeyForEmbedProvider(id.Provider), "", false
}

// EndpointIsProfileDefined reports whether a provider's endpoint can only be
// learned from a profile, rather than from the provider name.
//
// ONLY THE openai-compatible ARM. Every other provider name identifies exactly
// one vendor endpoint, so an identity naming it is enough to reach the same
// service the vectors came from. openai-compatible names a PROTOCOL, served by
// a whole population of third-party endpoints, and its arm falls back to
// OpenAI's own endpoint when no base_url is given — so a graph embedded against
// some other compatible server, whose profile has been deleted, would be
// queried against a DIFFERENT vector space while every call reported success.
func EndpointIsProfileDefined(p EmbedProvider) bool {
	return p == EmbedProviderOpenAICompatible
}
