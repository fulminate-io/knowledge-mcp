// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// The matrix's PARITY half, split out of resolution_matrix_test.go when the row
// rewrites pushed that file past the 500-line block. It moved byte-identically;
// the rows, the all-language walk and assertImportBound all stay there.

// TestResolutionParticipationParity keeps the matrix's eleven exemptions from
// rotting into a stale comment.
//
// It DERIVES participation from the query sources — a language participates iff
// its TopLevel query captures a name, or it registers a declaration-name
// resolver, or it has a TestBlocks query — and requires that derived set to
// equal the set of matrix rows marked participating. The exempt list is never
// restated here; producing it is the derived side's job, and hard-coding it
// would turn this into a copy of the matrix with no power to invalidate it.
//
// THE AUTO-INVALIDATION IS THE POINT. Add an @name capture to cssQueries and
// css moves into the derived set, the equality breaks, and the build stays red
// until someone writes css a real matrix row with real assertions. Both
// directions fail: a language that gains naming while still marked exempt, and
// one marked participating that names nothing.
//
// Observed exempt at authoring time, recorded as a value and not an
// expectation: hcl, protobuf, css, html, sql, dockerfile, svelte, toml,
// markdown, yaml and cue.
func TestResolutionParticipationParity(t *testing.T) {
	derived := map[treesitter.Language]bool{}
	for _, lang := range treesitter.RegisteredLanguages() {
		topLevel, testBlocks, hasResolver := treesitter.LanguageNamingSources(lang)
		if strings.Contains(topLevel, "@name") || testBlocks != "" || hasResolver {
			derived[lang] = true
		}
	}
	declared := map[treesitter.Language]bool{}
	for lang, row := range testResolutionMatrix {
		if row.participates {
			declared[lang] = true
		}
	}

	// KNOWN-POSITIVE CONTROLS for an equality that two empty maps would also
	// satisfy: both sides must be non-empty, and neither may cover everything —
	// a derivation that returned true for every language would agree with a
	// matrix that marked every language participating, and prove nothing.
	require.NotEmpty(t, derived, "control: no language derived as participating")
	require.Less(t, len(derived), len(treesitter.RegisteredLanguages()),
		"control: every language derived as participating, so the exemptions are untested")

	assert.Equal(t, derived, declared,
		"the matrix's participating set must equal the one derived from the query sources")
}
