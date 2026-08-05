// SPDX-License-Identifier: Apache-2.0

// walk_exclusion_disclosure_test.go — the walk's exclusion disclosure, checked
// against an independent measurement rather than against itself.
//
// The engine's per-rule tally and the census script's are two measurements of
// the same quantity taken by different means: the census reconciles `git
// ls-files` against a rule chain ported by hand into awk, the engine reports
// what its own Go chain declined as it declined it. Asserting the engine
// against its own output would prove only that it is self-consistent; asserting
// it against the frozen baseline is what makes a drift in either one visible.

package ast

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// censusSection is one `repo=<name> discovery=<path>` block of the frozen
// baseline: the counts it recorded per rule, and how many files it kept.
type censusSection struct {
	included int
	byRule   map[string]int
}

// parseCensusSection reads one section out of the frozen baseline. It fails the
// test rather than returning empty when the section is missing, because an empty
// expectation is exactly the shape that makes an agreement check vacuous.
func parseCensusSection(t *testing.T, path, repo, discovery string) censusSection {
	t.Helper()
	f, err := os.Open(path) // #nosec G304 -- fixed testdata path
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	want := "repo=" + repo + " discovery=" + discovery
	sec := censusSection{included: -1, byRule: map[string]int{}}
	inSection := false
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case line == want:
			inSection = true
		case !inSection:
			continue
		case strings.HasPrefix(line, "repo="):
			inSection = false // the next section starts; this one is done
		case strings.HasPrefix(line, "included="):
			n, convErr := strconv.Atoi(strings.TrimPrefix(line, "included="))
			require.NoError(t, convErr)
			sec.included = n
		case strings.HasPrefix(line, "rule="):
			var rule string
			var count int
			fields := strings.Fields(line)
			require.Len(t, fields, 2, "malformed rule line %q", line)
			rule = strings.TrimPrefix(fields[0], "rule=")
			count, convErr := strconv.Atoi(strings.TrimPrefix(fields[1], "count="))
			require.NoError(t, convErr)
			sec.byRule[rule] = count
		}
	}
	require.NoError(t, scanner.Err())
	require.NotEqual(t, -1, sec.included, "baseline has no section %q", want)
	require.NotEmpty(t, sec.byRule, "baseline section %q recorded no rules", want)
	return sec
}

// TestWalkStats_ExclusionTallyAgreesWithFrozenCensus is the cross-check plus the
// truncation row. The truncation subtest runs everywhere; the agreement subtest
// needs the fixture corpus and skips without it, following the same convention
// as the package's other corpus tests.
func TestWalkStats_ExclusionTallyAgreesWithFrozenCensus(t *testing.T) {
	t.Run("a rule past the sample cap reports truncated with a capped sample", func(t *testing.T) {
		// Eleven markdown files against a sample cap of five: the count must be
		// exact, the names capped, and the rule flagged — so a caller can never
		// read the short list as the whole story.
		const declined = 11
		files := map[string]string{"keep.go": "package main\n\nfunc A() {\n\tdefer x.Close()\n}\n"}
		for i := range declined {
			files["doc"+strconv.Itoa(i)+".md"] = "# doc\n"
		}
		dir := fixtureRepo(t, files)

		_, stats := honestyMatchOK(t, dir, treesitter.LangGo, honestyDeferPattern, Scope{})

		assert.Equal(t, declined, stats.ExcludedByRule["skip_extension"],
			"the COUNT is exact and uncapped")
		samples := stats.ExcludedSamples["skip_extension"]
		assert.True(t, len(samples) > 0 && len(samples) < declined,
			"the NAMES are capped: got %d for %d declined files", len(samples), declined)
		assert.True(t, stats.ExcludedTruncated["skip_extension"],
			"and the rule is flagged, so a short list is not read as a short decline set")
		for _, s := range samples {
			assert.Contains(t, s, ".md", "a sample must name a file the rule actually declined")
		}

		// Known negative through the same probe: a rule inside the budget is not
		// flagged, so the flag tracks the cap rather than being always-on.
		assert.False(t, stats.ExcludedTruncated["skip_lockfile"],
			"a rule that declined nothing must not report truncation")
	})

	t.Run("engine tally agrees with the frozen c-redis census", func(t *testing.T) {
		home, err := os.UserHomeDir()
		require.NoError(t, err)
		repoDir := filepath.Join(home, "code", "test-repos", "c-redis")
		if _, statErr := os.Stat(repoDir); os.IsNotExist(statErr) {
			t.Skipf("fixture repo not found at %s — clone the corpus first", repoDir)
		}

		want := parseCensusSection(t, filepath.Join("testdata", "walk_exclusion_baseline.txt"), "c-redis", "git")

		// A pattern that matches little; the subject here is the walk's
		// accounting, not its matches.
		_, stats := honestyMatchOK(t, repoDir, treesitter.LangC, "$F($$$A);", Scope{})

		require.Equal(t, "git", stats.DiscoveryPath,
			"c-redis is a git repo, so the git path must be what produced the tally")
		for rule, wantCount := range want.byRule {
			assert.Equal(t, wantCount, stats.ExcludedByRule[rule],
				"rule %s: engine tally disagrees with the frozen census", rule)
		}
		// The frozen census's own langcheck row for this repo: of 746 tracked
		// C files, 482 are under deps/ and one is over the size cap, leaving
		// 263. That number is measured by a different mechanism than the walk,
		// so scanning exactly it is the agreement that matters most here — the
		// per-rule counts above are repo-wide, while this is the slice a caller
		// scoping to one language actually receives.
		assert.Equal(t, 263, stats.FilesScanned,
			"the C slice of the walk must match the census's langcheck row")

		// A rule far past the cap on a real tree: truncation is not a synthetic
		// property.
		assert.Greater(t, stats.ExcludedByRule["skip_path_component"], len(stats.ExcludedSamples["skip_path_component"]),
			"setup: c-redis must decline more paths than the sample cap shows")
		assert.True(t, stats.ExcludedTruncated["skip_path_component"],
			"a rule declining hundreds of paths must be flagged truncated")
	})
}

