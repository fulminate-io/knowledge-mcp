// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// embed_profiles.go holds the NAMED embedder profile surface and the
// per-family creation defaults that reference profiles by name.
//
// WHY A LIST OF PROFILES RATHER THAN ONE SECTION. A single [embedder] table
// can only describe one embedder, so choosing a different provider or width
// for one graph meant editing the one table — which is the same edit every
// other graph reads. Named profiles let an operator define several complete
// embedders at once (a code profile, a text profile, a local endpoint) and
// choose among them per graph BY NAME, without any edit being a global one.
//
// A PROFILE IS EmbedSection PLUS A NAME, deliberately: reusing the section
// shape means the profile surface inherits every rule the section already
// carries — the provider vocabulary, the accepted width and dtype sets, the
// per-section key precedence — rather than growing a second, drifting copy.
//
// THE HARD PROHIBITION, and it is the spine of the design rather than a
// caution: parsing or editing a profile MUST NOT touch any graph's recorded
// embed identity. A graph's identity is recorded at first embed and is
// authoritative thereafter, so no config change — adding, editing or removing
// a profile — may implicitly re-embed an existing graph. Nothing in this file
// writes an identity, and there is deliberately no code path from config load
// to one; the only writers are the first-embed record and the explicit
// per-graph migrate operation.

// DefaultEmbedProfileName is the name of the profile the single [embedder]
// table defines.
//
// THE SINGLE-SECTION SHAPE BECOMES THE DEFAULT PROFILE, which is what keeps
// every config written before profiles existed valid and unchanged: such a
// config defines exactly one profile, named "default", and every family
// resolves to it. An ABSENT [embedder] is not a missing profile either — it
// resolves through ResolveEmbedder to the same documented defaults it always
// has.
const DefaultEmbedProfileName = "default"

// EmbedFamily names a graph FAMILY for the purpose of choosing a creation-time
// embedder profile. The values are the graph-type vocabulary the store already
// uses.
//
// THE VOCABULARY IS DECLARED LOCALLY, NOT IMPORTED, for the same reason
// EmbedProvider is (see embed.go): this package is a true leaf with no
// upstream deps, and importing the graph-type package to name six strings
// would end that. The cost is that the two lists must be kept in step by hand;
// the alternative cost is a dependency edge from config to the type system it
// configures.
//
// ONLY THE EMBEDDABLE FAMILIES ARE LISTED. Naming a family here is a claim
// that a graph of that family gets embedded and therefore has an embed
// identity to choose; linkage and the raw log/web/pdf graphs are not embedded,
// so a profile default for them would configure something that never runs.
type EmbedFamily string

// EmbedFamily constants — the accepted [embedder.family.<family>] keys.
const (
	EmbedFamilyKnowledge    EmbedFamily = "knowledge"
	EmbedFamilyCode         EmbedFamily = "code"
	EmbedFamilyPractice     EmbedFamily = "practice"
	EmbedFamilyCloud        EmbedFamily = "cloud"
	EmbedFamilyCICD         EmbedFamily = "cicd"
	EmbedFamilyTransformers EmbedFamily = "transformers"
)

// AcceptedEmbedFamilies is the set of families a creation default may name,
// in the order the error message lists them.
var AcceptedEmbedFamilies = []EmbedFamily{
	EmbedFamilyKnowledge,
	EmbedFamilyCode,
	EmbedFamilyPractice,
	EmbedFamilyCloud,
	EmbedFamilyCICD,
	EmbedFamilyTransformers,
}

// IsValid reports whether f is one of the accepted families.
func (f EmbedFamily) IsValid() bool { return slices.Contains(AcceptedEmbedFamilies, f) }

// String returns f as a plain string.
func (f EmbedFamily) String() string { return string(f) }

// EmbedProfile is a named embedder profile: an EmbedSection plus the name it
// is referenced by.
type EmbedProfile struct {
	// Name is the profile's name — "default" for the one the single
	// [embedder] table defines, otherwise the [embedder.profile.<name>] key.
	Name string
	EmbedSection
}

