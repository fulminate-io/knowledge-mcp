// SPDX-License-Identifier: Apache-2.0

package graph

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// dsm_test.go exercises DSMAnalyzer against a FILESYSTEM-sourced module path
// and layer config — the relocation moved both reads off graph metadata and
// onto <RepoRoot>/go.mod and <RepoRoot>/.knowledge/topology_layers.yaml. The
// tests write those files into a t.TempDir(), set Request.RepoRoot to it, and
// assert the analyzer emits the same violation/cycle findings it produced
// under the prior store-backed fixtures. There are no GraphMeta /
// ModulePathKey / LayerConfigKey references anywhere in this file.

const dsmTestModule = "example.com/m"

// writeDSMRepoRoot writes go.mod (module dsmTestModule) and the layer config
// (when layerYAML is non-empty) into a fresh temp dir, returning the root.
func writeDSMRepoRoot(t *testing.T, layerYAML string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module "+dsmTestModule+"\n\ngo 1.23\n"), 0o600); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if layerYAML != "" {
		dir := filepath.Join(root, ".knowledge")
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("mkdir .knowledge: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "topology_layers.yaml"), []byte(layerYAML), 0o600); err != nil {
			t.Fatalf("write topology_layers.yaml: %v", err)
		}
	}
	return root
}

// buildDSMFixture builds a code graph with two packages, pkg/store and
// pkg/api, where pkg/store imports pkg/api (an upward violation when the
// layer config puts store at the base and api above it).
func buildDSMFixture() *graphFixture {
	f := newGraphFixture()
	// One file per package; file node ID is its repo path so packageOf =
	// filepath.Dir gives the package path.
	f.AddNodeFull("pkg/store/s.go", "s.go", kgtypes.NodeFile, "go", nil)
	f.AddNodeFull("pkg/api/a.go", "a.go", kgtypes.NodeFile, "go", nil)
	// pkg/store imports pkg/api → upward violation (store is base).
	f.AddEdge("pkg/store/s.go", dsmTestModule+"/pkg/api", kgtypes.EdgeImports)
	return f
}

// layerYAMLStoreBase declares pkg/store at the base (layer 0) and pkg/api
// above it (layer 1) — so a store → api import is upward and violates.
const layerYAMLStoreBase = `source: test
layers:
  - name: base
    packages:
      - pkg/store
  - name: app
    packages:
      - pkg/api
`

