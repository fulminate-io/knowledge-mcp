// SPDX-License-Identifier: Apache-2.0

package corpus

import (
	"context"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// fence_placement_check_test.go — THE CHECKED-IN SOURCE of the TestMain-trap
// corpus check, and its admission gate, run in this process against no daemon.
//
// WHY THE SOURCE IS HERE. The check node itself lives in the checks graph and is
// rewritten there after this branch merges; a check node is not a file, so
// without this the only record of what the check SAYS would be the graph, and a
// reviewer auditing the fixture pair would have to reach a daemon to see it.
// These constants are the authority the post-merge rewrite is made from, and
// ValidateFixtures below is the same admission gate the write path runs, so a
// change to the pattern or either fixture is judged here first.
//
// THE DEFECT THE CHECK CLOSES. `go test` records a test's opened files only
// after it parses -test.testlogfile, which happens inside m.Run. A test-cache
// fence placed in TestMain therefore opens its files OUTSIDE the recording
// window and reaches no cache key at all, while looking in the diff and in a
// green run exactly like a fence that works. That is not hypothetical: the
// server-bench fence was first written in TestMain and its four-run proof
// returned `ok (cached)` against a deliberately broken subject.
//
// WHY THE LEAF MATCHES A NAME PREFIX RATHER THAN ONE NAME, which is the whole
// content of this file's revision. The first form of this check keyed on the
// single identifier fenceTestCacheOnBuiltBinarySources, so it covered the
// server-bench fence and was SILENT on the three fences that followed it —
// fenceTestCacheOnClientSources in cmd/frontend and fenceTestCacheOnServerTree
// in two collector packages — each of which carried the requirement in a doc
// comment and nothing else. Measured over a planted violation: the single-name
// leaf returned total 0 while the leaf below returned total 1 naming the file.
// A structural requirement carried in prose is the thing GOVERNANCE forbids, so
// the leaf matches the naming convention instead of one member of it.
const (
	// fencePlacementPattern selects every TestMain declaration.
	fencePlacementPattern = `func TestMain($$$P) { $$$B }`

	// fencePlacementWhere fires when that TestMain contains a call whose callee
	// name begins with fenceTestCache. The callee is CAPTURED and then matched by
	// regex, which is what makes the leaf cover a convention rather than a name.
	fencePlacementWhere = `{"contains_pattern":{"of":"$match","pattern":"$FN($$$_)","where":{"matches":{"of":"FN","regex":"^fenceTestCache"}}}}`
)

// THE FIXTURE PAIR varies exactly one axis: WHERE the fence is called. Both
// declare a TestMain, both call a fence, and the good one is a genuine near-miss
// — a TestMain doing real setup with the fence at a test-scoped site in the same
// file. A check keying on "a TestMain exists" or "this file calls a fence" fires
// on both, so the pair discriminates.
const (
	fencePlacementBad = `package bench

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	fenceTestCacheOnBuiltBinarySources(nil)
	os.Exit(m.Run())
}
`

	fencePlacementGood = `package bench

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	prepareHarnessDirs()
	os.Exit(m.Run())
}

func TestSpawnOSSHealthCheck(t *testing.T) {
	fenceTestCacheOnBuiltBinarySources(t)
	spawnAndCheck(t)
}
`
)

// fencePlacementCheck is the check as it must be written into the checks graph.
func fencePlacementCheck(where string) Check {
	return Check{
		ID:          "fence-placement",
		Type:        CheckAstPattern,
		Severity:    foundation.SeverityCritical,
		Language:    treesitter.LangGo,
		Pattern:     fencePlacementPattern,
		Where:       []byte(where),
		FixtureBad:  "bad-node",
		FixtureGood: "good-node",
	}
}

// TestFencePlacementCheck_IsAdmitted is the admission gate, run in process: the
// check must FIRE on the bad fixture and be SILENT on the good one. This is the
// same ValidateFixtures the write path runs, so a change to the pattern, the
// leaf or either fixture is judged here before it reaches any graph.
func TestFencePlacementCheck_IsAdmitted(t *testing.T) {
	err := ValidateFixtures(context.Background(), fencePlacementCheck(fencePlacementWhere),
		Fixture{ID: "bad-node", Content: fencePlacementBad},
		Fixture{ID: "good-node", Content: fencePlacementGood})
	if err != nil {
		t.Fatalf("the fence-placement check must be admitted by its own fixture pair: %v", err)
	}
}

// everyFenceName is every fence function this repository declares. A fence added
// under the same convention and NOT added here is caught by the check itself,
// which matches the prefix; this list is what proves the check covers the ones
// that exist rather than only the one it was written for.
var everyFenceName = []string{
	"fenceTestCacheOnBuiltBinarySources", // cmd/server-bench/internal/bench
	"fenceTestCacheOnBuildInputs",        // cmd/server-bench/internal/bench
	"fenceTestCacheOnClientSources",      // cmd/frontend
	"fenceTestCacheOnServerTree",         // collector/web and collector/treesitter
}

// TestFencePlacementCheck_CoversEveryFenceName is the widening's proof, and it
// is the row the single-name leaf could not pass. For each fence name in the
// repository, a TestMain calling THAT fence must be refused and the near-miss
// with the same name at a test-scoped site must be admitted.
func TestFencePlacementCheck_CoversEveryFenceName(t *testing.T) {
	for _, name := range everyFenceName {
		t.Run(name, func(t *testing.T) {
			bad := strings.ReplaceAll(fencePlacementBad, "fenceTestCacheOnBuiltBinarySources", name)
			good := strings.ReplaceAll(fencePlacementGood, "fenceTestCacheOnBuiltBinarySources", name)
			if err := ValidateFixtures(context.Background(), fencePlacementCheck(fencePlacementWhere),
				Fixture{ID: "bad-node", Content: bad},
				Fixture{ID: "good-node", Content: good}); err != nil {
				t.Fatalf("the check must fire on a TestMain-placed %s and stay silent on the test-scoped one: %v", name, err)
			}
		})
	}
}

// TestFencePlacementCheck_SingleNameLeafIsBlind is the RED, kept as a test so
// the widening cannot be quietly reverted. The leaf this check shipped with
// keyed on one identifier; against a TestMain calling any of the other three
// fences it is silent, and ValidateFixtures reports that silence as a refusal to
// admit — which is exactly the state the three fences were in.
func TestFencePlacementCheck_SingleNameLeafIsBlind(t *testing.T) {
	const singleNameWhere = `{"contains_pattern":{"of":"$match","pattern":"fenceTestCacheOnBuiltBinarySources($$$_)"}}`

	// The control, same run: on the fence it WAS written for, the old leaf is
	// admitted. Without this the refusals below would be satisfied by a leaf that
	// is broken in every direction.
	if err := ValidateFixtures(context.Background(), fencePlacementCheck(singleNameWhere),
		Fixture{ID: "bad-node", Content: fencePlacementBad},
		Fixture{ID: "good-node", Content: fencePlacementGood}); err != nil {
		t.Fatalf("control: the single-name leaf must still be admitted on its own fence: %v", err)
	}

	for _, name := range everyFenceName[1:] {
		t.Run(name, func(t *testing.T) {
			bad := strings.ReplaceAll(fencePlacementBad, "fenceTestCacheOnBuiltBinarySources", name)
			good := strings.ReplaceAll(fencePlacementGood, "fenceTestCacheOnBuiltBinarySources", name)
			err := ValidateFixtures(context.Background(), fencePlacementCheck(singleNameWhere),
				Fixture{ID: "bad-node", Content: bad},
				Fixture{ID: "good-node", Content: good})
			if err == nil {
				t.Fatalf("the single-name leaf must be SILENT on a TestMain-placed %s; if it is not, this red no longer describes the defect the widened leaf fixes", name)
			}
		})
	}
}
