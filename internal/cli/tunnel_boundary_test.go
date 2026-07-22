// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestCLIProxyImportBoundary is the durable encoding of the OSS module boundary:
// the knowledge CLI's ws --proxy transport carries raw SSH bytes + a JSON header
// only — the TunnelFrame wrapping is the RELAY's job. The OSS client must therefore
// import NO agent module and NO executor/v1 proto. Mirrors the relay's
// TestRelayImportBoundary (agent repo cmd/relay/relay_boundary_test.go).
func TestCLIProxyImportBoundary(t *testing.T) {
	// (1) Dependency graph: go list -deps of the cli package must contain neither
	// the agent module nor an executor/v1 proto path.
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	deps := string(out)
	forbiddenDeps := []string{
		"github.com/fulminate-io/agent", // the private agent module
		"/executor/v1",                  // the executor proto package path
	}
	for _, f := range forbiddenDeps {
		if strings.Contains(deps, f) {
			t.Errorf("cli dependency graph contains forbidden package %q — the OSS ws --proxy path must import no agent proto", f)
		}
	}

	// (2) Source scan: no cli non-test .go file may reference an agent proto import
	// (test files skipped so this test's own forbidden-token literals don't match).
	forbiddenTokens := []string{"fulminate-io/agent", "executor/v1", "executorv1"}
	err = filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, readErr := os.ReadFile(path) //nolint:gosec // G122: test-only scan over this package's own fixed local dir
		if readErr != nil {
			return readErr
		}
		for _, tok := range forbiddenTokens {
			if strings.Contains(string(src), tok) {
				t.Errorf("%s references forbidden agent-proto token %q — the CLI ws path stays proto-free", path, tok)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk cli source: %v", err)
	}
}
