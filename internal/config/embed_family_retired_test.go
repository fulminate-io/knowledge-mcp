// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEmbedFamily_RetiredTransformersRefusedSurvivorsAccepted is the PROPERTY
// PAIR for the embedder-family vocabulary after the transformers graph family
// was removed.
//
// It is driven through config.Parse on a real operator-authored file rather
// than through IsValid on the constant set, because the thing that has to hold
// is what happens to a config already sitting on an upgrading operator's disk:
// bad input ERRORS, naming the offending value and the vocabulary that would
// have worked. An artifact check on AcceptedEmbedFamilies would agree with the
// constant list by construction and observe nothing about the parser.
//
// BOTH DIRECTIONS ARE NEEDED, AND THE SECOND IS A SIBLING LEG RATHER THAN ITS
// OWN TEST. A parser that refused EVERY family table would satisfy the first
// subtest alone; a parser that accepted every key would satisfy the second
// alone. Only the pair pins "this exact key left the vocabulary and the rest
// did not", and keeping them in one test keeps them in one run, so the
// known-positive can never be skipped while the absence claim is reported.
func TestEmbedFamily_RetiredTransformersRefusedSurvivorsAccepted(t *testing.T) {
	t.Run("the retired family key is refused, naming it", func(t *testing.T) {
		_, err := Parse([]byte("[embedder.family.transformers]\nprofile = \"default\"\n"))
		require.Error(t, err, "a config carrying the retired family must not parse")
		assert.Contains(t, err.Error(), "transformers",
			"the refusal names the offending key, so an operator knows which line to delete")
		for _, survivor := range AcceptedEmbedFamilies {
			assert.Containsf(t, err.Error(), survivor.String(),
				"the refusal lists the accepted set; %q is missing from it", survivor)
		}
	})

	t.Run("every surviving family is still accepted", func(t *testing.T) {
		// The KNOWN-POSITIVE for the refusal above: the same parser, the same
		// section, the same shape of file. Without this leg a parser that
		// refused every [embedder.family.*] table would look correct.
		require.NotEmpty(t, AcceptedEmbedFamilies, "the accepted set is not empty")
		for _, family := range AcceptedEmbedFamilies {
			body := fmt.Sprintf("[embedder.family.%s]\nprofile = \"default\"\n", family)
			cfg, err := Parse([]byte(body))
			require.NoErrorf(t, err, "surviving family %q must still parse", family)

			resolved, err := cfg.ResolveEmbedProfileForFamily(family.String())
			require.NoErrorf(t, err, "surviving family %q must still resolve a profile", family)
			assert.Equalf(t, DefaultEmbedProfileName, resolved.Name,
				"family %q resolves to the profile its table names", family)
		}

		// The retired name is not merely absent from the constant block — it is
		// absent from the SET the parser reads, which is the thing the refusal
		// above depends on.
		assert.NotContains(t, AcceptedEmbedFamilies, EmbedFamily("transformers"),
			"the retired family is gone from the accepted set itself")
	})
}

// TestEmbedFamily_RetiredCollectorFamiliesAreRefusedByName is the SAME property
// for the families whose built-in collectors were removed, and it is written with
// the retired keys SPELLED OUT rather than derived from AcceptedEmbedFamilies.
//
// THE DERIVATION IS WHY IT IS A SEPARATE TEST. The sibling above loops the
// accepted set, so deleting a constant makes its rows pass by construction — the
// vocabulary agrees with itself whatever it contains. These names are the ones an
// operator's file on disk actually carries, because the starter template told
// them to write it: the shipped `config/starter.tmpl` listed cloud and cicd among
// the families for as long as those collectors were built in. A literal is the
// only spelling that can fail when the constant comes back.
//
// THIS IS THE ONE PLACE THE RETIREMENT REACHES A FILE THE USER WROTE, and the
// answer is a hard error at load with no fallback: a silently ignored family
// table would leave a graph embedded under an identity nobody chose, which is the
// outcome the whole family-profile mechanism exists to prevent.
func TestEmbedFamily_RetiredCollectorFamiliesAreRefusedByName(t *testing.T) {
	for _, retired := range []string{"cicd", "cloud"} {
		t.Run(retired, func(t *testing.T) {
			_, err := Parse([]byte("[embedder.family." + retired + "]\nprofile = \"default\"\n"))
			require.Errorf(t, err, "a config carrying the retired family %q must not parse", retired)
			assert.Containsf(t, err.Error(), retired,
				"the refusal names the offending key, so an operator knows which line to delete")
			for _, survivor := range AcceptedEmbedFamilies {
				assert.Containsf(t, err.Error(), survivor.String(),
					"the refusal lists the accepted set; %q is missing from it", survivor)
			}
			assert.NotContains(t, AcceptedEmbedFamilies, EmbedFamily(retired),
				"and the name is gone from the set the parser reads")
		})
	}

	// THE RESOLVE PATH IS THE SECOND GATE and it is reached by callers that never
	// parsed a file — the embed pipeline asks for a family by name. A retired
	// family must be refused there too, or a config that failed to load and a
	// caller passing the string directly would disagree.
	for _, retired := range []string{"cicd", "cloud"} {
		var cfg *Config
		_, err := cfg.ResolveEmbedProfileForFamily(retired)
		require.Errorf(t, err, "resolving the retired family %q must be refused", retired)
		assert.Containsf(t, err.Error(), retired, "naming it")
	}

	// KNOWN POSITIVE through the same call: a surviving family still resolves, so
	// the two rows above are a statement about these names rather than about a
	// resolver that refuses everything.
	var cfg *Config
	prof, err := cfg.ResolveEmbedProfileForFamily("code")
	require.NoError(t, err, "control: a surviving family still resolves")
	assert.Equal(t, DefaultEmbedProfileName, prof.Name)
}
