// SPDX-License-Identifier: Apache-2.0

// acceptance_test_wait_helper_test.go — the acceptance case for a check whose
// defect class exists ONLY in test files.
//
// A test wait helper whose deadline arm RETURNS instead of failing the test
// turns a timeout into a pass: the helper gives up, the caller proceeds, and the
// assertion that follows measures a state that never settled. Every real
// instance of that class is in a _test.go file, which is precisely why the check
// could pass its fixtures and then never fire on real source — the corpus walk
// could not see test files at all.
//
// THE CHECK BODY HERE IS THE REAL ONE, transcribed from the check node's stored
// dsl_pattern and check_where. The four shapes are transcribed from real helpers
// in this repository: two that return on the deadline, and two near-misses that
// fail the test instead. Inventing simplified shapes would test the harness
// rather than the check.

package corpusscan

import (
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/corpus"
)

// waitHelperPattern and waitHelperWhere are the check's stored body, verbatim.
const waitHelperPattern = "for time.Now().Before($D) { $$$B }"

// THE RETURN-TYPE SEQUENCE CAPTURE IS LOAD-BEARING. Helpers of this class come
// in both shapes — one returns a value on the deadline, one just returns — and
// an inside_pattern written without $$$RT matches only the result-less form.
const waitHelperWhere = `{"all":[{"inside_pattern":{"of":"$match","pattern":"func $N($$$P) $$$RT { $$$FB }","as":"FN"}},` +
	`{"not":{"contains_pattern":{"of":"FN","pattern":"$T.Fatalf($$$_)"}}},` +
	`{"not":{"contains_pattern":{"of":"FN","pattern":"$T.Fatal($$$_)"}}},` +
	`{"not":{"contains_pattern":{"of":"FN","pattern":"$T.Errorf($$$_)"}}},` +
	`{"not":{"contains_pattern":{"of":"FN","pattern":"$T.Error($$$_)"}}},` +
	`{"not":{"contains_pattern":{"of":"FN","pattern":"require.$M($$$_)"}}}]}`

// The two REAL instances, one of each shape.
const settledMergerCountSource = `package segmentdist

import "time"

func settledMergerCount(within time.Duration) int {
	deadline := time.Now().Add(within)
	prev := mergerGoroutines()
	for time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		got := mergerGoroutines()
		if got == prev {
			return got
		}
		prev = got
	}
	return prev
}
`

const waitMergeQuiesceWindowSource = `package segmentdist

import "time"

func waitMergeQuiesceWindow(mergeCount func() uint64, stableFor time.Duration) {
	last := mergeCount()
	stableStart := time.Now()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		cur := mergeCount()
		if cur != last {
			last = cur
			stableStart = time.Now()
			continue
		}
		if time.Since(stableStart) >= stableFor {
			return
		}
	}
}
`

// The two REAL near-misses: the same loop shape in a helper that FAILS the test
// on the deadline, and the same shape in a closure that reports an error.
const waitForMergeFatalSource = `package segmentdist

import (
	"testing"
	"time"
)

func waitForMerge(t *testing.T, mergeCount func() uint64, what string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if mergeCount() >= 1 {
			return
		}
		time.Sleep(3 * time.Millisecond)
	}
	t.Fatalf("%s: no merge ever fired within 30s (merge count still %d)", what, mergeCount())
}
`

const searchHammerErrorfSource = `package searchengine

import (
	"testing"
	"time"
)

func hammer(t *testing.T, e engine, valid map[string]bool) {
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		for _, h := range e.Search("x", 10) {
			if !valid[h.ID] {
				t.Errorf("Search returned unexpected id %q during merge", h.ID)
			}
		}
	}
}
`

// waitHelperCheckMeta is the real check's metadata, with the declaration set.
func waitHelperCheckMeta(declared bool) map[string]string {
	md := map[string]string{
		corpus.MetaCheckType:   string(corpus.CheckAstPattern),
		corpus.MetaSeverity:    "warning",
		corpus.MetaLanguage:    "go",
		corpus.MetaDSLPattern:  waitHelperPattern,
		corpus.MetaCheckWhere:  waitHelperWhere,
		corpus.MetaFixtureBad:  "fx-bad",
		corpus.MetaFixtureGood: "fx-good",
	}
	if declared {
		md[corpus.MetaAppliesToTests] = "true"
	}
	return md
}

// TestCorpusScan_TestWaitHelperCheckFiresOnItsRealInstances is the acceptance
// case: with the declaration the check reaches its real, test-file-only
// instances; without it the same check over the same tree is silent, which is
// the state every such check was in before the walk could see test files.
func TestCorpusScan_TestWaitHelperCheckFiresOnItsRealInstances(t *testing.T) {
	root := seedRepo(t, map[string]string{
		"segmentdist/manager_close_test.go":         settledMergerCountSource,
		"segmentdist/reclaim_test.go":               waitMergeQuiesceWindowSource,
		"segmentdist/manager_merge_reclaim_test.go": waitForMergeFatalSource,
		"searchengine/merge_test.go":                searchHammerErrorfSource,
	})

	corpusFor := func(declared bool) *fakeCaller {
		return newFakeCaller().seed("checks", []*knowledgev1.Node{
			checkNode("chk-wait-helper", "a test wait helper whose deadline arm returns instead of failing the test",
				"the helper gives up on the deadline and the caller proceeds, so the assertion that follows measures a state that never settled",
				waitHelperCheckMeta(declared)),
			exampleNode("fx-bad", settledMergerCountSource),
			exampleNode("fx-good", waitForMergeFatalSource),
		}, nil)
	}

	t.Run("undeclared: silent on every real instance", func(t *testing.T) {
		got := siteFiles(runScan(t, scanRequest(corpusFor(false), "repo", root)))
		if len(got) != 0 {
			t.Fatalf("the walk cannot see test files, so this check has no reachable instance; got %v", got)
		}
	})

	t.Run("declared: fires on both real instances and stays silent on both near-misses", func(t *testing.T) {
		got := siteFiles(runScan(t, scanRequest(corpusFor(true), "repo", root)))
		want := map[string]bool{
			"segmentdist/manager_close_test.go": true,
			"segmentdist/reclaim_test.go":       true,
		}
		if len(got) != len(want) {
			t.Fatalf("expected exactly the two helpers that return on the deadline, got %v", got)
		}
		for _, f := range got {
			if !want[f] {
				t.Errorf("flagged %s, which fails the test on its deadline and is a near-miss the check must not fire on", f)
			}
		}
	})

	t.Run("the run-wide knob reaches it too", func(t *testing.T) {
		// The same acceptance through the other control, so a check that has not
		// been declared is still reachable by a caller who asks for it.
		got := siteFiles(runScan(t, withIncludeTests(scanRequest(corpusFor(false), "repo", root), "true")))
		if len(got) != 2 {
			t.Fatalf("the run-wide knob must reach the same two instances, got %v", got)
		}
	})
}
