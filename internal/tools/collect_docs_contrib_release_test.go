// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// collect_docs_contrib_release_test.go — the guide says where a released
// collector build comes from, and does not say publishing is missing.
//
// WHY THIS EXISTS, and it is a lesson about gates rather than about this page.
// The doc sweep that wrote this paragraph said, truthfully at the time, that
// publishing these binaries from a knowledge-contrib repository was follow-on
// work that did not exist. The publishing then landed while the sweep's own
// branch waited to rebase, and the sentence became false inside the very commit
// whose subject is that no claim outlives its truth. It was caught by reading
// the landed commit during a rebase, which is luck rather than a gate.
//
// A CLAIM THAT SOMETHING DOES NOT EXIST YET IS THE SHORTEST-LIVED KIND, and this
// file is one row of each polarity for that reason: the positive requires the
// page to name where a build comes from, and the negative requires the
// does-not-exist wording to be gone, so a revert or a copy-forward cannot restore
// it in silence. The sibling gatedTailStatements list carries the same lesson
// from two directions at once and says so.
//
// AND THE CLAIM MOVED AGAIN, for the same reason and one round later. The
// install script landed, it ends by running `knowledge collector add`, and the
// sentence saying no script writes the config entry became false in its turn —
// so the positive half now pins what is STILL the operator's, which is supplying
// the credential the script deliberately never writes. The retired list below
// carries the previous claim, so a copy-forward cannot restore it in silence.

// retiredReleaseClaims are the phrases the paragraph carried while publishing
// was genuinely follow-on work. Each is false now.
var retiredReleaseClaims = []string{
	"There is no one-command install yet",
	"follow-on work and does not exist today",
	// Retired by the install script, which ends by running `knowledge collector
	// add`: the entry IS written for you now, and what remains yours is the
	// credential, which that script never writes.
	"What no script does yet is write the",
	"registration is still yours",
}

func TestCustomCollectorGuide_StatesWhereAReleasedBuildComesFrom(t *testing.T) {
	pages := guidePagesMentioningCustomCollectors(t)
	page, ok := pages["tools/custom_collector.md"]
	require.True(t, ok, "the census must reach the custom-collector guide")

	// POSITIVE HALF: where a build comes from, what is verified, and what the
	// operator still owes. An absence assertion alone is satisfied by a page that
	// simply stopped mentioning releases.
	for _, want := range []string{
		"knowledge-contrib",
		"checksums.txt",
		"install script",
		"What the script does not do is supply your credentials",
	} {
		assert.Containsf(t, page, want,
			"the guide must say where a released collector build comes from, what is verified, and what is still the "+
				"operator's — which is the credential, not the entry. Missing: %q", want)
	}

	// NEGATIVE HALF: the retired does-not-exist wording stays gone.
	for _, gone := range retiredReleaseClaims {
		assert.NotContainsf(t, page, gone,
			"the guide still says %q. Publishing landed; a reader told it does not exist will build from source for no "+
				"reason, and will not know a checksum-verified archive was available", gone)
	}
}