// unknownEmbedProfileError reports a profile name no profile defines. It
// mirrors unknownEmbedProviderError's shape — name the offending value AND
// list the accepted vocabulary — so an operator never has to guess a spelling.
//
// THE DEFINED LIST IS COMPUTED, NOT LITERAL, because unlike the provider
// vocabulary the profile set is the operator's own: telling them "one of:
// voyage, cohere..." would be useless here, and telling them nothing would
// leave a typo indistinguishable from a profile they forgot to define.
func unknownEmbedProfileError(got string, defined []string) error {
	return fmt.Errorf("unknown embedder profile %q (defined: %s)", got, strings.Join(defined, ", "))
}

// unknownEmbedFamilyError reports a family key outside the accepted set.
func unknownEmbedFamilyError(got string) error {
	names := make([]string, len(AcceptedEmbedFamilies))
	for i, f := range AcceptedEmbedFamilies {
		names[i] = f.String()
	}
	return fmt.Errorf("unknown graph family %q (accepted: %s)", got, strings.Join(names, ", "))
}

// EmbedProfileNames returns every defined profile name, sorted, always
// including "default".
//
// "default" IS ALWAYS PRESENT because it is always defined: it is the single
// [embedder] table, and an absent table still resolves to the documented
// defaults. There is no configuration in which a graph has no profile to be
// created under.
func (c *Config) EmbedProfileNames() []string {
	names := []string{DefaultEmbedProfileName}
	if c != nil {
		for name := range c.EmbedProfiles {
			if name != DefaultEmbedProfileName {
				names = append(names, name)
			}
		}
	}
	sort.Strings(names)
	return names
}

// EmbedProfileByName returns the named profile, or an error naming the value
// and listing the defined names.
//
// NO DEFAULT-ON-MISS. An unknown name is a typo or a profile the operator
// deleted while a family default still referenced it; answering with the
// default profile would silently embed a graph under an identity nobody
// chose, and — because the identity recorded at first embed is authoritative
// — that wrong choice would then be permanent for that graph short of an
// explicit migrate.
//
// AN EMPTY NAME RESOLVES TO "default" rather than erroring: absent is not the
// same as wrong, and every caller that has no family preference passes it.
func (c *Config) EmbedProfileByName(name string) (EmbedProfile, error) {
	if name == "" {
		name = DefaultEmbedProfileName
	}
	if name == DefaultEmbedProfileName {
		sec, err := c.ResolveEmbedder()
		if err != nil {
			return EmbedProfile{}, err
		}
		return EmbedProfile{Name: DefaultEmbedProfileName, EmbedSection: sec}, nil
	}
	var sec EmbedSection
	var found bool
	if c != nil {
		sec, found = c.EmbedProfiles[name]
	}
	if !found {
		return EmbedProfile{}, unknownEmbedProfileError(name, c.EmbedProfileNames())
	}
	// THE SHAPE IS RE-CHECKED HERE, not only at parse. A Config built in Go
	// never passes through the TOML translator, which is the same reason
	// embed.Config.Validate re-checks what translateEmbedSection already did.
	if err := validateEmbedShape(sec.Provider, sec.Dimension, sec.Dtype); err != nil {
		return EmbedProfile{}, fmt.Errorf("config: section [embedder.profile.%s]: %w", name, err)
	}
	return EmbedProfile{Name: name, EmbedSection: sec}, nil
}

// ResolveEmbedProfileForFamily returns the profile a NEW graph of the given
// family is created under: the family's [embedder.family.<family>] profile
// reference when one is set, otherwise the default profile.
//
// CREATION-TIME ONLY. The result is consulted at a graph's FIRST embed to
// choose the identity that is then recorded on the graph and is authoritative
// thereafter. It is not a lookup any later embed of that graph performs, and
// changing it does not reach back: a graph embedded under one profile stays on
// its recorded identity even after the family default moves, because the
// snapshot on the graph is the truth rather than the profile it came from.
//
// AN EMPTY FAMILY RESOLVES TO THE DEFAULT PROFILE, which is what a caller with
// no family in hand should get; a NON-EMPTY family outside the accepted set is
// an error rather than a silent fallthrough, because it is a config typo whose
// only other outcome is a graph quietly created under the wrong identity.
func (c *Config) ResolveEmbedProfileForFamily(family string) (EmbedProfile, error) {
	if family == "" {
		return c.EmbedProfileByName(DefaultEmbedProfileName)
	}
	fam := EmbedFamily(strings.ToLower(strings.TrimSpace(family)))
	if !fam.IsValid() {
		return EmbedProfile{}, fmt.Errorf("config: section [embedder.family]: %w", unknownEmbedFamilyError(family))
	}
	name := DefaultEmbedProfileName
	if c != nil {
		if ref, ok := c.EmbedFamilyProfiles[fam.String()]; ok && ref != "" {
			name = ref
		}
	}
	prof, err := c.EmbedProfileByName(name)
	if err != nil {
		return EmbedProfile{}, fmt.Errorf("config: section [embedder.family.%s]: %w", fam, err)
	}
	return prof, nil
}

