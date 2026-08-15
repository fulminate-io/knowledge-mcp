// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// assertRowExpect is assertion (5) of the resolution matrix: the row's
// designated reference resolves through the expected rung with the expected
// number of candidates, AND the edges it produced carry the expected Method.
//
// BOTH HALVES ARE NEEDED AND NEITHER SUBSUMES THE OTHER. resolveRef alone
// cannot see Method at all — the constant is attached when the group is
// emitted, downstream of resolution — so a row could expect a DYNAMIC outcome,
// get a dynamic RULE, and still emit an ambiguous group without this second
// half noticing. The edge half alone cannot tell which rung produced the group.
//
// It lives in its own file because the matrix file it serves is close enough to
// the 500-line lefthook block that adding it there would breach the cap.
func assertRowExpect(
	t *testing.T, lang treesitter.Language, row matrixRow, res PopulateResult, ix *declIndex,
) {
	t.Helper()

	found := 0
	for _, result := range chunkFixture(t, row.files) {
		for i := range result.Edges {
			e := &result.Edges[i]
			if e.Ref == nil || e.ToID != row.expect.Ref {
				continue
			}
			found++
			got := resolveRef(ix, e.Ref, e.ToID)
			assert.Equal(t, row.expect.Rule, got.Rule,
				"%s: reference %q fired the wrong rung", lang, row.expect.Ref)
			assert.Len(t, got.Candidates, row.expect.Candidates,
				"%s: reference %q resolved to the wrong number of candidates", lang, row.expect.Ref)
		}
	}
	// THE FIXTURE-DERIVED KNOWN POSITIVE. Every assertion above is inside a
	// loop, so a fixture that emitted no reference with this target at all —
	// because its query changed, or because the source no longer parses —
	// would satisfy the whole block vacuously.
	require.Positive(t, found,
		"%s: the fixture emitted no reference with target %q, so this row proves nothing",
		lang, row.expect.Ref)

	// The group key ends with the verbatim target, so the edges belonging to
	// this reference are the ones whose Evidence carries that suffix.
	//
	// THEY ARE COUNTED PER GROUP, NEVER IN TOTAL. A container chunk and its
	// member chunk both walk the member's body, so one source token routinely
	// emits the SAME reference twice — two groups of N rather than one — and a
	// total would be a multiple of the cardinality the fixture actually
	// declares. Per group, the count is the fixture-derived constant.
	suffix := ":" + row.expect.Ref
	groups := map[string][]string{}
	for _, e := range res.Edges {
		if e.Evidence != "" && strings.HasSuffix(e.Evidence, suffix) {
			groups[e.Evidence] = append(groups[e.Evidence], e.Method)
		}
	}

	if row.expect.Method == "" {
		assert.Empty(t, groups,
			"%s: a single-candidate outcome emits no group, so no edge carries a group key", lang)
		return
	}
	require.NotEmpty(t, groups,
		"%s: expected a %s group for %q and found none", lang, row.expect.Method, row.expect.Ref)
	for key, methods := range groups {
		assert.Len(t, methods, row.expect.Candidates,
			"%s: group %q must carry one edge per declaration the fixture declares", lang, key)
		for _, m := range methods {
			assert.Equal(t, row.expect.Method, m,
				"%s: group %q carries the wrong Method", lang, key)
		}
	}
}

// importedPackageName returns the symbol namespace the chunker recorded for one
// fixture file, which is the third argument populate passes to ScopeID when it
// builds that file's declarations' scope. Empty when the file produced no
// chunks, which is also what a ScopeFile language's scope construction ignores.
func importedPackageName(results []*treesitter.Result, path string) string {
	for _, r := range results {
		if r.FilePath != path || len(r.Chunks) == 0 {
			continue
		}
		return r.Chunks[0].Context.PackageName
	}
	return ""
}

// TestResolutionMatrixMethodConstantsAreDistinct is the control that keeps
// assertRowExpect's Method half meaningful. If the two constants were ever
// collapsed into one string, every dynamic row would pass an ambiguous
// assertion and vice versa, and no per-row gate could tell.
func TestResolutionMatrixMethodConstantsAreDistinct(t *testing.T) {
	assert.NotEqual(t, kgtypes.EdgeMethodDynamic, kgtypes.EdgeMethodAmbiguousName)
	assert.NotEmpty(t, kgtypes.EdgeMethodDynamic)
	assert.NotEmpty(t, kgtypes.EdgeMethodAmbiguousName)
}
