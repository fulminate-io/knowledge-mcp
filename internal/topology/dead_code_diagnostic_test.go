// SPDX-License-Identifier: Apache-2.0

package topology

// dead_code_diagnostic_test.go pins the one case where the reachability analysis
// does not complete: RTA produced a diagnostic rather than a call graph.
//
// AN EMPTY RESULT AND A CLEAN RESULT WERE INDISTINGUISHABLE HERE, which is the
// whole reason this test asserts on the finding's CONTENT rather than on the
// slice being non-empty. A placeholder finding satisfies "non-empty"; only the
// relayed diagnostic proves the run's own answer reached the reader.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// TestDeadCode_RTADiagnosticSurfacesAsAFinding drives RunDeadCode over a tree
// with no Go packages, which is what makes RTA produce a diagnostic.
//
// The expected diagnostic text is NOT hand-typed: the test asks runRTA for it
// directly over the same root and requires the finding to carry that exact
// string. A literal would pin today's wording of a message this package only
// relays, and would go stale silently the first time go/packages rephrased it.
func TestDeadCode_RTADiagnosticSurfacesAsAFinding(t *testing.T) {
	root := t.TempDir() // no go.mod, no .go files → RTA loads nothing

	_, wantDiagnostic, err := runRTA(context.Background(), root, true)
	require.NoError(t, err, "an unloadable tree is a diagnostic, not an error")
	require.NotEmpty(t, wantDiagnostic,
		"the fixture must actually produce a diagnostic, else this test asserts nothing")

	findings, err := RunDeadCode(context.Background(), fakeNodeIndexCaller{}, root, "somerepo", 0)
	require.NoError(t, err)
	require.Len(t, findings, 1,
		"a run that produced a diagnostic must state its inability as exactly one finding")

	got := findings[0]
	assert.Equal(t, "dead_code", got.Algorithm)
	assert.Equal(t, DeadCodeIncompleteTitle, got.Title)
	assert.Equal(t, foundation.SeverityNotice, got.Severity,
		"a stated inability is informational, not a dead-code verdict")
	assert.Contains(t, got.Summary, wantDiagnostic,
		"the diagnostic RTA already produced must be relayed, not discarded for a placeholder")
	assert.Contains(t, got.Summary, root,
		"the finding must name the root it analyzed, so a mis-resolved root is distinguishable from an unanalyzable one")
	assert.NotContains(t, strings.ToLower(got.Title), "no dead code",
		"the disclosure must not read as a clean verdict")
}

// TestDeadCode_CleanRunIsNotADisclosure is the known positive the assertion above
// needs: a tree RTA CAN analyze produces dead-function findings and never the
// did-not-complete disclosure. Without it, an implementation that emitted the
// disclosure unconditionally would satisfy every leg of the test above.
func TestDeadCode_CleanRunIsNotADisclosure(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go binary not on PATH")
	}
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module example.com/dcdiag\n\ngo 1.22\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "main.go"), []byte(`package main

func main() {
	live()
}

func live() {}

func dead() {}
`), 0o600))

	findings, err := RunDeadCode(context.Background(), fakeNodeIndexCaller{}, root, "somerepo", 0)
	require.NoError(t, err)
	require.NotEmpty(t, findings, "the fixture holds an unreachable function, so RTA must flag it")
	for _, f := range findings {
		assert.NotEqual(t, DeadCodeIncompleteTitle, f.Title,
			"a tree the analysis completed over must carry no did-not-complete disclosure")
	}
}
