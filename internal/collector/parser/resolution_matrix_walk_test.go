// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// The matrix's ALL-LANGUAGE WALK, split out of resolution_matrix_test.go when
// the row rewrites pushed that file past the 500-line block. It moved
// byte-identically. The ROWS stay in that file because the per-row gates
// extract them from it by name, and assertImportBound stays with them.

// TestResolutionMatrix_AllRegisteredLanguages is the all-language proof: for
// EVERY registered language, no CONTAINS edge crosses a file and no reference
// binds outside its own scope unit.
func TestResolutionMatrix_AllRegisteredLanguages(t *testing.T) {
	langs := treesitter.RegisteredLanguages()
	require.GreaterOrEqual(t, len(langs), 32,
		"control: the registry must be readable, and it held 32 languages when this was written")

	for _, lang := range langs {
		row, ok := testResolutionMatrix[lang]
		require.True(t, ok,
			"language %q is registered but has no resolution-matrix row: add one rather than shrinking the proof", lang)

		t.Run(string(lang), func(t *testing.T) {
			require.GreaterOrEqual(t, len(row.files), 2, "every row carries at least two files")
			res := populateFixture(t, row.files)

			byID := nodesByID(res)
			isFile := fileNodeIDs(res)
			require.Len(t, isFile, len(row.files), "every fixture file must produce a NodeFile node")

			// (1) NO CONTAINS EDGE CROSSES A FILE.
			for _, e := range res.Edges {
				if kgtypes.EdgeType(e.Type) != kgtypes.EdgeContains || !isFile[e.FromId] {
					continue
				}
				target, found := byID[e.ToId]
				require.True(t, found, "CONTAINS from %q points at unknown node %q", e.FromId, e.ToId)
				require.Equal(t, byID[e.FromId].FilePath, target.FilePath,
					"CROSS-FILE CONTAINS in %s: %q contains %q", lang, e.FromId, e.ToId)
			}

			ix := indexResults(t, chunkFixture(t, row.files))
			indexed := len(ix.byID)

			if !row.participates {
				// THE EXEMPTION, asserted rather than assumed. A non-participating
				// language names nothing, so nothing enters the index and the
				// zero above is a property of the language rather than of a
				// fixture that failed to parse — which is why chunk COUNT is
				// required to be positive on its own terms.
				chunks := 0
				for _, n := range res.Nodes {
					if nt := kgtypes.NodeType(n.Type); nt != kgtypes.NodeFile && nt != kgtypes.NodeLanguage {
						chunks++
						assert.Empty(t, n.SymbolName,
							"%s is declared non-participating, but %q carries a name", lang, n.Id)
					}
				}
				require.Positive(t, chunks,
					"control: %s produced no chunks at all, so its exemption is untested", lang)
				require.Zero(t, indexed, "a language that names nothing indexes nothing")
				return
			}

			// (2) THE PER-ROW KNOWN-POSITIVE CONTROL. Without it a language
			// whose fixture failed to parse satisfies every zero above.
			require.Positive(t, indexed,
				"control: %s indexed no declaration, so this row's assertions are vacuous", lang)

			// (3) NO REFERENCE BINDS OUTSIDE ITS OWN SCOPE UNIT. Import-bound
			// rules are exempt BY DESIGN — they bind into the IMPORTED scope.
			// This walk fills no binds, so nothing here can take that exemption
			// and any binding came from a scope rule; the rows that DO take it
			// assert the positive separately, at (4).
			for _, result := range chunkFixture(t, row.files) {
				for i := range result.Edges {
					e := &result.Edges[i]
					if kgtypes.EdgeType(e.Type) == kgtypes.EdgeContains ||
						kgtypes.EdgeType(e.Type) == kgtypes.EdgeImports || e.Ref == nil {
						continue
					}
					got := resolveRef(ix, e.Ref, e.ToID)
					if got.Rule == RuleQualifiedImport || got.Rule == RuleUnqualifiedImport {
						continue
					}
					for _, c := range got.Candidates {
						assert.Equal(t, e.Ref.Scope, c.Scope,
							"%s: reference %q bound outside its scope unit (rule %s)", lang, e.ToID, got.Rule)
					}
				}
			}

			// (4) THE IMPORT EXEMPTION, TAKEN. For a language with a registered
			// arm the row names the file it imports from, and at least one
			// reference must bind INTO that file's scope through the import
			// rule — the property (3) can only ever exempt, never prove.
			if row.importBound != "" {
				assertImportBound(t, lang, row)
			}

			// (5) THE COLLIDING PAIR'S OUTCOME. (3) can only ever say a binding
			// stayed inside its scope; it cannot say WHICH rung fired or
			// whether an open set was emitted where a closed one was meant.
			if row.expect.Ref != "" {
				assertRowExpect(t, lang, row, res, ix)
			}
		})
	}
}
