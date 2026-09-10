// SPDX-License-Identifier: Apache-2.0

// spawn_effect_corpus_test.go — the two effect-level spawn checks run over THIS
// REPOSITORY, with every leg's contribution read rather than assumed.
//
// Split from spawn_effect_checks_test.go at the file-size threshold, and the
// seam is the topic's own: that file admits the checks against their fixture
// pairs, this one runs them against the corpus the fixtures cannot stand in
// for. The check bodies, the fixtures and the shared helpers live there.

package corpus

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/ast"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// ---------------------------------------------------------------------------
// The corpus run. A fixture pair proves only the axes it varies, and "the real
// tree" is the axis no pair can carry.
// ---------------------------------------------------------------------------

// repoRootForScan walks up from this package to the directory holding the
// repository's own .git, so the walk covers BOTH modules rather than the client
// one. Discovered rather than counted in "..", for the reason the census test's
// module-root discovery gives: a hardcoded depth breaks under a different
// checkout layout for a reason that is not the defect under test.
func repoRootForScan(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	require.NoError(t, err)
	for {
		if _, statErr := os.Stat(filepath.Join(dir, ".git")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Skip("no ancestor holds a .git; the corpus row needs a checkout to walk")
		}
		dir = parent
	}
}

// scanCorpus runs one check body over the repository and returns the hit paths.
func scanCorpus(t *testing.T, pattern, whereJSON string) []string {
	t.Helper()
	pat, err := ast.Parse(pattern)
	require.NoError(t, err)
	cp, err := ast.Compile(pat, treesitter.LangGo, "")
	require.NoError(t, err)
	defer cp.Close()
	where, err := ast.ParseWhere([]byte(whereJSON))
	require.NoError(t, err)
	require.NoError(t, ast.ValidateWhereCaptureRefs(where, pat))
	matches, _, err := ast.Match(context.Background(), repoRootForScan(t), treesitter.LangGo, cp, where,
		ast.Scope{IncludeTests: true})
	require.NoError(t, err)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m.FilePath)
	}
	return out
}

