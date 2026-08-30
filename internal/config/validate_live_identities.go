// SPDX-License-Identifier: Apache-2.0

package config

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// validate_live_identities.go cross-checks the config against the embed
// identities the LOCAL GRAPHS ACTUALLY CARRY.
//
// QUOTED FROM THE DECISION THIS IMPLEMENTS: "the client must be able to
// construct an embedder for any identity its graphs carry, so a profile (or
// credential) for a still-referenced identity being deleted is a loud
// config-validation error naming the graphs that depend on it, not a silent
// BM25 degrade."
//
// THIS IS A TRUTHFUL-INABILITY SURFACE, not a convenience. The alternative —
// resolving to nothing and searching BM25-only — produces confident-looking
// results computed without the vector arm, and the reader has no way to tell
// they got a worse answer. The candidate set (which graphs, which identity,
// which missing profile or credential) IS the correct output.
//
// CREDENTIAL VALUES ARE NEVER NAMED. Every message here names graphs, an
// identity and a provider; assertions about a credential report presence or
// length, never content. That is the same rule EmbedSection.Key states, and it
// binds these messages too.
//
// THE GATE IS DELIBERATELY NARROW so it cannot become noise. A profile defined
// but referenced by nothing is legal — an operator staging a future migration.
// A graph with no recorded identity is legal — it has not embedded. The error
// fires only on the intersection: a RECORDED identity with no constructible
// embedder.

// LiveGraphIdentity is one graph and the embed identity it has recorded. A zero
// Identity.Provider means the graph records none.
type LiveGraphIdentity struct {
	GraphType string
	Name      string
	Identity  RecordedIdentity
}

// String names the graph the way an operator names it: type and name.
func (g LiveGraphIdentity) String() string { return g.GraphType + "/" + g.Name }

// ValidateAgainstLiveIdentities reports every recorded identity this config
// cannot construct an embedder for, NAMING the graphs that depend on it.
//
// ERRORS ARE JOINED, not returned one at a time, for the same reason
// Config.Validate joins its consumer errors: an operator whose config lost a
// [credentials] block may have broken several identities at once, and fixing
// them one restart at a time is the experience this avoids.
//
// THE GROUPING IS BY IDENTITY, not by graph, because the repair is per
// identity: restoring one profile fixes every graph that recorded it, and a
// message repeated once per graph would obscure that they are one problem.
func (c *Config) ValidateAgainstLiveIdentities(live []LiveGraphIdentity) error {
	byIdentity := map[RecordedIdentity][]string{}
	for _, g := range live {
		if g.Identity.Provider == "" {
			// NOT AN ERROR: the graph has never embedded, so there is no identity
			// to be unable to construct. Its first embed will record one.
			continue
		}
		byIdentity[g.Identity] = append(byIdentity[g.Identity], g.String())
	}

	identities := make([]RecordedIdentity, 0, len(byIdentity))
	for id := range byIdentity {
		identities = append(identities, id)
	}
	// A STABLE ORDER, so two runs against the same config produce the same
	// message. Map iteration order would otherwise reshuffle a multi-identity
	// failure on every start, which makes a startup log impossible to diff.
	sort.Slice(identities, func(i, j int) bool {
		return identities[i].String() < identities[j].String()
	})

	var errs []error
	for _, id := range identities {
		graphs := byIdentity[id]
		sort.Strings(graphs)
		if err := c.identityIsConstructible(id, graphs); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// identityIsConstructible reports why this config cannot build an embedder for
// one recorded identity, or nil when it can. graphs is the sorted list of
// graphs that recorded it, and every message names them.
func (c *Config) identityIsConstructible(id RecordedIdentity, graphs []string) error {
	named := strings.Join(graphs, ", ")

	if !id.Provider.IsValid() {
		return fmt.Errorf(
			"config: %s is embedded by provider %q, which this build does not know how to construct — "+
				"its vectors cannot be searched semantically by this client",
			named, id.Provider)
	}
	if !id.Provider.IsAPI() {
		// The deterministic fake makes no network call, so it needs neither a
		// credential nor an endpoint.
		return nil
	}

	key, baseURL, fromProfile := c.ResolveIdentityCredential(id)

	// THE ENDPOINT HALF, and it is checked FIRST because it is the failure that
	// otherwise passes silently. For a provider whose name does not determine its
	// endpoint, a resolved key is not enough: the arm falls back to its default
	// endpoint, so a graph embedded elsewhere would be queried against a
	// DIFFERENT vector space and every call would report success.
	if EndpointIsProfileDefined(id.Provider) && !fromProfile {
		return fmt.Errorf(
			"config: %s is embedded by %s and no [embedder.profile] matches that identity, so this "+
				"client does not know which endpoint produced those vectors — %q names a protocol rather "+
				"than one service, and building the embedder without a profile would query a different "+
				"endpoint's vector space. Restore the profile, or migrate those graphs deliberately with "+
				"manage(operation:\"migrate_embed_identity\")",
			named, id, id.Provider)
	}

	// THE CREDENTIAL HALF. key-or-base_url is the same rule ValidateEmbedCredential
	// enforces for a configured section; here it is asked of an identity a graph
	// already depends on, which is why the message names the graphs.
	if key == "" && baseURL == "" {
		return fmt.Errorf(
			"config: %s is embedded by %s and no credential or base_url resolves for %q, so this client "+
				"cannot embed a query for those graphs — set [credentials].%s_api_key, define an "+
				"[embedder.profile] matching that identity, or migrate those graphs deliberately with "+
				"manage(operation:\"migrate_embed_identity\")",
			named, id, id.Provider, id.Provider)
	}
	return nil
}
