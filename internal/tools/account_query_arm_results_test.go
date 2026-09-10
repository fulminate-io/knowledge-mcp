// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// account_query_arm_results_test.go asserts requirement 1's OWN observable on
// the client-composed query modes: the RESULT of an arm, with `account` and
// without it, compared verbatim.
//
// WHY THIS FILE EXISTS SEPARATELY FROM THE GATE TESTS. The first pass tested the
// param-accounting GATE — does the arm refuse the parameter — and treated a
// CONSUMED registry cell as proof that nothing else could happen. It was not:
// three arms read `account` on the client and changed their answer without ever
// reaching a server gate. A class assertion says where a value is DECLARED to
// go; only a result says whether it changed anything. The gate tests stay,
// because the refusal is a real behavior worth pinning, and these are added
// beside them rather than in place of them.
//
// EVERY ROW CARRIES A KNOWN-POSITIVE. Several of these arms return an early
// infrastructure refusal under the package's stub deps, and two identical
// refusals compare equal — a pair written without a control is vacuous green.

// TestQueryArm_MetadataStatsJSONIsIdenticalWithAccount is measurement (a): the
// format=json payload of query(mode:"metadata_stats").
//
// The defect it reds on: the composer passed the caller's `account` into
// engine.MetadataStatsJSONPayload, which emitted it as a top-level payload key,
// so the json body carried an extra member no call without the parameter had.
func TestQueryArm_MetadataStatsJSONIsIdenticalWithAccount(t *testing.T) {
	seeded := &knowledgev1.MetadataStatsResponse{
		MetadataStats: &knowledgev1.MetadataStats{Keys: map[string]*knowledgev1.KeyStats{
			"severity": {DistinctValues: 4, TotalWrites: 10},
		}},
	}
	drive := func(extra string) string {
		deps := &repoTestDeps{rootDir: t.TempDir(), gc: &fakeGraphCaller{metadataStatsResp: seeded}}
		handled, res := InterceptQueryMetadataStats(opCtx(), deps, kgtools.CallToolParams{
			Name: "query",
			Arguments: json.RawMessage(
				`{"mode":"metadata_stats","graph":"knowledge","format":"json"` + extra + `}`),
		})
		require.True(t, handled, "the metadata_stats arm must claim the call")
		require.False(t, res.IsError, "the arm must serve, not refuse: %s", toolResultText(res))
		return toolResultText(res)
	}

	without := drive("")
	with := drive(`,"account":"` + accountProbeValue + `"`)

	// KNOWN POSITIVE, read before the comparison: the base body is a real stats
	// payload rather than an empty or refusing one, so "identical" is a
	// statement about a result and not about two blanks.
	require.Contains(t, without, `"stats"`, "the base payload must carry the stats rows")
	require.Contains(t, without, "severity", "the base payload must carry the seeded key")
	require.NotContains(t, without, "account", "the base payload must not carry an account key at all")

	assert.Equal(t, without, with,
		"query(mode:\"metadata_stats\", format:\"json\") must be byte-identical with the ignored "+
			"`account` parameter set")
}

// TestQueryArm_TopologyRequestIsIdenticalWithAccount is measurement (b): the
// foundation.Request the analyzer is handed, captured through the probe analyzer
// this package already registers, plus the rendered result.
//
// The defect it reds on: topologyInstanceName resolved `account` into
// Request.Name, so the analyzer fetched a DIFFERENT graph instance — on
// graph:"knowledge" an instance that does not exist.
func TestQueryArm_TopologyRequestIsIdenticalWithAccount(t *testing.T) {
	drive := func(extra string) (string, string) {
		qpLastTopologyRequest = foundationRequestZero()
		deps := &repoTestDeps{rootDir: t.TempDir(), gc: &fakeGraphCaller{}}
		handled, res := InterceptTopology(context.Background(), deps, kgtools.CallToolParams{
			Name: "query",
			Arguments: json.RawMessage(
				`{"mode":"topology","algorithm":"` + qpTopologyAnalyzer + `","graph":"knowledge"` + extra + `}`),
		})
		require.True(t, handled, "the topology arm must claim the call")
		require.False(t, res.IsError, "the arm must serve, not refuse: %s", toolResultText(res))
		return qpLastTopologyRequest.Name, toolResultText(res)
	}

	nameWithout, bodyWithout := drive("")
	nameWith, bodyWith := drive(`,"account":"` + accountProbeValue + `"`)

	// KNOWN POSITIVE for the capture itself: RepoRoot is a different routed field
	// on the same Request literal, so its arrival proves the probe observed a live
	// dispatch rather than a zero value from a call that never reached the
	// analyzer. Without it, "both names are empty" would also be satisfied by two
	// dispatches that never happened.
	require.NotEmpty(t, qpLastTopologyRequest.RepoRoot,
		"the probe must have captured a live foundation.Request")

	assert.Equal(t, nameWithout, nameWith,
		"the analyzer must be handed the SAME graph instance with and without `account`; "+
			"it got %q without and %q with", nameWithout, nameWith)
	assert.Empty(t, nameWith,
		"on graph:\"knowledge\" the instance is the single one (empty) — an account value must "+
			"never become the instance name")
	assert.Equal(t, bodyWithout, bodyWith,
		"query(mode:\"topology\") must render identically with the ignored `account` parameter set")
}

