// SPDX-License-Identifier: Apache-2.0

// spawn_supersession_test.go — the supersession's own evidence.
//
// The two effect-level checks replace TEN spelling checks (the owner's R5
// amendment; the spawn-import census test is NOT replaced and no row here
// touches it). A supersession is only as good as the comparison behind it, and
// the comparison RUNS IN BOTH DIRECTIONS:
//
//   - a hit the ten find that the pair MISSES is a blocker: the supersession
//     would lose coverage;
//   - a hit the PAIR finds that the ten do not is equally a blocker until it is
//     read and shown to be a real defect: the supersession would trade a silent
//     check set for a noisy one.
//
// THE CORPUS CARRIES ONE FILE PER CELL, NOT PER AXIS NAME. The ten checks span
// ten cells read from their own patterns: five spawn APIs (Command,
// CommandContext, StartProcess, Exec, ForkExec), two arities, two
// composite-literal shapes, one post-construction assignment, and an ARGUMENT
// POSITION axis the API list does not name, since CommandContext puts the
// command SECOND behind a context. ALIAS IS NOT A CELL — every one of the ten
// captures the package qualifier as $P — so the alias varies inside the files
// instead of spending a cell.

package corpus

import (
	"context"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/ast"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// supersededWhere is the where-tree NINE of the ten share, read from the checks
// graph at their stored ids. Its `not` leg is a BARE transport literal with no
// flows_to inside it, so the mere PRESENCE of a transport anywhere in the
// function exonerates it — which is the imprecision the replacement removes.
const supersededWhere = `{"all": [
	{"inside_pattern": {"of": "$match", "pattern": "func $F($$$P2) $$$R { $$$B }", "as": "FN"}},
	{"contains_pattern": {"of": "FN", "pattern": "$X.GetCommand()", "as": "ACC"}},
	{"flows_to": {"from": "ACC", "to": "CMD", "within": "FN"}},
	{"not": {"contains_pattern": {"of": "FN", "pattern": "$MP.CommandTransport{$$$T}"}}}
]}`

// supersededWhereNoFlow is the TENTH check's tree. It carries NO flows_to leg,
// which is why a re-authoring must not assume ten uniform legs.
const supersededWhereNoFlow = `{"all": [
	{"inside_pattern": {"of": "$match", "pattern": "func $F($$$P2) $$$R { $$$B }", "as": "FN"}},
	{"contains_pattern": {"of": "FN", "pattern": "$X.GetCommand()", "as": "ACC"}},
	{"not": {"contains_pattern": {"of": "FN", "pattern": "$MP.CommandTransport{$$$T}"}}}
]}`

// supersededChecks are the ten by pattern and where-tree, in the order their
// node names number them.
var supersededChecks = []struct {
	name    string
	pattern string
	where   string
}{
	{"1/10 CommandContext, no argv", "$P.CommandContext($CTX, $CMD)", supersededWhere},
	{"2/10 CommandContext, argv", "$P.CommandContext($CTX, $CMD, $$$ARGS)", supersededWhere},
	{"3/10 Command, no argv", "$P.Command($CMD)", supersededWhere},
	{"4/10 Command, argv", "$P.Command($CMD, $$$ARGS)", supersededWhere},
	{"5/10 Cmd literal, Path alone", "$P.Cmd{Path: $CMD}", supersededWhere},
	{"6/10 Cmd literal, Path plus fields", "$P.Cmd{Path: $CMD, $$$REST}", supersededWhere},
	{"7/10 StartProcess", "$P.StartProcess($CMD, $$$REST)", supersededWhere},
	{"8/10 Exec", "$P.Exec($CMD, $$$REST)", supersededWhere},
	{"9/10 ForkExec", "$P.ForkExec($CMD, $$$REST)", supersededWhere},
	{"10/10 post-construction Path", "$C.Path = $CMD", supersededWhereNoFlow},
}

// scanDir runs one check body over a directory and returns the base names it
// flagged, deduped and sorted — the unit the comparison is made in, since the
// two check sets root at different nodes and would otherwise report different
// line numbers for the same defect.
func scanDir(t *testing.T, dir, pattern, whereJSON string) []string {
	t.Helper()
	pat, err := ast.Parse(pattern)
	require.NoError(t, err, "pattern %q", pattern)
	cp, err := ast.Compile(pat, treesitter.LangGo, "")
	require.NoError(t, err, "pattern %q", pattern)
	defer cp.Close()
	where, err := ast.ParseWhere([]byte(whereJSON))
	require.NoError(t, err)
	require.NoError(t, ast.ValidateWhereCaptureRefs(where, pat))
	matches, _, err := ast.Match(context.Background(), dir, treesitter.LangGo, cp, where, ast.Scope{IncludeTests: true})
	require.NoError(t, err)
	seen := map[string]bool{}
	var out []string
	for _, m := range matches {
		seen[filepath.Base(m.FilePath)] = true
	}
	for f := range seen {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

func spawnCellsDir(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("testdata", "spawncells"))
	require.NoError(t, err)
	return abs
}

func TestSupersession_ThePairReproducesEveryCellTheTenFind(t *testing.T) {
	dir := spawnCellsDir(t)

	// THE TEN, cell by cell. Each is asserted to find its OWN cell — a check
	// that found nothing would otherwise make the union comparison below pass
	// for the wrong reason, and a corpus that failed to parse would look like a
	// clean supersession.
	tenUnion := map[string]bool{}
	for _, c := range supersededChecks {
		hits := scanDir(t, dir, c.pattern, c.where)
		assert.NotEmpty(t, hits, "superseded check %s must find its own cell; an empty result makes the union comparison vacuous", c.name)
		for _, h := range hits {
			tenUnion[h] = true
		}
	}

	pairA := scanDir(t, dir, checkAPattern, checkAWhere)
	pairB := scanDir(t, dir, checkBPattern, checkBWhere)
	pairUnion := map[string]bool{}
	for _, h := range append(append([]string{}, pairA...), pairB...) {
		pairUnion[h] = true
	}

	// DIRECTION ONE: nothing the ten find may be missed by the pair.
	var missed []string
	for f := range tenUnion {
		if !pairUnion[f] {
			missed = append(missed, f)
		}
	}
	sort.Strings(missed)
	assert.Empty(t, missed, "the pair misses cells the ten catch, so the supersession would lose coverage")

	// DIRECTION TWO: nothing the pair finds may be unexplained. Here the pair
	// finding MORE is correct and is the point of the re-authoring — the ten
	// exonerate a function that merely CONTAINS a transport literal, while the
	// pair asks whether the command reaches its Command field — but the
	// direction is still asserted, because the executed run at the target tree
	// took exactly this direction and no round had looked for it.
	var extra []string
	for f := range pairUnion {
		if !tenUnion[f] {
			extra = append(extra, f)
		}
	}
	sort.Strings(extra)
	assert.Empty(t, extra, "the pair flags cells the ten do not; read each one before accepting the supersession")

	// AND THE CORPUS IS COMPLETE: ten cells, all flagged. Without this the two
	// comparisons above are satisfiable by a corpus nothing matched.
	assert.Len(t, tenUnion, 10, "the ten-cell corpus must be exercised end to end")
	assert.Len(t, pairUnion, 10, "the pair must cover all ten cells")
}

func TestSupersession_TheTenAreSilentOverThisRepository(t *testing.T) {
	// THE FIRST LEG OF THE COMPARISON, over the corpus the checks actually run
	// on. The ten return nothing here — their `not` leg is a bare transport
	// literal, so the one function that builds a transport is exonerated
	// whatever its command does — and the pair returns nothing either
	// (TestSpawnEffectChecks_AreCleanOverThisRepositoryWithLayeredControls),
	// so the supersession neither loses a hit nor gains one on this tree.
	if testing.Short() {
		t.Skip("walks the whole repository")
	}
	root := repoRootForScan(t)
	for _, c := range supersededChecks {
		hits := scanDir(t, root, c.pattern, c.where)
		assert.Empty(t, hits, "superseded check %s over this repository", c.name)
	}
}
