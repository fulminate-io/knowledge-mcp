// SPDX-License-Identifier: Apache-2.0

package codesync

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// TestBuildGoCallGraph runs BuildGoCallGraph on the knowledge repo itself and
// verifies four known caller→callee pairs, plus two structural invariants over
// the whole returned graph.
//
// This test is intentionally slow (SSA + VTA on the full repo) — run with
// -timeout 300s to allow sufficient time. Skipped in CI (no build cache
// makes SSA take 15+ minutes on a fresh runner), which is why the fast fixture
// gates below it exist rather than this being the only catcher.
func TestBuildGoCallGraph(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("SSA/RTA too slow on fresh CI runners without build cache")
	}
	// Locate the repo root by walking up from the test file until we find go.mod.
	// That go.mod is cmd/knowledge/go.mod, so every decl key below is relative to
	// cmd/knowledge and NOT to the repository root.
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	rootDir := testFile
	for {
		parent := filepath.Dir(rootDir)
		if parent == rootDir {
			t.Fatal("could not locate go.mod above test file")
		}
		rootDir = parent
		if _, err := os.Stat(filepath.Join(rootDir, "go.mod")); err == nil {
			break
		}
	}

	t.Logf("running BuildGoCallGraph on %s", rootDir)

	// rootDir IS the module root located above, so the module set is the single
	// entry "." — this test deliberately pins the decl-key namespace to
	// cmd/knowledge rather than exercising module discovery, which
	// TestAugmentPreciseCallGraph_CoverageAndSurvival covers.
	result, err := BuildGoCallGraph(t.Context(), rootDir, []string{"."})
	if err != nil {
		t.Fatalf("BuildGoCallGraph error: %v", err)
	}
	graph := result.CallMap
	if len(graph) == 0 {
		t.Fatal("BuildGoCallGraph returned empty graph")
	}
	t.Logf("total callers: %d", len(graph))

	// Known caller→callee pairs, written in the decl-key namespace:
	// "<file relative to cmd/knowledge>:<symbol>". ALL FOUR are required.
	//
	// The fourth is the method-caller leg. Its caller is a method, which was
	// impossible as a caller under the old package-members seeding, and its
	// callee is the very function this package's merge path is built around — so
	// the pair cannot rot without this package's own surface changing.
	//
	// The three method candidates this list used to carry were dropped because
	// they were written in the retired package-qualified namespace, NOT because
	// methods cannot be callers. That was true only under the old seeding, which
	// the repo-declared function set replaces.
	//
	// STANDING RULE, binding on any future re-derivation: no candidate endpoint
	// may name a symbol that in-flight parser work is scheduled to delete —
	// resolveEdgeID, resolveDottedEdgeCallee, extractCallerContext, nsLang,
	// ConvertEdges, recordSymbol. A pair naming parser/edges.go was removed for
	// exactly that reason: correct work deleting one of those symbols would have
	// turned a required candidate permanently red with no sanctioned repair.
	//
	// These pairs are TREE-DERIVED, not fixed by decree. If a later change moves
	// one of these declarations, RE-DERIVE the pair (run with -v and read the
	// logged endpoint sample) rather than deleting it, and keep the rule above.
	type pair struct {
		caller string
		callee string
	}
	candidates := []pair{
		{
			caller: "internal/collector/parser/populate.go:Populate",
			callee: "internal/collector/parser/indexer_discover.go:DiscoverFiles",
		},
		{
			caller: "internal/collector/parser/populate.go:Populate",
			callee: "internal/collector/parser/indexer_chunk.go:ChunkFilesParallel",
		},
		{
			caller: "internal/collector/parser/populate.go:appendChunkNode",
			callee: "internal/collector/parser/indexer_chunk.go:ChunkNodeID",
		},
		{
			caller: "internal/collector/codesync/collector.go:CodeCollector.Collect",
			callee: "internal/collector/codesync/reindex.go:augmentWithPreciseCallGraph",
		},
	}

	for _, p := range candidates {
		if slices.Contains(graph[p.caller], p.callee) {
			t.Logf("FOUND edge: %s → %s", p.caller, p.callee)
			continue
		}
		t.Errorf("missing required edge\nwant: %s -> %s\ngot:  callees of %s = %v",
			p.caller, p.callee, p.caller, graph[p.caller])
	}

	// Every endpoint key must name a Go file that actually exists under rootDir.
	// This is what makes the decl key a real identity rather than a plausible
	// string: a key that names no file could never match a collector node ID.
	t.Run("every_key_names_a_real_go_file", func(t *testing.T) {
		if len(graph) == 0 {
			t.Fatal("graph is empty, so this check would be vacuous")
		}
		var checked, bad int
		check := func(key string) {
			checked++
			colon := strings.LastIndex(key, ":")
			if colon < 0 {
				bad++
				t.Errorf("key has no file/symbol separator: %q", key)
				return
			}
			file := key[:colon]
			if !strings.HasSuffix(file, ".go") {
				bad++
				t.Errorf("key's file half is not a Go file: %q (from key %q)", file, key)
				return
			}
			if _, err := os.Stat(filepath.Join(rootDir, file)); err != nil {
				bad++
				t.Errorf("key names a file that does not exist: %q (from key %q): %v", file, key, err)
			}
		}
		for caller, callees := range graph {
			check(caller)
			for _, callee := range callees {
				check(callee)
			}
		}
		t.Logf("checked %d endpoint keys, %d malformed", checked, bad)
	})

	// Methods must be analyzed as CALLERS at corpus scale, not merely as
	// callees. A receiver-qualified caller key — one whose symbol half carries a
	// dot — is the observable form of that. The old package-members seeding
	// produced zero of these over this same corpus.
	//
	// A bare count > 0 is deliberately the assertion rather than a pinned
	// number: the number moves with the tree, the property does not.
	t.Run("at_least_one_receiver_qualified_caller", func(t *testing.T) {
		if len(graph) == 0 {
			t.Fatal("graph is empty, so this check would be vacuous")
		}
		var count int
		var example string
		for caller := range graph {
			colon := strings.LastIndex(caller, ":")
			if colon < 0 {
				continue
			}
			if strings.Contains(caller[colon+1:], ".") {
				count++
				if example == "" {
					example = caller
				}
			}
		}
		t.Logf("receiver-qualified caller keys: %d (example: %s)", count, example)
		if count == 0 {
			t.Error("want: at least one receiver-qualified caller key, proving a method " +
				"was analyzed as a caller; got: 0")
		}
	})
}

