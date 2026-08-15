// SPDX-License-Identifier: Apache-2.0

package codesync

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/parser"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestAugmentPreciseCallGraph_CoverageAndSurvival drives the full Populate +
// RTA-merge path over a fixture whose Go code deliberately spans three coverage
// classes, and asserts that no class loses its call edges.
//
// WHY THIS SEAM. The test calls augmentWithPreciseCallGraph, whose signature is
// unchanged by the coverage and drop-gate fixes. A reproduction only fails
// observably if it COMPILES against the unfixed tree, and BuildGoCallGraph's
// signature does change — so the reproduction is written here and never against
// BuildGoCallGraph.
//
// The four legs, and why each one is present:
//
//	(A) CONTROL. The root module's own edge, green before and after. Without it
//	    an empty edge set would satisfy the two red legs vacuously.
//	(B) COVERAGE. An edge inside a NESTED module with no go.work. Red before the
//	    per-module load: a single `./...` from the root never reaches it.
//	(C) SURVIVAL. An edge out of a build-tag-excluded file. tree-sitter indexes
//	    that file; go/packages excludes it under every build configuration, so it
//	    is permanently uncoverable. This is the ONLY leg that distinguishes
//	    "coverage fixed" from "coverage fixed AND the kill made conditional".
//	(D) BUILTIN HONESTY. A CHARACTERIZATION GUARD, green before and after — it is
//	    not red-first and does not claim to be. It locks in the ticket's
//	    constraint, quoted verbatim: "a builtin call that VTA cannot type-resolve
//	    to a real target produces NO edge (honest absence) — never a name-keyed
//	    fallback bind", and "Go builtin shadowing (a package legally declaring
//	    func append) must still resolve to the real declaration when the types say
//	    so". SSA models a builtin as *ssa.Builtin and never as an *ssa.Function, so
//	    no callgraph node exists for one; there is no heuristic here to remove.
//
// Each assertion is its own t.Errorf so a failure names the violated property
// instead of dumping a set diff. The post-merge edge set is printed as
// `COVCALLS <from> -> <to>` lines at column zero on both pass and fail;
// testdata/ful1348_redfirst.txt is a frozen capture of that output from before
// the fix.
func TestAugmentPreciseCallGraph_CoverageAndSurvival(t *testing.T) {
	root := writeCoverageFixture(t)

	pop, err := parser.Populate(t.Context(), "cov", root)
	require.NoError(t, err)

	out := augmentWithPreciseCallGraph(t.Context(), pop, root)

	edges := make([]string, 0, len(out.Edges))
	for _, e := range out.Edges {
		if kgtypes.EdgeType(e.Type) != kgtypes.EdgeCalls {
			continue
		}
		edges = append(edges, e.FromId+" -> "+e.ToId)
	}
	slices.Sort(edges)
	for _, line := range edges {
		fmt.Printf("COVCALLS %s\n", line)
	}

	// (A) CONTROL — the module the analysis has always covered.
	if !slices.Contains(edges, "top/top.go:TopA -> top/top.go:topB") {
		t.Errorf("want: the root-module CALLS edge top/top.go:TopA -> top/top.go:topB; " +
			"got: absent — not even the covered module produced an edge, so this run is vacuous")
	}

	// (B) COVERAGE — the nested module.
	if !slices.Contains(edges, "sub/in/in.go:InA -> sub/in/in.go:inB") {
		t.Errorf("want: a precise CALLS edge inside the nested module sub/; got: absent — the analysis did not cover that module")
	}

	// (C) SURVIVAL — the permanently uncoverable file.
	if !slices.Contains(edges, "top/tag.go:TagA -> top/tag.go:tagB") {
		t.Errorf("want: the tree-sitter CALLS edge from a build-tag-excluded Go file preserved; got: deleted with nothing to replace it")
	}

	// (D) BUILTIN HONESTY — characterization guard, not a red-first leg.
	for _, line := range edges {
		from, to, ok := strings.Cut(line, " -> ")
		if !ok || to != "web/dom.go:walker.append" {
			continue
		}
		if !strings.HasPrefix(from, "web/dom.go:") {
			t.Errorf("want: no CALLS edge into the walker.append METHOD from outside the file "+
				"that declares it, since a builtin append call must produce honest absence; "+
				"got: caller %s bound to it", from)
		}
	}
}

// writeCoverageFixture writes the three-coverage-class fixture and returns its
// root. Every path and identifier here is load-bearing: the criteria on this
// step and on the drop-gate step grep the resulting edge strings.
//
//	top/top.go  root module, ordinary coverage. TopA calls the BUILTIN append
//	            and the in-package topB — the control edge plus the builtin
//	            caller leg (D) needs.
//	top/tag.go  //go:build neverbuilt. tree-sitter indexes it, go/packages
//	            excludes it under every build configuration: the permanently
//	            uncovered class.
//	web/dom.go  the ticket's own shape — a `walker` type with an `append`
//	            METHOD, declared in a different package from the builtin caller.
//	sub/        a NESTED module with NO go.work. Executed against the unfixed
//	            loader, packages.Load(Dir=root, "./...", "./sub/...") returns a
//	            synthetic error package reading `pattern ./sub/...: directory
//	            prefix sub does not contain main module or its selected
//	            dependencies` — which is why a multi-pattern single Load cannot
//	            substitute for the per-module loop, and why this leg is here.
func writeCoverageFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	writePopulateFixtureFile(t, filepath.Join(root, "go.mod"), "module example.com/outer\n\ngo 1.24\n")

	writePopulateFixtureFile(t, filepath.Join(root, "top", "top.go"), `package top

func TopA(xs []int) []int {
	return append(xs, topB())
}

func topB() int {
	return 1
}
`)

	writePopulateFixtureFile(t, filepath.Join(root, "top", "tag.go"), `//go:build neverbuilt

package top

func TagA() int {
	return tagB()
}

func tagB() int {
	return 2
}
`)

	writePopulateFixtureFile(t, filepath.Join(root, "web", "dom.go"), `package web

type walker struct{ out []int }

func (w *walker) append(x int) {
	w.out = append(w.out, x)
}

func Walk(n int) []int {
	w := &walker{}
	w.append(n)
	return w.out
}
`)

	writePopulateFixtureFile(t, filepath.Join(root, "sub", "go.mod"), "module example.com/inner\n\ngo 1.24\n")

	writePopulateFixtureFile(t, filepath.Join(root, "sub", "in", "in.go"), `package in

func InA() int {
	return inB()
}

func inB() int {
	return 3
}
`)

	return root
}
