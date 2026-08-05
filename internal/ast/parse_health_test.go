// SPDX-License-Identifier: Apache-2.0

// parse_health_test.go — the walk's parse-health accounting, proven at two
// tiers because the causes are not equally reachable.
//
// TIER 1 (TestWalkStats_EndToEndCountersFire) drives three counters through a
// real ast.Match over a real fixture tree: a file the walk cannot READ, and a
// file whose parse succeeds only after tree-sitter error-recovers it. Each is
// paired with a CLEAN CONTROL in the same test where every counter reads zero,
// so a fixture cannot pass by making everything non-zero.
//
// TIER 2 (TestSkipCounters_EachReasonIncrementsItsOwnAndTheSum) drives the two
// remaining causes at the cause-to-counter seam, against the same walkCounters
// the production walk uses. Those two causes have NO reachable fixture:
// tree-sitter error-recovers malformed input into a tree with ERROR nodes
// rather than failing, and the parse-limit branch needs a 30-second wall-clock
// overrun behind an unexported const with no hook (see the NO-E2E-FIXTURE
// comments in match_walk.go). What Tier 2 proves is that each counter is WIRED and that the
// reason-to-counter mapping sends each cause to its own field — which is the
// declared-but-unwired and the mis-wired-switch failure both. It does not prove
// those two causes ever occur in production; they are defensive accounting, and
// they stay because FilesSkipped is DEFINED as the sum of its causes.

package ast

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// parseHealthCleanFile parses without error recovery and carries the pattern.
const parseHealthCleanFile = `package p

func A() {
	defer x.Close()
}
`

// parseHealthDegradedFile carries the pattern AND a genuine syntax error, so
// its tree is error-recovered (HasError true) and still contributes matches.
// That combination is the whole point: a file that is neither skipped nor
// clean, which before this change produced no signal at all.
const parseHealthDegradedFile = `package p

func B() {
	defer y.Close()
}

func Broken( {
	this is not go
}
`

// TestWalkStats_EndToEndCountersFire is Tier 1: three counters driven non-zero
// through a real walk, each against a clean control that must stay zero.
func TestWalkStats_EndToEndCountersFire(t *testing.T) {
	// THE CLEAN CONTROL, measured first so the non-zero rows below are read
	// against a known zero rather than against an assumption. Every one of the
	// five counters must be zero over a tree with nothing wrong with it —
	// without this row, a counter wired to increment unconditionally would
	// satisfy every other assertion in this test.
	cleanDir := fixtureRepo(t, map[string]string{"clean.go": parseHealthCleanFile})
	cleanRaws, clean := honestyMatchOK(t, cleanDir, treesitter.LangGo, honestyDeferPattern, Scope{})

	require.Len(t, cleanRaws, 1, "setup: the clean fixture must match, or the control proves nothing")
	assert.Equal(t, 1, clean.FilesScanned)
	assert.Equal(t, 0, clean.FilesSkipped, "control: a clean tree skips nothing")
	assert.Equal(t, 0, clean.SkippedRead)
	assert.Equal(t, 0, clean.SkippedParseError)
	assert.Equal(t, 0, clean.SkippedParseLimit)
	assert.Equal(t, 0, clean.FilesWithParseErrors, "control: a clean parse is not degraded")
	assert.Equal(t, 0, clean.MatchesFromDegradedTrees)

	t.Run("SkippedRead fires on a file the walk cannot read", func(t *testing.T) {
		// A *.go symlink pointing at a DIRECTORY. Discovery hands it to the
		// walk (it stats as a normal small entry), and os.ReadFile then fails
		// with EISDIR. Root-safe, unlike a chmod-000 file, which a root test
		// runner would happily read. A dangling symlink and a symlink loop both
		// fail EARLIER, inside discovery's size rule, and never reach the walk
		// at all — so neither drives this counter.
		dir := fixtureRepo(t, map[string]string{"clean.go": parseHealthCleanFile})
		require.NoError(t, os.Mkdir(filepath.Join(dir, "sub"), 0o750))
		require.NoError(t, os.Symlink(filepath.Join(dir, "sub"), filepath.Join(dir, "unreadable.go")))

		raws, stats := honestyMatchOK(t, dir, treesitter.LangGo, honestyDeferPattern, Scope{})

		assert.Equal(t, 1, stats.SkippedRead, "the unreadable file must be attributed to the READ cause")
		assert.Equal(t, 1, stats.FilesSkipped, "and must show up in the total")
		assert.Equal(t, 0, stats.SkippedParseError, "a read failure is not a parse failure")
		assert.Equal(t, 0, stats.SkippedParseLimit)
		// The walk is not derailed: the readable sibling is still scanned and
		// still matches, so the skip cost exactly one file.
		assert.Equal(t, 1, stats.FilesScanned)
		assert.Equal(t, []string{"clean.go"}, matchedFiles(raws))
	})

	t.Run("degraded counters fire on an error-recovered parse that still matches", func(t *testing.T) {
		dir := fixtureRepo(t, map[string]string{
			"clean.go":    parseHealthCleanFile,
			"degraded.go": parseHealthDegradedFile,
		})

		raws, stats := honestyMatchOK(t, dir, treesitter.LangGo, honestyDeferPattern, Scope{})

		// The degraded file is NOT skipped — that is the case with no signal
		// before this change. It parsed, it was walked, and it contributed.
		assert.Equal(t, 0, stats.FilesSkipped, "an error-recovered parse is not a skip")
		assert.Equal(t, 2, stats.FilesScanned, "both files are scanned")
		assert.Equal(t, 1, stats.FilesWithParseErrors, "exactly the broken file is degraded")
		assert.Equal(t, 1, stats.MatchesFromDegradedTrees, "and its match is attributed to the degraded tree")

		// The matches themselves are unfiltered: report, do not guess. Both
		// files contribute, and the degraded one's match is still returned.
		assert.ElementsMatch(t, []string{"clean.go", "degraded.go"}, matchedFiles(raws))
		assert.Len(t, raws, 2)

		// The degraded-match count tracks MATCHES, not files: it must not be a
		// second copy of the file counter. The clean sibling's match is not
		// degraded-origin, so the two numbers differ here by construction.
		assert.NotEqual(t, len(raws), stats.MatchesFromDegradedTrees,
			"matches_from_degraded_trees must count degraded-origin matches, not every match")
	})
}