// TestDSM_FilesystemViolation verifies the analyzer reads the module path from
// <RepoRoot>/go.mod and the layering from <RepoRoot>/.knowledge/topology_layers.yaml,
// then flags the store → api upward import as a config-file violation.
func TestDSM_FilesystemViolation(t *testing.T) {
	root := writeDSMRepoRoot(t, layerYAMLStoreBase)
	f := buildDSMFixture()
	req := foundation.Request{Caller: f, Graph: kgtypes.GraphCode, Name: "repo", RepoRoot: root, TopK: 0}

	findings, err := DSMAnalyzer{}.Run(newTestCtx(t), req)
	if err != nil {
		t.Fatalf("DSMAnalyzer.Run: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("expected exactly 1 DSM violation, got %d: %v", len(findings), findings)
	}
	fnd := findings[0]
	if fnd.Algorithm != "dsm" {
		t.Errorf("algorithm = %q, want dsm", fnd.Algorithm)
	}
	if fnd.Severity != foundation.SeverityWarning {
		t.Errorf("severity = %q, want warning (config-file source)", fnd.Severity)
	}
	// from_layer (pkg/store=0) -> to_layer (pkg/api=1): delta 1.
	if got := fnd.Metrics["from_layer"]; got != 0 {
		t.Errorf("from_layer = %g, want 0 (pkg/store base)", got)
	}
	if got := fnd.Metrics["to_layer"]; got != 1 {
		t.Errorf("to_layer = %g, want 1 (pkg/api)", got)
	}
	// Evidence: from pkg, to pkg, then representative file.
	if len(fnd.Evidence) < 3 || fnd.Evidence[0] != "pkg/store" || fnd.Evidence[1] != "pkg/api" {
		t.Errorf("evidence = %v, want [pkg/store pkg/api pkg/store/s.go ...]", fnd.Evidence)
	}
}

// TestDSM_NoGoModuleSurfacesAsAFinding verifies a RepoRoot without go.mod is
// STATED rather than returned as an empty result.
//
// It replaces the assertion that the analyzer skipped with nil findings. An
// empty finding set is indistinguishable from "this repo has no layering
// violations", so the test asserts on the finding's CONTENT — its title and the
// resolved root it names — and never merely that the slice is non-empty, which a
// placeholder finding would also satisfy. The root is the leg that ties this to
// the resolved walk root: a finding naming no path would leave a reader unable to
// tell a genuinely non-Go tree from a mis-resolved one.
func TestDSM_NoGoModuleSurfacesAsAFinding(t *testing.T) {
	root := t.TempDir() // no go.mod
	f := buildDSMFixture()
	req := foundation.Request{Caller: f, Graph: kgtypes.GraphCode, Name: "repo", RepoRoot: root, TopK: 0}

	findings, err := DSMAnalyzer{}.Run(newTestCtx(t), req)
	if err != nil {
		t.Fatalf("DSMAnalyzer.Run: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("a non-Go tree must produce exactly one informational finding, got %d: %v", len(findings), findings)
	}
	got := findings[0]
	if got.Title != DSMNoGoModuleTitle {
		t.Errorf("title = %q, want %q", got.Title, DSMNoGoModuleTitle)
	}
	if got.Severity != foundation.SeverityNotice {
		t.Errorf("severity = %q, want %q — a stated inability is informational, not a violation", got.Severity, foundation.SeverityNotice)
	}
	if !strings.Contains(got.Summary, root) {
		t.Errorf("summary %q does not name the resolved root %q, so a reader cannot tell a non-Go tree from a mis-resolved one", got.Summary, root)
	}
	if !strings.Contains(got.Summary, "go.mod") {
		t.Errorf("summary %q does not name the absent module file", got.Summary)
	}
	// The control the emptiness assertion above cannot supply on its own: the
	// SAME fixture WITH a go.mod produces real violation findings, so "exactly
	// one finding" is a statement about this input rather than about an analyzer
	// that can only ever emit one thing.
	withModule := writeDSMRepoRoot(t, layerYAMLStoreBase)
	req.RepoRoot = withModule
	violations, err := DSMAnalyzer{}.Run(newTestCtx(t), req)
	if err != nil {
		t.Fatalf("DSMAnalyzer.Run (control): %v", err)
	}
	if len(violations) == 0 {
		t.Fatal("control: the same fixture WITH a go.mod must still produce violation findings")
	}
	for _, v := range violations {
		if v.Title == DSMNoGoModuleTitle {
			t.Errorf("control: an analyzable tree must not carry the not-a-Go-module disclosure")
		}
	}
}

// TestDSM_NonCodeGraphSkips verifies a non-code graph skips.
func TestDSM_NonCodeGraphSkips(t *testing.T) {
	root := writeDSMRepoRoot(t, layerYAMLStoreBase)
	f := buildDSMFixture()
	req := foundation.Request{Caller: f, Graph: kgtypes.GraphKnowledge, Name: "default", RepoRoot: root}
	findings, err := DSMAnalyzer{}.Run(newTestCtx(t), req)
	if err != nil {
		t.Fatalf("DSMAnalyzer.Run: %v", err)
	}
	if findings != nil {
		t.Errorf("dsm on a knowledge graph should return nil, got %v", findings)
	}
}

// TestDSM_Cycle verifies a package cycle (store imports api AND api imports
// store) surfaces a critical DSM cycle finding, sourced from the
// path-heuristic provider when no config file is present.
func TestDSM_Cycle(t *testing.T) {
	root := writeDSMRepoRoot(t, "") // no layer config → path-heuristic
	f := newGraphFixture()
	f.AddNodeFull("pkg/store/s.go", "s.go", kgtypes.NodeFile, "go", nil)
	f.AddNodeFull("pkg/api/a.go", "a.go", kgtypes.NodeFile, "go", nil)
	f.AddEdge("pkg/store/s.go", dsmTestModule+"/pkg/api", kgtypes.EdgeImports)
	f.AddEdge("pkg/api/a.go", dsmTestModule+"/pkg/store", kgtypes.EdgeImports)

	req := foundation.Request{Caller: f, Graph: kgtypes.GraphCode, Name: "repo", RepoRoot: root, TopK: 0}
	findings, err := DSMAnalyzer{}.Run(newTestCtx(t), req)
	if err != nil {
		t.Fatalf("DSMAnalyzer.Run: %v", err)
	}
	var sawCycle bool
	for _, fnd := range findings {
		if _, ok := fnd.Metrics["size"]; ok && fnd.Severity == foundation.SeverityCritical {
			sawCycle = true
			if got := fnd.Metrics["size"]; got != 2 {
				t.Errorf("cycle size = %g, want 2", got)
			}
		}
	}
	if !sawCycle {
		t.Errorf("expected a critical DSM cycle finding, got %v", findings)
	}
}

// TestDSM_Registered guards self-registration.
func TestDSM_Registered(t *testing.T) {
	if _, ok := foundation.Get("dsm"); !ok {
		t.Fatal("dsm analyzer must be registered at package init")
	}
}