// TestQueryArm_PivotHydrateInstanceIsIdenticalWithAccount is measurement (c):
// the GraphInstance stamped on every hydrated seed-search row for the pivot and
// correlations arms.
//
// The defect it reds on: pivotHydrateSelector copied `account` into the hydrate
// selector, and hydrateSelectorInstance read that field AHEAD of Name — so the
// caller's ignored parameter replaced the instance the rows actually came from.
func TestQueryArm_PivotHydrateInstanceIsIdenticalWithAccount(t *testing.T) {
	for _, tc := range []struct {
		name string
		base queryArgs
		want string
	}{
		{"registered_custom_by_name", queryArgs{Graph: "hellograph", Name: "hellograph"}, "hellograph"},
		{"code_by_repo", queryArgs{Graph: "code", Repo: "knowledge"}, "knowledge"},
		{"knowledge_singleton", queryArgs{Graph: "knowledge"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			without := hydrateSelectorInstance(pivotHydrateSelector(tc.base))

			withArgs := tc.base
			withArgs.Account = accountProbeValue
			with := hydrateSelectorInstance(pivotHydrateSelector(withArgs))

			// KNOWN POSITIVE: the base instance is the one the row's own selector
			// names, so an equal pair is two correct answers rather than two empties.
			require.Equalf(t, tc.want, without,
				"the base instance for %s must be the selector's own, or this pair proves nothing", tc.name)

			assert.Equalf(t, without, with,
				"%s must stamp the SAME GraphInstance with and without `account`; it stamped %q with it",
				tc.name, with)
		})
	}
}

// TestQueryArms_NoClientSideAccountRead is the CENSUS that generalizes the three
// rows above, and it is the part that stops the class returning.
//
// WHY A CENSUS AND NOT THREE MORE PAIRS. The three defects were found one at a
// time by reading arms; the remaining consuming arms pass a result pair only
// because their value goes onto the wire GraphSelector and the SERVER ignores it,
// which no client-side pair can distinguish from a client that reads it. The
// enumerable property is the one that matters: in the client's query path, the
// ONLY thing that may read the account argument is a wire-target builder. Any
// other read is a client-side decision, which is the defect class.
//
// It parses production source rather than matching text, so a read spelled
// across a line break or inside a composite literal is still seen.
func TestQueryArms_NoClientSideAccountRead(t *testing.T) {
	// The allowlist: sites whose read puts the value on the WIRE GraphSelector and
	// nothing else. Each entry is a function name in the named package directory.
	// A read anywhere else is reported.
	// THE KEYS ARE RELATIVE TO THIS PACKAGE'S DIRECTORY, which is `go test`'s
	// working directory. Both directories are inside the cmd/knowledge module, so
	// the test cache tracks the files this census opens and a source edit
	// re-runs it; a path reaching outside the module would be invisible to the
	// cache and this census would report a stale pass.
	allowed := map[string]map[string]bool{
		".": {
			"domainTarget": true, // the composite-mode arms' wire selector
		},
		// EVERY ENGINE ENTRY BELOW READS a.Account FOR EXACTLY ONE THING: it is
		// the third positional argument of buildTarget, the wire GraphSelector.
		// Each was opened and read rather than inferred from its name.
		"../engine": {
			"buildTarget":            true, // the wire selector itself
			"compileQuery":           true,
			"compileSearch":          true,
			"compileTraverse":        true,
			"dispatchQueryByID":      true,
			"dispatchGraphWideEdges": true,
			"resolveTraverseArgs":    true,
		},
	}

	var offenders []string
	for pkgDir, allowedFuncs := range allowed {
		fset := token.NewFileSet()
		entries, err := os.ReadDir(pkgDir)
		require.NoErrorf(t, err, "reading %s", pkgDir)

		scanned := 0
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			path := filepath.Join(pkgDir, e.Name())
			file, perr := parser.ParseFile(fset, path, nil, 0)
			require.NoErrorf(t, perr, "parsing %s", path)
			scanned++

			ast.Inspect(file, func(n ast.Node) bool {
				fn, ok := n.(*ast.FuncDecl)
				if !ok {
					return true
				}
				if allowedFuncs[fn.Name.Name] {
					return false
				}
				ast.Inspect(fn.Body, func(inner ast.Node) bool {
					sel, ok := inner.(*ast.SelectorExpr)
					if !ok || sel.Sel.Name != "Account" {
						return true
					}
					offenders = append(offenders,
						path+":"+fset.Position(sel.Pos()).String()[len(path)+1:]+" in "+fn.Name.Name)
					return true
				})
				return false
			})
		}
		// A directory that parsed no files would report a clean census having read
		// nothing, which is the failure this guard exists to make impossible.
		require.NotZerof(t, scanned, "no production files scanned in %s — the census read nothing", pkgDir)
	}

	sort.Strings(offenders)
	assert.Emptyf(t, offenders, "these client-side sites READ the account argument: %v\n\n"+
		"The `account` tool parameter is accepted and ignored by owner ruling, so the only client site "+
		"that may read it is a wire-target builder, whose value reaches a server gate that ignores it. "+
		"A read anywhere else is a CLIENT decision, and it changes the caller's answer — the exact defect "+
		"that shipped in topologyInstanceName, MetadataStatsJSONPayload and pivotHydrateSelector. "+
		"Remove the read; do not add the site to the allowlist unless it is a wire GraphSelector field.",
		offenders)
}

