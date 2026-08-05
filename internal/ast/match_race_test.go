// SPDX-License-Identifier: Apache-2.0

// match_race_test.go — concurrency regression test for the ast worker
// pool. Before the per-worker-compile fix, every worker goroutine shared
// ONE compiled pattern tree, ONE sub-pattern cache (a map from sub-pattern
// source to its compile, plus the mutex), and the root query, and walked the
// shared pattern tree concurrently via go-tree-sitter NamedChild ->
// (*Tree).cachedNode (an unsynchronized per-Tree map) -> fatal
// "concurrent map read and map write".
//
// Reproduction hinges on a fully-LITERAL pattern (no placeholders): the
// compile-time placeholder walk (indexPlaceholders) short-circuits when a
// pattern has zero placeholders, so it does NOT warm the pattern tree's
// cachedNode map. With a cold cache, every worker populates the shared map
// as it walks — concurrent read+write. A pattern WITH placeholders warms
// the cache single-threaded at Compile, hiding the race. The original
// crash trigger (`StartPostgresContainer(t)`) was exactly such a literal
// pattern.
//
// The proof is `go test -race`: a plain (non-race) run may PASS with the
// bug present because the racing map access only sometimes corrupts. Run
//
//	go test -race -run TestMatch_ConcurrentSubPattern_NoRace ./cmd/knowledge/internal/ast/
//
// to exercise it. Pre-fix it reports a DATA RACE whose stack names
// go-tree-sitter cachedNode / NamedChild and the walker's child-list
// helper (ast.allChildren); post-fix it passes clean.

package ast

import (
	"context"
	"fmt"
	"runtime"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

func TestMatch_ConcurrentSubPattern_NoRace(t *testing.T) {
	// The race needs >1 CPU so the worker pool actually runs goroutines in
	// parallel over the shared tree. On a single-core box there is no
	// concurrent map access to detect.
	if runtime.NumCPU() < 2 {
		t.Skip("race needs >1 CPU")
	}

	// MANY more files than cores keeps fileCh full so every worker stays
	// busy walking the shared pattern tree simultaneously for the whole run
	// (one-file-per-worker drains too fast to collide). Each file is its own
	// package (subdir) so the literal function name raceTarget can repeat
	// across files — letting the WHOLE multi-statement function be the
	// literal pattern (a wide cold-cache walk), not just a small statement.
	numFiles := 8 * runtime.NumCPU()

	contents := make(map[string]string, numFiles)
	for i := range numFiles {
		contents[fmt.Sprintf("p%d/src.go", i)] = raceFileSrc
	}
	dir := fixtureRepo(t, contents)

	// A LITERAL pattern (no $ placeholders): indexPlaceholders skips the
	// cache-warming walk for zero-placeholder patterns, so cp.Tree's
	// cachedNode map is COLD when the worker pool starts. Every worker then
	// populates it concurrently as it walks — the pre-fix race. The pattern
	// is multi-statement so the cold-cache walk spans many distinct nodes,
	// widening the collision window.
	pat, err := Parse(racePattern)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	cp, err := Compile(pat, treesitter.LangGo, "")
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	defer cp.Close()

	// A non-nil where-tree with contains_pattern exercises the second
	// (formerly shared) tree class — the sub-pattern cache + its trees —
	// alongside the cold main-tree walk. of:"$match" is the matched
	// function node; every match's body contains `x.Close()`, so the
	// contains_pattern holds.
	where, err := ParseWhere([]byte(`{"contains_pattern": {"of": "$match", "pattern": "x.Close()"}}`))
	if err != nil {
		t.Fatalf("ParseWhere: %v", err)
	}

	// Equivalence guard: each of the numFiles files has exactly one
	// raceTarget function matching the literal pattern once, and its body
	// contains x.Close() (so contains_pattern holds). This is the
	// single-threaded ground truth; pinning it makes the test fail loudly
	// if a per-worker compile ever diverges from the shared-cp results, not
	// only if -race trips.
	want := numFiles

	// Loop a handful of Match calls to widen the timing window — the race
	// is nondeterministic per call; repeated runs over the shared cp
	// multiply the chance two workers hit cachedNode concurrently. Each
	// call re-runs the full NumCPU worker pool over the shared pattern.
	for iter := range 30 {
		raws, _, err := Match(context.Background(), dir, treesitter.LangGo, cp, where, Scope{})
		if err != nil {
			t.Fatalf("Match (iter %d): %v", iter, err)
		}
		if len(raws) != want {
			t.Fatalf("matches (iter %d) = %d, want %d", iter, len(raws), want)
		}
	}
}

// racePattern is a fully-LITERAL DSL pattern (no $ placeholders). Because
// it has zero placeholders, Compile's indexPlaceholders walk is skipped and
// cp.Tree's go-tree-sitter cachedNode cache is NOT pre-warmed — so the
// worker pool populates it concurrently, the pre-fix data race. It is
// multi-statement so the cold-cache walk spans many distinct nodes.
const racePattern = `func raceTarget() {
	var x C
	g(1)
	g(2)
	g(3)
	defer x.Close()
}`

// raceFileSrc is one fixture file: its own package so the raceTarget name
// can repeat across all files, with the body byte-identical to racePattern
// so each file yields exactly one match.
const raceFileSrc = `package p

type C struct{}

func (C) Close() error { return nil }
func g(int)            {}

func raceTarget() {
	var x C
	g(1)
	g(2)
	g(3)
	defer x.Close()
}
`