// TestBuildGoCallGraph_MethodsAreAnalyzedAsCallers is the fast counterpart to
// TestBuildGoCallGraph: the corpus test is slow and CI-skipped, so this is the
// gate that actually runs everywhere.
//
// It proves three things a package-members seed cannot do:
//
//   - a plain method contributes OUTGOING edges, so it appears as a caller key;
//   - a generic method keys to its declaration — Box[time.Duration] keys as
//     "<file>:Box.Get", which needs origin resolution (an instantiation's own
//     Pkg is nil, so without it the key is "" and the declaration is invisible)
//     AND the corrected receiver derivation (without the reorder the receiver
//     derives to "Duration");
//   - a bound method value's synthetic wrappers neither panic nor produce keys.
//
// THE BOUND METHOD VALUE IS NOT DECORATION. It is the only thing in this
// fixture that exercises the synthetic-wrapper class (Pkg nil AND Origin nil),
// which a resolve-then-read call site panics on, and augmentWithPreciseCallGraph
// has no recover. Neither acceptance corpus surfaces that class, so without this
// leg the nil-contract regression would first appear in a user's repo.
//
// The plain-function caller key is asserted as a CONTROL, so an empty graph
// cannot pass the absence checks vacuously.
//
// Note the test name's US spelling: the repo's pre-commit golangci-lint runs
// with --fix and misspell locale US, which rewrites the British spelling in
// identifiers as well as prose.
func TestBuildGoCallGraph_MethodsAreAnalyzedAsCallers(t *testing.T) {
	root := t.TempDir()
	writePopulateFixtureFile(t, filepath.Join(root, "go.mod"), "module example.com/mc\n\ngo 1.24\n")
	writePopulateFixtureFile(t, filepath.Join(root, "svc", "handler.go"), `package svc

import "time"

type Server struct{}

func (s Server) Handle() int {
	return target()
}

type Box[T any] struct {
	v T
}

func (b Box[T]) Get() int {
	return target()
}

func target() int {
	return 1
}

func Use() int {
	var s Server
	f := s.Handle
	b := Box[time.Duration]{}
	return f() + b.Get() + s.Handle()
}
`)

	// A panic inside the analysis fails this test by itself — that is one of the
	// properties under test, so there is no recover here on purpose.
	result, err := BuildGoCallGraph(t.Context(), root, []string{"."})
	if err != nil {
		t.Fatalf("BuildGoCallGraph error: %v", err)
	}
	graph := result.CallMap

	callers := make([]string, 0, len(graph))
	for caller := range graph {
		callers = append(callers, caller)
	}
	slices.Sort(callers)
	t.Logf("caller keys: %v", callers)

	for _, want := range []string{
		"svc/handler.go:Server.Handle",
		"svc/handler.go:Box.Get",
		// CONTROL: a plain function caller, so an empty graph cannot pass.
		"svc/handler.go:Use",
	} {
		if _, ok := graph[want]; !ok {
			t.Errorf("missing caller key\nwant: %q\ngot:  %v", want, callers)
		}
	}

	// Synthetic wrappers ("Handle$bound" and friends) must contribute no keys.
	for _, caller := range callers {
		if strings.Contains(caller, "$") {
			t.Errorf("synthetic wrapper produced a caller key: %q", caller)
		}
		for _, callee := range graph[caller] {
			if strings.Contains(callee, "$") {
				t.Errorf("synthetic wrapper produced a callee key: %q (from %q)", callee, caller)
			}
		}
	}
}