// TestQueryArmRegistry_AccountConsumedOnlyByWireTargetArms closes the OTHER
// direction of the census above: a consumed CELL that no production code reads.
//
// THE CENSUS CANNOT SEE THIS, correctly and by construction. It enumerates
// client-side READS of the account argument; a consumed cell naming a param
// nothing reads is the opposite shape, a declaration with no read. Measured: put
// `account` back into armTopology's consumed set at this commit, change no
// production read, and every test in this package still passes — because the
// registry test admits classConsumed as satisfying the ruling and its
// consuming-count floor is already met by the arms that genuinely build a wire
// Target.
//
// WHY THE CELL IS WORTH ASSERTING THOUGH IT GATES NOTHING. A consumed cell is
// the CLAIM that made the topologyInstanceName read look legitimate for a whole
// commit: the arm declared it routed the value, so the value being read looked
// like routing. Both admissible classes pass the param through, so a wrong cell
// changes no caller-visible answer today — it changes what the next reader
// believes, which is how the first defect survived review.
//
// IT PINS THE CLASS RATHER THAN THE ONE ARM. The permitted set below is the same
// distinction the census allowlist draws: an arm may declare `account` consumed
// only if its handler puts the value on a wire GraphSelector, where the server's
// selector gate ignores it. armTopology is absent because it builds no wire
// selector at all — it hands the analyzer a foundation.Request, which has no
// Account field and never did.
func TestQueryArmRegistry_AccountConsumedOnlyByWireTargetArms(t *testing.T) {
	// The arms whose handler reaches a wire-target builder: the composite modes
	// and metadata_stats through domainTarget, and the engine dispatch through
	// engine.buildTarget. Each was confirmed by opening its handler, not by its
	// name.
	wireTargetArms := map[armID]bool{
		armCorrelations:   true, // domainTarget
		armPivot:          true, // domainTarget
		armExplain:        true, // domainTarget
		armTimeline:       true, // domainTarget
		armMetadataStats:  true, // domainTarget, on the MetadataStatsRequest
		armEngineDispatch: true, // engine.buildTarget
	}

	var claimed []armID
	for _, arm := range sortedArmIDs() {
		if !queryArmRegistry[arm].consumed["account"] {
			continue
		}
		claimed = append(claimed, arm)
		assert.Truef(t, wireTargetArms[arm],
			"arm %s declares `account` CONSUMED, but its handler builds no wire GraphSelector — the "+
				"cell claims a routing that does not exist, and that claim is what made the "+
				"topologyInstanceName read look legitimate. Either the arm routes the value to the "+
				"server (add it here, having opened the handler) or the cell belongs in the ruling's "+
				"ignored class", arm)
	}

	// KNOWN POSITIVE in both directions. An empty claimed set would satisfy the
	// loop above while meaning the ruling's applier had swallowed every cell, and
	// a stale entry in the permitted table would never be noticed by a loop that
	// only walks the registry.
	require.NotEmpty(t, claimed,
		"no arm consumes `account` — the arms that put it on the wire Target must still say so")
	for arm := range wireTargetArms {
		assert.Truef(t, queryArmRegistry[arm].consumed["account"],
			"%s is listed here as a wire-target arm but no longer declares `account` consumed — "+
				"remove the stale entry rather than leaving the table describing a routing that ended", arm)
	}
}

// foundationRequestZero returns the zero Request the topology probe is reset to
// between the two halves of a pair, so a stale capture from the first call can
// never be read as the second call's.
func foundationRequestZero() (zero foundation.Request) { return zero }
