// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

const (
	// censusRootEnv points the census at a real repository. Like the corpus
	// verification beside it, this run is a MEASUREMENT INSTRUMENT rather than a
	// gate, so it skips when unset: the numbers depend on the corpus, and a
	// corpus is not something CI can be assumed to have. The artifact it writes
	// IS committed, and a gate reads the artifact.
	censusRootEnv = "FUL1357_CORPUS_ROOT"

	// censusArtifact is fixed rather than configurable: the gate reads this
	// exact path, so an override could only ever write the numbers somewhere the
	// gate does not look.
	censusArtifact = "testdata/ful1357_summary_census.txt"
)

// deterministicCensusRow is one node type's coverage.
type deterministicCensusRow struct {
	total         int
	deterministic int
}

// TestDeterministicCoverageCensus walks a real repository through the
// production chunk → node path and writes the per-node-type deterministic
// coverage artifact.
//
// COUNTS ARE COMMANDS, NOT FACTS. The artifact measures whatever tree it ran
// against and moves as the corpus moves; nothing downstream may quote a number
// from it as fixed. The assertions below are therefore PROPERTIES — over-reach
// is zero, breadth of coverage is at least three types — never remembered
// counts.
func TestDeterministicCoverageCensus(t *testing.T) {
	root := os.Getenv(censusRootEnv)
	if root == "" {
		t.Skipf("set %s to a repository path to run the deterministic coverage census", censusRootEnv)
	}
	// Sanitized before it reaches discovery: an operator-supplied root is an
	// external input, and the walk below joins it with every discovered path.
	root = filepath.Clean(root)
	require.True(t, filepath.IsAbs(root), "%s must be an absolute path, got %q", censusRootEnv, root)

	ctx := context.Background()
	files, err := DiscoverFiles(ctx, root)
	require.NoError(t, err)
	require.NotEmpty(t, files, "control: discovery found no files under the corpus root")

	results, _, err := ChunkFilesParallel(ctx, root, files)
	require.NoError(t, err)
	require.NotEmpty(t, results, "control: chunking produced no results")

	modulePath, _ := ReadModulePath(root)
	rc := &treesitter.RepoContext{Root: root, ModulePath: modulePath, Files: files}
	pop := chunkResultsToPopulate("census", rc, results)
	require.NotEmpty(t, pop.Nodes, "control: the populate pass emitted no nodes")

	// The node set includes the NodeFile and NodeLanguage nodes populate emits
	// and does NOT include package containers, which come from the coderun
	// hierarchy stage — a different code path this plan does not touch.
	rows := map[string]*deterministicCensusRow{}
	for _, n := range pop.Nodes {
		r := rows[n.Type]
		if r == nil {
			r = &deterministicCensusRow{}
			rows[n.Type] = r
		}
		r.total++
		if n.Summary != "" && n.Keywords != "" {
			r.deterministic++
		}
	}

	types := make([]string, 0, len(rows))
	for ty := range rows {
		types = append(types, ty)
	}
	sort.Strings(types)

	var b strings.Builder
	// THE HEADER MUST NOT CARRY AN OPERATOR'S ABSOLUTE PATH. This artifact is
	// checked-in testdata in the OSS-shipped tree. The corpus marker is derived
	// from the module path so it names WHICH corpus without naming anyone's
	// filesystem; the SHA is what lets a later reader tell a stale artifact from
	// a current one.
	fmt.Fprintf(&b, "# corpus=%s sha=%s\n", censusCorpusName(modulePath), censusShortSHA(root))
	for _, ty := range types {
		fmt.Fprintf(&b, "%s %d %d\n", ty, rows[ty].total, rows[ty].deterministic)
	}
	body := b.String()
	require.NoError(t, os.WriteFile(censusArtifact, []byte(body), 0o600))
	t.Logf("wrote %s\n%s", censusArtifact, body)

	// OVER-REACH FENCE: the keep-LLM types and the two containers must carry no
	// deterministic coverage at all. NodeFile / NodePackage embed text is the
	// Summary ALONE, so leakage there would degrade their vectors one-for-one.
	for _, ty := range []string{"function_declaration", "method_declaration", "type_declaration", "file", "language"} {
		r := rows[ty]
		require.NotNil(t, r, "control: no %s rows at all — the census walked the wrong tree", ty)
		require.Zero(t, r.deterministic, "over-reach: %s carries deterministic coverage", ty)
	}

	// BREADTH, not total. A total-only check is satisfied by one type carrying
	// every deterministic node while the other ten ship inert — exactly the
	// failure a whole-corpus number hides. Deliberately NOT a per-type floor:
	// import_statement has zero instances in a Go-dominant corpus, so a per-type
	// floor would be unsatisfiable here through no fault of the implementation.
	covered := 0
	for _, r := range rows {
		if r.deterministic > 0 {
			covered++
		}
	}
	require.GreaterOrEqual(t, covered, 3, "fewer than three distinct types carry deterministic coverage")
}

// censusCorpusName reduces a module path to its last segment, which names the
// corpus without naming a filesystem. Empty module path (a non-Go corpus) reads
// as elided rather than as a lie.
func censusCorpusName(modulePath string) string {
	if modulePath == "" {
		return "<elided>"
	}
	if i := strings.LastIndex(modulePath, "/"); i >= 0 && i+1 < len(modulePath) {
		return modulePath[i+1:]
	}
	return modulePath
}

// censusShortSHA returns the corpus's short git SHA, or "unknown" when the
// corpus is not a git checkout. Best-effort by design: the SHA is provenance on
// the artifact, not an input to any assertion.
func censusShortSHA(root string) string {
	// The root is carried on cmd.Dir rather than as a `-C <root>` ARGUMENT,
	// mirroring discoverWithGit (indexer_discover.go:98): the operator-supplied
	// root never becomes a command argument.
	cmd := exec.Command("git", "rev-parse", "--short", "HEAD")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}
