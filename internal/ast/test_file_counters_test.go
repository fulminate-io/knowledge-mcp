// SPDX-License-Identifier: Apache-2.0

// test_file_counters_test.go — the walk reports what its own test-file filter
// did, in both directions.
//
// THE TWO COUNTERS ARE COMPLEMENTS AND BOTH ARE NEEDED. TestFilesScanned is
// zero exactly when the filter is on, TestFilesExcluded is zero exactly when it
// is off, and neither is derivable from the other without the corpus total —
// which the walk does not report. A consumer that wants to say "this run
// reached tests" reads the first; one that wants to explain a zero scan reads
// the second.
//
// Every row drives a REAL walk over a REAL tree, because the filter lives in
// discovery and a hand-built WalkStats would assert nothing about it.

package ast

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// counterFixture is one non-test file and one test file, each holding exactly
// one instance of the pattern. The match count is the known-positive control:
// an implementation that counted the right files while walking the wrong ones
// would satisfy the counter assertions and fail these.
func counterFixture() map[string]string {
	const body = `package p

type c struct{}

func (c) Close() error { return nil }

func A(x c) {
	defer x.Close()
}
`
	return map[string]string{"lib.go": body, "lib_test.go": body}
}

func TestMatch_TestFileCountersAreComplements(t *testing.T) {
	dir := fixtureRepo(t, counterFixture())

	t.Run("filter on: the test file is excluded and none is scanned", func(t *testing.T) {
		raws, stats := honestyMatchOK(t, dir, treesitter.LangGo, honestyDeferPattern, Scope{IncludeTests: false})
		require.Equal(t, 1, stats.FilesScanned, "setup: only the non-test file may reach the walk")
		assert.Equal(t, 1, stats.TestFilesExcluded, "the walk's own test-file filter dropped one file and must say so")
		assert.Equal(t, 0, stats.TestFilesScanned, "no test file was scanned, so the scanned counter is zero")
		assert.Equal(t, []string{"lib.go"}, matchedFiles(raws), "known-positive control: the match came from the non-test file")
	})

	t.Run("filter off: the test file is scanned and none is excluded", func(t *testing.T) {
		raws, stats := honestyMatchOK(t, dir, treesitter.LangGo, honestyDeferPattern, Scope{IncludeTests: true})
		require.Equal(t, 2, stats.FilesScanned, "setup: both files must reach the walk")
		assert.Equal(t, 0, stats.TestFilesExcluded, "nothing was dropped, so the excluded counter is zero")
		assert.Equal(t, 1, stats.TestFilesScanned, "one test file was scanned and the counter is how a reader knows")
		assert.Len(t, raws, 2, "known-positive control: both files contributed a match")
	})
}

// TestMatch_TestFileCountersDoNotTouchTheExclusionReport is R4's compatibility
// leg, asserted at the source rather than inferred from a whole-tree census.
// The scope filters are the CALLER'S OWN narrowing and discovery's exclusion
// report is deliberately blind to them; folding the test-file drop into
// excluded_by_rule would report a caller's request back as an exclusion.
func TestMatch_TestFileCountersDoNotTouchTheExclusionReport(t *testing.T) {
	dir := fixtureRepo(t, counterFixture())

	_, off := honestyMatchOK(t, dir, treesitter.LangGo, honestyDeferPattern, Scope{IncludeTests: false})
	_, on := honestyMatchOK(t, dir, treesitter.LangGo, honestyDeferPattern, Scope{IncludeTests: true})

	require.Equal(t, 1, off.TestFilesExcluded, "setup: the off run must actually have dropped a file")
	assert.Equal(t, on.ExcludedByRule, off.ExcludedByRule,
		"the exclusion report accounts for what DISCOVERY declined; a scope filter is not a discovery rule")
	assert.Equal(t, on.DiscoveryPath, off.DiscoveryPath, "and the same discovery path produced both")
}
