// SPDX-License-Identifier: Apache-2.0

package jsmodule

import (
	"context"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// corpusEnv names the acceptance corpus; the default is where this ticket's
// measurements were taken.
const (
	corpusEnv     = "KNOWLEDGE_JS_CORPUS"
	corpusDefault = "/tmp/agentmain/agent"
)

// TestResolve_AgentCorpus runs the resolver over a real repository and asserts
// PROPERTIES, never pinned counts — the corpus moves, and a pinned count would
// turn every unrelated commit there into a red build here.
//
// THE TWO CLASS CONTROLS ARE WHY THIS TEST MEANS ANYTHING. A resolver that took
// the relative branch for every specifier would satisfy both property
// assertions trivially, so the tsconfig-path class and the index-file class are
// each required NON-EMPTY. They are logged by name so the gate can see them.
func TestResolve_AgentCorpus(t *testing.T) {
	root := os.Getenv(corpusEnv)
	if root == "" {
		root = corpusDefault
	}
	// Sanitized before use: the corpus root is an operator-supplied external
	// input and is joined with every discovered path below.
	root = filepath.Clean(root)
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		t.Skipf("acceptance corpus %s not present; set %s to run this probe", root, corpusEnv)
	}
	// PRESENT MEANS USABLE, NOT MERELY THERE. Every read below goes through git, so
	// a path that exists but is not a working repository — a half-removed or
	// partially-cloned checkout, which leaves a .git directory that git itself
	// rejects — is a corpus that is absent for this probe's purposes. Guarding on
	// os.Stat alone let such a path through and turned an absent corpus into a hard
	// failure at the first git call, which reads as a broken resolver rather than a
	// missing fixture. The reason is quoted into the skip so it stays diagnosable.
	probe := exec.Command("git", "rev-parse", "--git-dir")
	probe.Dir = root
	if out, gitErr := probe.CombinedOutput(); gitErr != nil {
		t.Skipf("acceptance corpus %s is not a usable git repository (%s); set %s to run this probe",
			root, strings.TrimSpace(string(out)), corpusEnv)
	}

	files := corpusFiles(t, root)
	require.NotEmpty(t, files, "the corpus must yield a discovered file set")

	exports, bindings := chunkCorpus(t, root, files)
	r, err := NewResolver(root, files, exports)
	require.NoError(t, err)

	var relative, tsconfigPath, indexFile, outside, chainLeftRepo int
	var unresolvable []string

	for importer, binds := range bindings {
		for _, b := range binds {
			class := classifySpecifier(r, importer, b.Specifier)
			if class == "bare" || class == "absolute" {
				continue
			}
			if class == "relative" {
				relative++
			} else {
				tsconfigPath++
			}

			target, outcome := r.Resolve(importer, b.Specifier, b.Imported, b.Kind)
			switch outcome {
			case OutcomeBound, OutcomeNoNamedDecls:
				// NO SPECIFIER MAY RESOLVE OUTSIDE THE DISCOVERED SET: naming a
				// file the collector never discovered would claim an indexed
				// target that is not there.
				if !r.files[target.File] {
					outside++
					t.Errorf("resolved outside the discovered set: %s -> %s (%s)",
						importer, target.File, b.Specifier)
				}
				if isIndexFile(target.File) {
					indexFile++
				}
			case OutcomeUndiscovered:
				// Reported with a reason rather than silently dropped.
				unresolvable = append(unresolvable,
					importer+" -> "+b.Specifier+" (no discovered file at "+target.File+")")
			case OutcomeOutOfRepo:
				// A RE-EXPORT CHAIN THAT LEAVES THE REPOSITORY, which is the
				// documented behavior and not a defect: the specifier resolves
				// to an in-repo barrel that does not DECLARE the name, and the
				// barrel forwards it from a package. Read first-hand on this
				// corpus at web/src/app/test/utils.tsx:136,
				// `export * from '@testing-library/react'`, which is where
				// every instance of this class here comes from. The caller
				// records an empty scope, which is absent from the declaration
				// index by construction, so the reference terminates instead of
				// manufacturing an edge to a same-named local.
				chainLeftRepo++
			case OutcomeRefused:
				// A side-effect import binds no name; nothing to resolve.
			}
		}
	}

	assert.Zero(t, outside, "no specifier may resolve to a path outside the discovered set")

	// THE KNOWN-POSITIVE CONTROLS. Their names are logged verbatim so the gate
	// can assert they were reached.
	require.Positive(t, tsconfigPath, "tsconfig-path class must be non-empty")
	t.Logf("tsconfig_path_class_nonempty n=%d", tsconfigPath)
	require.Positive(t, indexFile, "index-file class must be non-empty")
	t.Logf("index_file_class_nonempty n=%d", indexFile)

	t.Logf("corpus %s: %d relative, %d tsconfig-path, %d via index file, "+
		"%d re-export chains leaving the repo, %d unresolvable",
		root, relative, tsconfigPath, indexFile, chainLeftRepo, len(unresolvable))
	for _, u := range unresolvable {
		t.Logf("unresolvable: %s", u)
	}
}

// corpusFiles lists the corpus's tracked files, repo-relative — git's own view
// of the repository, which is what the collector's discovery walks.
func corpusFiles(t *testing.T, root string) []string {
	t.Helper()
	// The root rides cmd.Dir rather than an argv entry: it is an
	// operator-supplied external value, and keeping it out of the argument
	// vector is what makes that unambiguous to a reader and to gosec.
	cmd := exec.Command("git", "ls-files")
	cmd.Dir = root
	out, err := cmd.Output()
	require.NoError(t, err, "listing corpus files")
	var files []string
	for line := range strings.SplitSeq(string(out), "\n") {
		if line != "" {
			files = append(files, line)
		}
	}
	return files
}

// chunkCorpus runs the real chunker over every ECMAScript file, so the probe
// consumes the SAME import captures the collector produces rather than a
// re-implementation of them.
func chunkCorpus(
	t *testing.T, root string, files []string,
) (map[string]FileExports, map[string][]treesitter.ImportBinding) {
	t.Helper()
	chunker := treesitter.NewChunker()
	defer chunker.Close()

	exports := map[string]FileExports{}
	bindings := map[string][]treesitter.ImportBinding{}

	for _, rel := range files {
		switch path.Ext(rel) {
		case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs":
		default:
			continue
		}
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			continue
		}
		res, err := chunker.ChunkFile(context.Background(), rel, src)
		if err != nil || res == nil {
			continue
		}
		fe := FileExports{Declared: map[string]bool{}}
		for _, c := range res.Chunks {
			if c.Name != "" {
				fe.Declared[c.Name] = true
			}
		}
		if len(res.Chunks) > 0 {
			ctx := res.Chunks[0].Context
			fe.ReExports = ctx.ReExports
			fe.DefaultName = ctx.DefaultExportName
			bindings[rel] = ctx.ImportBindings
		}
		exports[rel] = fe
	}
	return exports, bindings
}

// classifySpecifier names the ladder branch a specifier takes, so the probe can
// report per-class counts.
func classifySpecifier(r *Resolver, importer, specifier string) string {
	switch {
	case strings.HasPrefix(specifier, "./"), strings.HasPrefix(specifier, "../"):
		return "relative"
	case strings.HasPrefix(specifier, "/"):
		return "absolute"
	}
	if _, ok := r.aliasCandidate(importer, specifier); ok {
		return "tsconfig_path"
	}
	return "bare"
}

// isIndexFile reports whether a resolved path is a directory index.
func isIndexFile(file string) bool {
	base := path.Base(file)
	return strings.HasPrefix(base, "index.")
}
