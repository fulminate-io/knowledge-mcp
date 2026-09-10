// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A FILE OF ITS OWN, and not only because its neighbor crossed the 500-line
// gate. The assertions in collect_docs_contract_test.go are about the COLLECTOR
// CONTRACT — what a provider must return and what the daemon does with it. This
// one is about where a reader gets a collector somebody else already wrote,
// which is a different subject with a different owner: the eight collector
// READMEs carry the same pointer, each held by its own module's test, and this
// is the ninth surface.

// TestCollectDocs_TheGuideNamesWhereThePublishedCollectorsLive is the guide's
// half of the published-artifact pointer, and it is a REQUIRED-PRESENCE
// assertion for the same reason the two above are.
//
// WHY IT HAS TO BE HERE. A set of collectors is published and maintained rather
// than written from scratch, and this page is where a reader learns that: it
// names the repository the releases live in and the install script that fetches
// one. That sentence is the only route from this document to a working
// collector without writing one, so a page that quietly stopped carrying it
// would send every reader down the write-your-own path with nothing saying the
// other exists. The eight collector READMEs carry the same pointer and each is
// held by its own module's test; this is the ninth surface and it had none.
//
// IT ASSERTS THE MEANING, NOT THE WORDING: the repository name and the install
// script, so the section can be rewritten freely.
func TestCollectDocs_TheGuideNamesWhereThePublishedCollectorsLive(t *testing.T) {
	pages := guidePagesMentioningCustomCollectors(t)
	page, ok := pages["tools/custom_collector.md"]
	require.True(t, ok, "the census must reach the contract page")

	assert.Contains(t, page, "knowledge-contrib",
		"the guide must name the repository the published collectors and their releases live in")
	assert.Contains(t, page, "install script",
		"the guide must name the install script as the way to get a published collector")
}
