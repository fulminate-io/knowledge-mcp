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