// TestSpawnEffectChecks_AreCleanOverThisRepositoryWithLayeredControls is the
// third admission condition, and the one four review rounds of fixture work
// could not have produced: the checks are run over the corpus they will run on,
// and every leg's contribution to the zero is read rather than assumed.
//
// A BARE ZERO PROVES NOTHING, so each check's zero is bracketed by the layers
// its own legs produce. Check A: a population of record-command reads, of which
// exactly 1 also reaches a spawn, of which 0 fail to reach the transport. Check
// B: a population of spawn-package importers, of which exactly 1 also reads a
// record command, of which 0 lack the transport. If a leg ever stops
// constraining, its layer collapses into the layer above it and this test says
// which one.
//
// THE TWO POPULATION LAYERS ARE ASSERTED BY MEMBERSHIP AND BOUND, NEVER BY AN
// EXACT COUNT, and that is a correction rather than a looseness. A population
// is a property of the CORPUS: any sibling branch that adds a record-command
// read or a spawn-package import moves it, and this test would then go red on
// whoever merges second and read as a defect in the checks rather than as
// corpus drift. Measured: an octopus merge of the two branches already in
// flight on this project branch takes the record-command population from 5 to 7
// while both checks stay silent and both NARROWING layers stay at 1.
//
// WHAT A POPULATION LAYER IS ACTUALLY FOR is the known-positive control — the
// walk reached the files the narrowing layers are about — plus an upper bound
// that catches a leg which has stopped constraining. Membership and a bound
// carry both. The NARROWING layers keep their exact values, because 1 is not a
// property of the corpus but of what the legs subtract from it, and a change
// there is the defect this test exists to catch.
func TestSpawnEffectChecks_AreCleanOverThisRepositoryWithLayeredControls(t *testing.T) {
	if testing.Short() {
		t.Skip("walks the whole repository")
	}

	insideOnly := `{"inside_pattern": {"of": "$match", "pattern": "func $F($$$P) $$$R { $$$B }", "as": "FN"}}`
	population := scanCorpus(t, checkAPattern, insideOnly)
	assertPopulation(t, "check A's record-command reads", population, populationBound, []string{
		"externalcollector/mcphost.go",
		"graphtypecrud/validate.go",
		"tools/graphtype_crud.go",
		// The config loader's legacy renderer reads a record command to
		// synthesize the `knowledge collector add` invocation for a catalog
		// family no config entry claims. It REPLACED tools/graphtype_mcp_roundtrip_test.go
		// in this membership list, which went with the registration tool's
		// retired args decode.
		"collectorconfig/legacy.go",
	})

	spawnReaching := scanCorpus(t, checkAPattern, `{"all": [`+insideOnly+`, `+checkASpawnLeg+`]}`)
	assert.Len(t, spawnReaching, 1,
		"the spawn leg must narrow the population; if it matches everything it is not constraining")
	if len(spawnReaching) == 1 {
		assert.Contains(t, spawnReaching[0], "externalcollector/mcphost.go",
			"the one record command that reaches a spawn is the host's, which is the file the census test's known positive blesses")
	}

	hitsA := scanCorpus(t, checkAPattern, checkAWhere)
	assert.Empty(t, hitsA,
		"check A must be silent over this repository: the one function that spawns a record-derived command hands it to the transport, "+
			"and the three that read a command without spawning are excluded by the spawn leg")

	importers := scanCorpus(t, checkBPattern, `{"all": [
		{"kind": {"of": "X", "is": "source_file"}},
		{"contains_pattern": {"of": "X", "pattern": "$$$P \"os/exec\"", "as": "IMP"}}
	]}`)
	// The import census the import-spec form makes expressible. It is the SAME
	// brittleness class as check A's population — every new file importing a
	// spawn package moves it — so it is asserted the same way. Every member but
	// the host is a legitimate operator-facing subprocess, which is exactly why
	// the record-command leg is part of the effect rather than a narrowing of
	// convenience.
	assertPopulation(t, "check B's spawn-package importers", importers, populationBound, []string{
		"externalcollector/mcphost.go",
		"bootstrap/lifecycle.go",
		"llm/claudecli/subprocess.go",
		"collector/parser/indexer_discover.go",
	})

	recordImporters := scanCorpus(t, checkBPattern, `{"all": [
		{"kind": {"of": "X", "is": "source_file"}},
		{"contains_pattern": {"of": "X", "pattern": "$$$P \"os/exec\"", "as": "IMP"}},
		{"contains_pattern": {"of": "X", "pattern": "$R.GetCommand()", "as": "CMD"}}
	]}`)
	require.Len(t, recordImporters, 1, "exactly one file both imports the spawn package and reads a record command")
	assert.Contains(t, recordImporters[0], "externalcollector/mcphost.go")

	hitsB := scanCorpus(t, checkBPattern, checkBWhere)
	assert.Empty(t, hitsB, "check B must be silent over this repository: the one such file builds the transport")
}

// populationBound is the ceiling a population layer may not cross. It is far
// above either population's measured size and far below the corpus, so it
// cannot go red on ordinary corpus growth and cannot stay green if a leg stops
// constraining: the walk scans over six thousand files, and a leg that matched
// everything would report thousands rather than hundreds.
const populationBound = 400

// assertPopulation is the membership-and-bound form both population layers use.
//
// IT ASSERTS THREE THINGS AND EACH ONE FAILS DIFFERENTLY. Membership is the
// known-positive control: these files are the ones the narrowing layers are
// about, so a walk that missed them makes every later assertion vacuous. The
// lower bound is implied by membership. The upper bound is the only thing that
// catches a leg which stopped constraining, which is the failure an exact count
// was really guarding and the only part of it that was not a property of the
// corpus.
func assertPopulation(t *testing.T, label string, got []string, bound int, mustContain []string) {
	t.Helper()
	for _, want := range mustContain {
		found := false
		for _, g := range got {
			if strings.Contains(g, want) {
				found = true
				break
			}
		}
		assert.True(t, found,
			"%s: the walk did not reach %s, so every narrowing assertion below it is vacuous", label, want)
	}
	assert.LessOrEqual(t, len(got), bound,
		"%s: the population is %d, past the bound — a leg has stopped constraining and is no longer a population but a scan",
		label, len(got))
	t.Logf("%s: %d members", label, len(got))
}