// TestScope_LiftedExclusionsWidenWalkAndAreDisclosed pins the distinction the
// override exists for: a run that was FORBIDDEN to exclude anything and a run
// that had NOTHING to exclude both report zero exclusions, and must not render
// identically.
func TestScope_LiftedExclusionsWidenWalkAndAreDisclosed(t *testing.T) {
	const body = `package p

func A() {
	defer x.Close()
}
`
	// api.pb.go is declined by skip_generated_go and vendor/lib.go by
	// skip_path_component (skip_dir on this non-git walk): both carry the
	// pattern, so lifting the rules is observable in the MATCHES and not only
	// in the stats.
	dir := fixtureRepo(t, map[string]string{
		"keep.go":       body,
		"api.pb.go":     body,
		"vendor/lib.go": body,
	})

	unlifted, unliftedStats := honestyMatchOK(t, dir, treesitter.LangGo, honestyDeferPattern, Scope{})
	lifted, liftedStats := honestyMatchOK(t, dir, treesitter.LangGo, honestyDeferPattern,
		Scope{LiftExclusions: true})

	// THE WIDENING.
	assert.Equal(t, []string{"keep.go"}, matchedFiles(unlifted),
		"unlifted, the rules decline both of the other files")
	assert.ElementsMatch(t, []string{"keep.go", "api.pb.go", "vendor/lib.go"}, matchedFiles(lifted),
		"lifted, the declined files are walked and matched")
	assert.Greater(t, liftedStats.FilesScanned, unliftedStats.FilesScanned,
		"lifting must widen the walk, not merely relabel it")

	// THE DISCLOSURE. The unlifted run names what it declined; the lifted run
	// declines nothing and says WHY its counts are zero.
	assert.Equal(t, 1, unliftedStats.ExcludedByRule["skip_generated_go"])
	assert.Equal(t, "nongit", unliftedStats.DiscoveryPath)
	assert.Equal(t, "nongit+lifted", liftedStats.DiscoveryPath,
		"a lifted run must be self-identifying")
	for rule, n := range liftedStats.ExcludedByRule {
		assert.Equal(t, 0, n, "rule %s declined %d files on a lifted run", rule, n)
	}

	// THE CONTROL that makes the disclosure load-bearing: a tree with nothing to
	// exclude produces the same all-zero tally as the lifted run above, so the
	// zeros alone cannot distinguish them. Only discovery_path can, and here it
	// does.
	cleanDir := fixtureRepo(t, map[string]string{"keep.go": body})
	_, cleanStats := honestyMatchOK(t, cleanDir, treesitter.LangGo, honestyDeferPattern, Scope{})
	for rule, n := range cleanStats.ExcludedByRule {
		assert.Equal(t, 0, n, "control: rule %s declined %d files in a clean tree", rule, n)
	}
	assert.Equal(t, "nongit", cleanStats.DiscoveryPath,
		"nothing to exclude is NOT the same fact as not being allowed to exclude")
	assert.NotEqual(t, liftedStats.DiscoveryPath, cleanStats.DiscoveryPath,
		"the two zero-exclusion runs must be distinguishable in the response")
}