// TestReceiverTypeName_CutsTypeArgumentsBeforeQualifier pins the ORDER of the
// receiver derivation on the four receiver shapes: value, pointer,
// generic-value and generic-pointer.
//
// The two generic cases are the falsifiers. The derivation this code inherited
// cut the package qualifier BEFORE the type-argument list, so a QUALIFIED type
// argument swallowed the result — measured first-hand against that ordering,
// the Box[time.Duration] receiver derived to "Duration" and the distManager
// receiver, whose type argument is *…/bm25.CorpusStats, derived to
// "CorpusStats". The distManager string is a real receiver type from this repo,
// not an invented one.
func TestReceiverTypeName_CutsTypeArgumentsBeforeQualifier(t *testing.T) {
	const seg = "github.com/fulminate-io/knowledge-mcp/internal/searchengine/"

	cases := []struct {
		name     string
		typeName string
		want     string
	}{
		{
			name:     "value receiver",
			typeName: "example.com/fx/svc/api.Server",
			want:     "Server",
		},
		{
			name:     "pointer receiver",
			typeName: "*example.com/fx/svc/api.Server",
			want:     "Server",
		},
		{
			name:     "generic value receiver",
			typeName: "example.com/fx/svc/api.Box[time.Duration]",
			want:     "Box",
		},
		{
			name:     "generic pointer receiver with qualified type arguments",
			typeName: "*" + seg + "segmentdist.distManager[" + seg + "bm25.Query, *" + seg + "bm25.CorpusStats]",
			want:     "distManager",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := receiverTypeName(tc.typeName); got != tc.want {
				t.Errorf("receiverTypeName(%q)\nwant: %q\ngot:  %q", tc.typeName, tc.want, got)
			}
		})
	}
}