// applyEmbedProfiles translates the [embedder.profile.<name>] and
// [embedder.family.<family>] tables onto cfg and checks that every family
// default names a profile that exists.
//
// THE REFERENCE CHECK RUNS HERE, AT LOAD, and that placement is the point. A
// family default is consulted at a graph's FIRST embed, which may be days
// after the config was written; a dangling reference discovered there would
// surface at the exact moment an identity is about to be recorded, to nobody
// watching. Failing at load puts it in front of the operator who typed it.
func applyEmbedProfiles(cfg *Config, raw parseEmbedSection) error {
	if len(raw.Profile) > 0 {
		profiles := make(map[string]EmbedSection, len(raw.Profile))
		for _, name := range sortedKeys(raw.Profile) {
			if err := checkEmbedProfileName(name); err != nil {
				return err
			}
			sec, err := translateEmbedShape("embedder.profile."+name, embedShapeFields(raw.Profile[name]))
			if err != nil {
				return err
			}
			profiles[name] = sec
		}
		cfg.EmbedProfiles = profiles
	}
	if len(raw.Family) > 0 {
		fams := make(map[string]string, len(raw.Family))
		for _, key := range sortedKeys(raw.Family) {
			fam := EmbedFamily(strings.ToLower(strings.TrimSpace(key)))
			if !fam.IsValid() {
				return fmt.Errorf("config: section [embedder.family]: %w", unknownEmbedFamilyError(key))
			}
			ref := strings.TrimSpace(raw.Family[key].Profile)
			if ref == "" {
				return fmt.Errorf("config: section [embedder.family.%s]: profile is required "+
					"(a family table exists only to name a profile)", fam)
			}
			fams[fam.String()] = ref
		}
		cfg.EmbedFamilyProfiles = fams
	}
	return validateEmbedFamilyRefs(cfg)
}

// sortedKeys returns m's keys in sorted order, so a config carrying two bad
// tables always reports the same one first — a map-order-dependent error
// message is a flaky failure for anyone testing or scripting against it.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// checkEmbedProfileName refuses a profile name that cannot be referenced
// unambiguously.
//
// "default" IS RESERVED, not merely discouraged: it already names the profile
// the single [embedder] table defines, so an [embedder.profile.default] table
// would give one name two definitions and leave which one a family reference
// reaches a matter of lookup order. Refusing is the only reading that cannot
// silently create a graph under the wrong identity.
func checkEmbedProfileName(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("config: section [embedder.profile]: a profile name may not be empty")
	}
	if name == DefaultEmbedProfileName {
		return fmt.Errorf("config: section [embedder.profile.%s]: %q is reserved for the profile the "+
			"single [embedder] table defines — name this profile something else",
			DefaultEmbedProfileName, DefaultEmbedProfileName)
	}
	return nil
}

// validateEmbedFamilyRefs checks that every family default names a profile
// that exists. It runs at parse time so a config referencing a profile the
// operator forgot to define fails at LOAD, where the operator is looking,
// rather than at the first embed of the next new graph of that family — which
// could be days later and is the moment an identity gets recorded.
func validateEmbedFamilyRefs(c *Config) error {
	if c == nil || len(c.EmbedFamilyProfiles) == 0 {
		return nil
	}
	for _, fam := range sortedKeys(c.EmbedFamilyProfiles) {
		if _, err := c.ResolveEmbedProfileForFamily(fam); err != nil {
			return err
		}
	}
	return nil
}
