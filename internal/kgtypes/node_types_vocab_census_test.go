// SPDX-License-Identifier: Apache-2.0

package kgtypes

// node_types_vocab_census_test.go is the CLIENT-SIDE half of the cross-module
// plan-part vocabulary census. Its server twin lives in the server store package
// and reads the same two files with the same parser.
//
// THIS FILE CARRIES THE PARTS WHOSE SUBJECT IS THIS MODULE — its own declared
// literals, the reader they are read with, and the reader's self-check — and it
// is published to the OSS mirror. The leg that reads knowledge-server's
// declaration file is in node_types_vocab_census_sibling_test.go, which the sync
// script removes from the published tree: the mirror is this module alone, so a
// read of the other module's source can only fail there. Both legs run here, and
// the cache argument below is unaffected by the split, since both files compile
// into this same package in this same module.
//
// WHY THE CENSUS IS WRITTEN TWICE, once per module, rather than once. The Go test
// cache does not track a file the test opens from OUTSIDE its own module. The
// toolchain says so in its own words, at cmd/go/internal/test/test.go in the
// "open" arm of the test-input hasher:
//
//	if a.Package.Root == "" || search.InDir(name, a.Package.Root) == "" {
//	    // Do not recheck files outside the module, GOPATH, or GOROOT root.
//	    break
//	}
//
// So a census living only in one module can be served a CACHED PASS after an
// edit to the other module's vocabulary — which is precisely the edit it exists
// to catch. Reproduced: with this module's NodePlanAnnotation changed to
// "plan_annotationX", the server census answered `ok (cached)` and said nothing.
//
// THE OBVIOUS REPAIRS ARE BOTH CLOSED. A //go:embed of the other module's file
// cannot be written at all: an embed pattern goes through fs.ValidPath, which
// rejects every path containing a ".." element, so a pattern reaching out of the
// package directory is refused as "invalid pattern syntax" before the build
// starts. And hoisting the census to the repo-root module would put a
// hand-written package beside the generated protobuf, which the architecture
// forbids outright.
//
// WHAT THE MIRROR BUYS, stated as the property rather than as a hope: the cache
// can only skip a census whose OWN module did not change. An edit to this
// module's vocabulary is a compiled input of THIS package, so this census always
// runs on it; an edit to the server's vocabulary always runs the server twin. The
// cross-module read stays untracked in both directions, and neither direction is
// left to it. CI, which builds each module in a cold container, runs both
// regardless.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"
)

// clientCensusWireLiterals is this side's copy of the row set. It is a DELIBERATE
// duplicate of the server twin's table for the same reason the vocabulary itself
// is duplicated: the two modules share no hand-written package.
var clientCensusWireLiterals = map[string]string{
	"NodePlanSection":    "plan_section",
	"NodePlanAnnotation": "plan_annotation",
}

// This module's declaration site, MODULE-relative — the one spelling that names
// it both here, where the module sits at cmd/knowledge, and in the published
// mirror, where the module root is the repository root. The SERVER's site is
// repo-relative by nature and is declared in the sibling half, which does not
// ship.
const censusClientVocabFile = "internal/kgtypes/node_types.go"

// censusModuleRoot walks up from the test's working directory to this module's
// root, FAILING rather than guessing — a guessed root silently narrows the
// corpus, which is the one way a census lies.
func censusModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("census cannot read the working directory: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod found above %s — the census cannot locate the module root", dir)
		}
		dir = parent
	}
}

// censusConstLiterals returns every `Name NodeType = "literal"` const the file
// declares. It reads the DECLARATION rather than the file text, so a const that
// moved between blocks or grew a comment still counts, and a name that appears
// only inside a comment or a string never does.
//
// A const whose value is not a plain string literal is reported with an EMPTY
// literal rather than skipped, so the caller sees "declared but not a literal"
// instead of the census silently reading it as absent.
func censusConstLiterals(t *testing.T, root, rel string) map[string]string {
	t.Helper()
	path := filepath.Join(root, rel)
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("census could not parse %s: %v", path, err)
	}
	out := map[string]string{}
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			ident, ok := vs.Type.(*ast.Ident)
			if !ok || ident.Name != "NodeType" {
				continue
			}
			for i, name := range vs.Names {
				lit := ""
				if i < len(vs.Values) {
					if bl, ok := vs.Values[i].(*ast.BasicLit); ok && bl.Kind == token.STRING {
						if len(bl.Value) >= 2 && bl.Value[0] == '"' && bl.Value[len(bl.Value)-1] == '"' {
							lit = bl.Value[1 : len(bl.Value)-1]
						}
					}
				}
				out[name.Name] = lit
			}
		}
	}
	return out
}