// TestSkipCounters_EachReasonIncrementsItsOwnAndTheSum is Tier 2: the
// cause-to-counter seam, driven against the real production accumulator.
//
// Every case asserts BOTH halves — that the intended counter moved AND that the
// other two did not — because a switch wired to increment the wrong field, or
// every field, is exactly the defect this tier exists to catch.
func TestSkipCounters_EachReasonIncrementsItsOwnAndTheSum(t *testing.T) {
	t.Run("each reason increments only its own counter", func(t *testing.T) {
		cases := []struct {
			name    string
			reason  skipReason
			read    int
			parseEr int
			parseLm int
		}{
			{"read", skipRead, 1, 0, 0},
			{"parse error", skipParseError, 0, 1, 0},
			{"parse limit", skipParseLimit, 0, 0, 1},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				var c walkCounters
				c.recordSkip(tc.reason)

				var stats WalkStats
				c.applyTo(&stats)

				assert.Equal(t, tc.read, stats.SkippedRead)
				assert.Equal(t, tc.parseEr, stats.SkippedParseError)
				assert.Equal(t, tc.parseLm, stats.SkippedParseLimit)
				assert.Equal(t, 1, stats.FilesSkipped, "one recorded skip is one skipped file")
			})
		}
	})

	t.Run("skipNone records nothing", func(t *testing.T) {
		// The known negative that keeps recordSkip from being an
		// increment-anything function: the zero value must move no counter.
		var c walkCounters
		c.recordSkip(skipNone)

		var stats WalkStats
		c.applyTo(&stats)

		assert.Equal(t, 0, stats.FilesSkipped)
		assert.Equal(t, 0, stats.SkippedRead)
		assert.Equal(t, 0, stats.SkippedParseError)
		assert.Equal(t, 0, stats.SkippedParseLimit)
	})

	t.Run("FilesSkipped is the exact sum of the three causes", func(t *testing.T) {
		// Deliberately distinct per-cause counts, so the sum cannot be
		// satisfied by any single counter, by a doubled one, or by a total
		// tracked independently of its parts.
		const (
			reads       = 2
			parseErrors = 3
			parseLimits = 5
		)
		var c walkCounters
		for range reads {
			c.recordSkip(skipRead)
		}
		for range parseErrors {
			c.recordSkip(skipParseError)
		}
		for range parseLimits {
			c.recordSkip(skipParseLimit)
		}

		var stats WalkStats
		c.applyTo(&stats)

		assert.Equal(t, reads, stats.SkippedRead)
		assert.Equal(t, parseErrors, stats.SkippedParseError)
		assert.Equal(t, parseLimits, stats.SkippedParseLimit)

		// Against a fixture-derived constant, not against a re-derivation of
		// the same three fields: comparing the total to the sum of the numbers
		// it was computed from would hold even if all four were wrong together.
		assert.Equal(t, reads+parseErrors+parseLimits, stats.FilesSkipped)
		assert.Equal(t, 10, stats.FilesSkipped)
		assert.Equal(t, stats.SkippedRead+stats.SkippedParseError+stats.SkippedParseLimit, stats.FilesSkipped,
			"files_skipped must remain the exact sum of its by-cause decomposition")
	})

	t.Run("degraded accounting is independent of the skip total", func(t *testing.T) {
		// A degraded file is scanned, not skipped — the distinction the whole
		// phase turns on. Recording one must move the degraded counters and
		// leave every skip counter alone.
		var c walkCounters
		c.recordParsed(true, 4)
		c.recordParsed(false, 9)

		var stats WalkStats
		c.applyTo(&stats)

		assert.Equal(t, 2, stats.FilesScanned, "both parsed files are scanned")
		assert.Equal(t, 1, stats.FilesWithParseErrors)
		assert.Equal(t, 4, stats.MatchesFromDegradedTrees,
			"only the degraded file's matches are degraded-origin")
		assert.Equal(t, 0, stats.FilesSkipped, "a degraded parse is not a skip")
	})
}
