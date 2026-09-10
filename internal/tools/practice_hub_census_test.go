// SPDX-License-Identifier: Apache-2.0

package tools

// practice_hub_census_test.go holds the module-wide census of mutate compile
// sites that TestPracticeHubArms_EveryRouteReachesTheEngineRules asserts against.
//
// WHY IT IS MODULE-WIDE. The engine-layer hub rules run beneath the one funnel in
// this package. A route that bypasses them is not likely to be written next to
// the funnel; it is likely to be written in a sibling package that wants a plan
// of its own. A census that reads only this package's directory cannot see that,
// and would stay green through exactly the change it exists to catch.
//
// WHAT A NEW SITE COSTS ITS AUTHOR. One line here, carrying the reason the site
// does not reach a practice hub — or a route through the funnel instead. That is
// the whole point: the reason is written down once, by the person who knows it,
// rather than re-derived by every later reader of the write path.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// practiceCompileSite is one censused site: the module-relative file, how many
// times it compiles a mutate plan, and why that is sound.
type practiceCompileSite struct {
	path   string
	sites  int
	reason string
}

// practiceCompileSiteCensus is every place in this module that compiles a mutate
// plan: the guarded funnel first, then the siblings that do not reach a practice
// hub, each with the reason it does not.
//
// A SIBLING'S REASON IS A CLAIM ABOUT ITS PAYLOAD, not about its caller's good
// manners. Two things make the hub rules apply: a hub selector on the call
// (`source_hub`, or `source` on the delete tool), and a practice `source` body
// that must be self-keyed. A sibling that acquires either owes a route through
// the funnel, and the equality below is what makes it say so.
func practiceCompileSiteCensus() map[string]int {
	sites := []practiceCompileSite{
		{"internal/tools/wire_persist.go", 1,
			"THE GUARDED FUNNEL. executeMutate runs the engine-layer hub rules before it " +
				"compiles, so every tool arm that reaches a write reaches them first."},
		{"internal/crossgraph/proxy.go", 1,
			"Upserts ONE cross-graph proxy built by the shared builder: its type is proxy, " +
				"never source, and the args it marshals carry no hub selector."},
		{"internal/graphtypecrud/client.go", 2,
			"Upserts and by-id-deletes a graph_type_def node on the knowledge graph. It " +
				"names no graph selector and no hub, and graph_type_def is not a hub type."},
		{"internal/pipeline/rpc.go", 1,
			"An update_batch of summaries and vectors onto nodes that already exist. It " +
				"creates no node, so no body can need a self-key, and it carries no hub."},
		{"internal/postpopulate/wire.go", 1,
			"The collector post-population create_batch. Its graph comes from the graph " +
				"type being collected, its bodies are collector nodes, and it carries no hub."},
		{"internal/postpopulate/wire_edges.go", 1,
			"Unlinks ONE edge by (from, to, relationship). It creates nothing and carries " +
				"no hub selector."},
		{"internal/projects/render/test_plan.go", 1,
			"Creates the pending test_run nodes of one run session on the knowledge graph. " +
				"test_run is not a hub type and the payload carries no hub."},
		{"internal/thought/wire.go", 1,
			"The reflection writeback: a bulk metadata update on existing knowledge-graph " +
				"nodes, marked reflect-inert on the compiled plan. It creates no node."},
	}
	out := make(map[string]int, len(sites))
	for _, s := range sites {
		out[s.path] = s.sites
	}
	return out
}

// practiceMutateCompileSitesFromTree derives the same shape from the source
// tree, walking from the MODULE ROOT so a sibling package is in scope.
//
// IT PARSES RATHER THAN GREPS. The first draft counted the call as text and
// reported two sites in a file that has one, because a doc comment there quotes
// the call it describes — a census that counts prose is a census whose numbers
// have to be argued with, and this one is compared for equality.
func practiceMutateCompileSitesFromTree(t *testing.T) map[string]int {
	t.Helper()
	root := goModuleRoot(t)

	out := map[string]int{}
	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		n, countErr := countCompileMutateCalls(path)
		if countErr != nil {
			return countErr
		}
		if n == 0 {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		out[filepath.ToSlash(rel)] = n
		return nil
	}))
	require.NotEmpty(t, out,
		"the walk found no compiling site at all, so the comparison it feeds would be vacuous")
	return out
}

// countCompileMutateCalls counts the engine.Compile("mutate", …) CALL EXPRESSIONS
// in one file, ignoring comments and any other spelling of the words.
func countCompileMutateCalls(path string) (int, error) {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		return 0, err
	}
	n := 0
	ast.Inspect(file, func(node ast.Node) bool {
		call, isCall := node.(*ast.CallExpr)
		if !isCall || len(call.Args) == 0 {
			return true
		}
		sel, isSel := call.Fun.(*ast.SelectorExpr)
		if !isSel || sel.Sel.Name != "Compile" {
			return true
		}
		pkg, isIdent := sel.X.(*ast.Ident)
		if !isIdent || pkg.Name != "engine" {
			return true
		}
		lit, isLit := call.Args[0].(*ast.BasicLit)
		if isLit && lit.Kind == token.STRING && lit.Value == `"mutate"` {
			n++
		}
		return true
	})
	return n, nil
}

// goModuleRoot finds the module the test binary was built from by walking up for
// a go.mod, because a test's working directory is its own package's directory
// and the census is about the whole module.
func goModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, dir, parent,
			"walked to the filesystem root without finding a go.mod, so the census read nothing")
		dir = parent
	}
}