// TestPlanPartVocabularyLiterals_InThisModule is the half of the parity census
// whose subject is THIS module: the wire literals its own vocabulary declares,
// read out of the source with the same parser the cross-module half uses.
//
// IT IS NOT THE PARITY GUARD, and the distinction is the point of the split. The
// guard that catches the two modules DRIFTING reads knowledge-server's
// declaration file and lives in node_types_vocab_census_sibling_test.go, which
// the sync script does not publish — the mirror is the client module alone and
// that file is not in it. What ships is this: the client's own declarations
// still say what the wire says, asserted against a table rather than against
// themselves.
//
// A CONSTANT COMPARISON WOULD NOT DO. Asserting string(NodePlanSection) reads
// the compiled value, which is the same expression the census is auditing; the
// source read is what catches a declaration that compiles to the right value by
// a route the wire cannot follow, which is the composed-const case the self-check
// below pins.
func TestPlanPartVocabularyLiterals_InThisModule(t *testing.T) {
	root := censusModuleRoot(t)
	clientConsts := censusConstLiterals(t, root, censusClientVocabFile)

	// The parse found SOMETHING — otherwise every assertion below would read as
	// a clean absence rather than a broken probe.
	if len(clientConsts) == 0 {
		t.Fatalf("census read zero NodeType consts from %s — the probe is broken, not the vocabulary", censusClientVocabFile)
	}

	for name, want := range clientCensusWireLiterals {
		lit, ok := clientConsts[name]
		if !ok {
			t.Errorf("%s is not declared in %s — a node type the wire expects is missing from this module", name, censusClientVocabFile)
			continue
		}
		if lit != want {
			t.Errorf("%s in %s = %q, want %q", name, censusClientVocabFile, lit, want)
		}
	}
}

// TestPlanPartVocabularyParity_FromTheClientModule_SelfCheck drives the reader
// over source it controls, so the census above is known to be CAPABLE of failing
// rather than merely never having failed.
func TestPlanPartVocabularyParity_FromTheClientModule_SelfCheck(t *testing.T) {
	dir := t.TempDir()
	write := func(name, src string) string {
		t.Helper()
		rel := filepath.Join("fixture", name)
		if err := os.MkdirAll(filepath.Join(dir, "fixture"), 0o750); err != nil {
			t.Fatalf("fixture dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(src), 0o600); err != nil {
			t.Fatalf("fixture write: %v", err)
		}
		return rel
	}

	present := write("present.go", "package p\n\ntype NodeType string\n\nconst (\n\tNodePlanSection NodeType = \"plan_section\"\n)\n")
	absent := write("absent.go", "package p\n\ntype NodeType string\n\nconst (\n\tNodeOther NodeType = \"other\"\n)\n")
	wrong := write("wrong.go", "package p\n\ntype NodeType string\n\nconst (\n\tNodePlanSection NodeType = \"section\"\n)\n")
	composed := write("composed.go", "package p\n\ntype NodeType string\n\nconst prefix = \"plan_\"\n\nconst (\n\tNodePlanSection NodeType = prefix + \"section\"\n)\n")

	if got := censusConstLiterals(t, dir, present)["NodePlanSection"]; got != "plan_section" {
		t.Errorf("the PRESENT case must read the literal, got %q", got)
	}
	if _, ok := censusConstLiterals(t, dir, absent)["NodePlanSection"]; ok {
		t.Errorf("the ABSENT case must report the const as undeclared")
	}
	if got := censusConstLiterals(t, dir, wrong)["NodePlanSection"]; got != "section" {
		t.Errorf("the WRONG-LITERAL case must read the wrong literal rather than the expected one, got %q", got)
	}
	// A composed value is DECLARED but unreadable, which the census reports as an
	// empty literal — distinguishable from absent, so it fails the comparison
	// loudly instead of passing as a missing row.
	got, ok := censusConstLiterals(t, dir, composed)["NodePlanSection"]
	if !ok || got != "" {
		t.Errorf("a composed const must read as declared-with-empty-literal, got (%q, declared=%v)", got, ok)
	}
}
