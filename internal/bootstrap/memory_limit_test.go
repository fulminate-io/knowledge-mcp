// SPDX-License-Identifier: Apache-2.0

// memory_limit_test.go pins the client's soft memory ceiling: its value, the
// precedence of an operator's own GOMEMLIMIT, and the single place that imposes it.

package bootstrap

import (
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
)

// TestClientSoftMemoryLimitIsTheOwnersValue pins GOMEMLIMIT at the value the owner
// chose, and states why it is not derived from the residency budget.
//
// THE OWNER'S DECISION (2026-09-15): the soft limit STAYS 4 GiB because it bounds
// the WHOLE PROCESS — an AST collection's live working set needs more than the
// segment pools do — while the 1 GiB residency budget and the release of a sealed
// segment's encoder output are what bound the DRAIN's own footprint. The two
// numbers answer different questions, so the larger one is not an inconsistency to
// be tidied away, and a lane that "fixes" it by deriving one from the other is
// changing a decision rather than a constant.
//
// The expectation is written as the literal the owner named rather than as a
// reference to the constant under test, which would be the thing under test
// supplying its own answer key.
func TestClientSoftMemoryLimitIsTheOwnersValue(t *testing.T) {
	const ownerChosenBytes = 4 * 1024 * 1024 * 1024 // 4 GiB
	if defaultClientMemLimit != ownerChosenBytes {
		t.Errorf("the client's soft memory limit is %d bytes, but the owner's decision is %d (4 GiB); "+
			"changing it is an owner decision, not an implementation one",
			defaultClientMemLimit, ownerChosenBytes)
	}
}

// TestAnOperatorsOwnMemoryLimitWins is the precedence arm: a GOMEMLIMIT set before
// this process reached applyMemoryLimit must survive it, because that is how a
// container operator sizes the client for their own pod.
//
// IT DRIVES THE MECHANISM THE CODE READS. The runtime resolves the GOMEMLIMIT
// environment variable at startup, so a test that set the variable would observe
// nothing; what applyMemoryLimit actually branches on is whether the ACTIVE limit
// is still the zero-config math.MaxInt64, which this sets directly.
func TestAnOperatorsOwnMemoryLimitWins(t *testing.T) {
	const operatorLimit = 9 << 30
	restore := debug.SetMemoryLimit(-1)
	t.Cleanup(func() { debug.SetMemoryLimit(restore) })

	debug.SetMemoryLimit(operatorLimit)
	applyMemoryLimit()
	if got := debug.SetMemoryLimit(-1); got != operatorLimit {
		t.Errorf("applyMemoryLimit overwrote an operator's GOMEMLIMIT: limit is %d, want %d", got, operatorLimit)
	}

	// KNOWN-POSITIVE: with no limit in effect, the same call DOES impose the
	// default. Without this arm the assertion above would pass on an
	// applyMemoryLimit that had been turned into a no-op.
	debug.SetMemoryLimit(math.MaxInt64)
	applyMemoryLimit()
	if got := debug.SetMemoryLimit(-1); got != defaultClientMemLimit {
		t.Errorf("applyMemoryLimit left the limit at %d with nothing else set, want the default %d",
			got, defaultClientMemLimit)
	}
}

// TestTheDaemonBootPathImposesTheLimit covers the boot path that actually starts a
// long-lived client: runServe, which is also the restart-handoff child's entry
// point and which reproduces Run's logging and GOMEMLIMIT setup.
//
// IT READS THE SOURCE, because the claim is a structural one and a behavioral
// test cannot make it. Calling applyMemoryLimit twice from a test and comparing the
// results holds for any deterministic function: it observes no boot path at all,
// so deleting the call from runServe would leave it green.
//
// AND THERE IS ONE BOOT PATH TODAY, not two. bootstrap.Run is a stub that returns
// the "no longer serves MCP over stdio" error and starts nothing, so the pair this
// change's plan named — Run and runServe — is really the helper's DEFINITION site
// and its one caller. The property that matters survives the correction and is the
// one asserted here and in the sibling below: whatever applies the limit does it
// through the single helper, and the daemon path does apply it.
func TestTheDaemonBootPathImposesTheLimit(t *testing.T) {
	parsed, err := parser.ParseFile(token.NewFileSet(), "daemon.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing daemon.go: %v", err)
	}
	found, calls := false, 0
	for _, decl := range parsed.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "runServe" {
			continue
		}
		found = true
		calls = countCalls(fn, "applyMemoryLimit")
	}
	if !found {
		t.Fatal("daemon.go declares no runServe — this row names the wrong entry point")
	}
	if calls != 1 {
		t.Errorf("runServe calls applyMemoryLimit %d times, want exactly 1: a daemon that imposes no ceiling, "+
			"or imposes its own, is the divergence this row exists to catch", calls)
	}
}

// TestEverySoftLimitGoesThroughTheOneHelper is the same claim from the other side,
// over every non-test file of this package: nothing but applyMemoryLimit may call
// debug.SetMemoryLimit, so two boot paths cannot drift to different ceilings and
// no path can skip the branch that lets an operator's own GOMEMLIMIT win.
//
// The tree-wide instrument for this class is the corpus check named "the process
// soft memory limit is imposed in exactly one place", which walks every Go file
// rather than this package; this row is the fast local echo that fails in
// `go test` without waiting for a checks run.
func TestEverySoftLimitGoesThroughTheOneHelper(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parsing the bootstrap package: %v", err)
	}
	inside, outside := 0, 0
	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}
				n := countSelectorCalls(fn, "SetMemoryLimit")
				if n == 0 {
					continue
				}
				if fn.Name.Name == "applyMemoryLimit" {
					inside += n
					continue
				}
				outside += n
				t.Errorf("%s: %s calls debug.SetMemoryLimit outside applyMemoryLimit — "+
					"a second imposer diverges from the first the moment either is edited",
					filepath.Base(name), fn.Name.Name)
			}
		}
	}
	// KNOWN-POSITIVE: the helper itself holds the read and the set, so a zero
	// outside it is "nowhere else" rather than "nowhere at all".
	if inside < 2 {
		t.Errorf("applyMemoryLimit holds %d SetMemoryLimit calls, want the read and the set — "+
			"without both, the zero found outside it proves nothing", inside)
	}
	if outside > 0 {
		t.Logf("imposing sites outside the helper: %d", outside)
	}
}

// countCalls counts calls to a package-level function by name inside fn.
func countCalls(fn *ast.FuncDecl, name string) int {
	n := 0
	ast.Inspect(fn, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == name {
			n++
		}
		return true
	})
	return n
}

// countSelectorCalls counts calls to a SELECTED method or function by its final
// name (pkg.Name) inside fn.
func countSelectorCalls(fn *ast.FuncDecl, name string) int {
	n := 0
	ast.Inspect(fn, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == name {
			n++
		}
		return true
	})
	return n
}
