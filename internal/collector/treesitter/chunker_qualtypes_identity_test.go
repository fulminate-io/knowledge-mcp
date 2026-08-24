// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// qualIdentityArtifact is the measurement artifact this instrument and the
// chunker's allocation budget gate both read, relative to this package
// directory (the working directory of a `go test` run in this package).
const qualIdentityArtifact = "testdata/qualtypes_alloc_measurements.txt"

// qualIdentityLineFloor is the KNOWN-POSITIVE CONTROL for the whole digest: a
// walk that emitted fewer facts than this measured nothing, and a matching
// digest of two empty sets is a vacuous pass.
//
// 1000 is a PLAN-MANDATED literal, not a tree-derived count. It mirrors the
// env-root floor of the sibling census at chunker_callee_whitespace_test.go and
// sits orders of magnitude below the real population, so corpus drift cannot
// false-fail it.
const qualIdentityLineFloor = 1000

// TestGoQualifierOutputDigest is the arm-level output-identity instrument for
// the Go qualifier-types and type-facts arms: it walks a corpus, serializes
// every QualifierTypes map and TypeFacts record the chunker actually emits into
// one canonical line per fact, and reduces the sorted set to a SHA-256.
//
// WHY THIS INSTRUMENT AND NOT THE PARITY HARNESS. TestR2TParityScore
// (cmd/knowledge/internal/collector/codesync/parity_score_test.go) is a
// whole-system coverage aggregate with a 3600s timeout, and its own header says
// it is not a standing regression gate — an arm-level output change could move
// inside it without moving a floor. The requirement being gated here is
// byte-identical QualifierTypes/TypeFacts output, an arm-level property that
// needs an arm-level instrument.
//
// ENVIRONMENT:
//
//	QUALID_ROOT   corpus root to walk. Unset walks this repo's own cmd/knowledge
//	              and cmd/knowledge-server trees.
//	QUALID_EXPECT set to 1 to run in VERIFY mode: the recorded baseline key for
//	              this corpus must exist, be non-empty, and match.
//	QUALID_OUT    optional path for the full sorted line dump, for diffing a
//	              mismatch. Point it at /tmp, never under a frozen corpus root.
//
// QUALID_EXPECT IS A MODE SWITCH, NOT A FALLBACK. With QUALID_EXPECT=1 a
// missing or empty baseline key is a test FAILURE naming the key and the
// corpus; there is no permissive lane that lets an unrecorded corpus pass.
// Unset, the test logs the digest and passes — that is baseline-CAPTURE mode,
// and it is the only mode that does not assert an identity.
//
// SCOPE IS GO FILES ONLY, AND THAT IS NOW A DELIBERATE NARROWING RATHER THAN A
// DESCRIPTION OF THE WORLD. Other languages carry qualifier and type-facts arms;
// testTypedQualifierCensus is the authority on which, and it is the place to
// read rather than this comment, which cannot stay current as arms land. The
// scope stays Go because this test's two frozen corpus digests pin the GO arm's
// output identity — widening the walk would move them for reasons that have
// nothing to do with the Go arm.
func TestGoQualifierOutputDigest(t *testing.T) {
	corpus, relBase, roots := qualIdentityRoots(t)

	chunker := NewChunker()
	defer chunker.Close()

	// Pre-sized: the repo's own trees emit six figures of facts, so growing
	// from zero would dominate a walk whose subject is allocation behavior.
	lines := make([]string, 0, 1<<17)

	for _, root := range roots {
		//nolint:gosec // walks a source tree the operator named: either this repo's own, or the corpus root supplied to measure a frozen corpus
		walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				// censusSkipDirs is REUSED VERBATIM from the sibling census,
				// including its load-bearing .claude exclusion: .claude/worktrees
				// holds full checkouts of this repo, so descending into it walks
				// every fact once per live worktree.
				if censusSkipDirs[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			if DetectLanguage(path) != LangGo {
				return nil
			}
			src, readErr := os.ReadFile(path) //nolint:gosec // walks a source tree, same as the sibling census at chunker_callee_whitespace_test.go
			if readErr != nil {
				return readErr
			}
			result, chunkErr := chunker.ChunkFile(context.Background(), path, src)
			if chunkErr != nil {
				return chunkErr
			}
			rel, relErr := filepath.Rel(relBase, path)
			if relErr != nil {
				return relErr
			}
			lines = appendQualIdentityLines(lines, rel, result)
			return nil
		})
		require.NoError(t, walkErr, "walking %s", root)
	}

	require.GreaterOrEqualf(t, len(lines), qualIdentityLineFloor,
		"the digest walk emitted %d fact lines over %v, below the known-positive floor of %d — it measured nothing, and a matching digest of two empty sets proves nothing",
		len(lines), roots, qualIdentityLineFloor)

	sort.Strings(lines)

	// Fed incrementally rather than through a joined mega-string: the joined
	// form of this corpus is tens of megabytes.
	h := sha256.New()
	for _, line := range lines {
		h.Write([]byte(line))
		h.Write([]byte("\n"))
	}
	digest := hex.EncodeToString(h.Sum(nil))

	if out := os.Getenv("QUALID_OUT"); out != "" {
		body := strings.Join(lines, "\n") + "\n"
		//nolint:gosec // the dump path is named by the operator running this diagnostic, exactly as the corpus root above is; there is no untrusted input in a test the operator invokes with an explicit env var
		require.NoError(t, os.WriteFile(out, []byte(body), 0o600), "writing QUALID_OUT dump")
		t.Logf("QUALID_OUT dump written: %s", out)
	}

	// ONE LINE, this exact shape: the criteria grep digest=[0-9a-f]{64}.
	t.Logf("corpus=%s lines=%d digest=%s", corpus, len(lines), digest)

	if os.Getenv("QUALID_EXPECT") != "1" {
		return
	}

	key := "digest_" + corpus + "_baseline"
	want := qualIdentityArtifactValue(t, key)
	require.NotEmptyf(t, want,
		"QUALID_EXPECT=1 but %s carries no value for %s: the baseline for corpus %q was never recorded, and verify mode does not pass an unrecorded corpus",
		qualIdentityArtifact, key, corpus)
	require.Equalf(t, want, digest,
		"corpus %q: QualifierTypes/TypeFacts output changed — digest %s does not match the recorded baseline %s (%s). Re-run with QUALID_OUT=/tmp/<name>.txt on both trees and diff the dumps",
		corpus, digest, want, key)
}

// qualIdentityRoots resolves the corpus name, the path all relative paths are
// taken against, and the directories to walk.
//
// The corpus NAME is what selects the baseline key, so it is the walked root's
// basename — corpora/knowledge is corpus "knowledge" and reads
// digest_knowledge_baseline. The repo's own two internal trees are one corpus
// named "repo", walked from a shared base so a relative path names which binary
// a file belongs to.
func qualIdentityRoots(t *testing.T) (corpus, relBase string, roots []string) {
	t.Helper()
	if envRoot := os.Getenv("QUALID_ROOT"); envRoot != "" {
		clean := filepath.Clean(envRoot)
		info, err := os.Stat(clean)
		require.NoErrorf(t, err, "QUALID_ROOT=%s is not readable", envRoot)
		require.Truef(t, info.IsDir(), "QUALID_ROOT=%s is not a directory", envRoot)
		return filepath.Base(clean), clean, []string{clean}
	}
	// repoRootForCensus anchors on the two internal trees rather than on
	// go.mod, because cmd/knowledge is its own module and the first go.mod
	// above this package stops one binary short of the server tree.
	root := repoRootForCensus(t)
	return "repo", root, []string{
		filepath.Join(root, "cmd", "knowledge"),
		filepath.Join(root, "cmd", "knowledge-server"),
	}
}

// appendQualIdentityLines serializes one file's arm output into the LOCKED
// canonical form, TAB-separated, one line per fact:
//
//	QT <relpath> <edge.FromID> <qualifierName>=<Text>|<FromCall>|<ResultIndex>
//	TF <relpath> <ParentName>.<Name> R<i>=<results[i]>
//	TF <relpath> <ParentName>.<Name> F<fieldName>=<fieldType>
//
// QualifierTypes are read off the EDGE's reference site rather than off a
// chunk, for the reason qualTypesFor records: the reference site is what the
// resolution ladder consults, so a map that never reached an edge is invisible
// to the rung no matter how correctly the walk built it. Several edges of one
// declaration SHARE ONE *RefSite, so the map is taken once per FromID rather
// than once per edge — otherwise a declaration's bindings would be weighted by
// how many references it happens to emit.
//
// TypeFacts come off Result.Chunks (Chunk.TypeFacts), the surface the parser's
// declaration index consumes.
func appendQualIdentityLines(lines []string, rel string, result *Result) []string {
	seenFrom := map[string]bool{}
	for i := range result.Edges {
		e := &result.Edges[i]
		if e.Ref == nil || len(e.Ref.QualifierTypes) == 0 {
			continue
		}
		if seenFrom[e.FromID] {
			continue
		}
		seenFrom[e.FromID] = true
		for name, qt := range e.Ref.QualifierTypes {
			lines = append(lines, "QT\t"+rel+"\t"+e.FromID+"\t"+name+"="+
				qt.Text+"|"+strconv.FormatBool(qt.FromCall)+"|"+strconv.Itoa(qt.ResultIndex))
		}
	}

	for i := range result.Chunks {
		c := &result.Chunks[i]
		if c.TypeFacts == nil {
			continue
		}
		decl := c.ParentName + "." + c.Name
		for j, r := range c.TypeFacts.Results {
			lines = append(lines, "TF\t"+rel+"\t"+decl+"\tR"+strconv.Itoa(j)+"="+r)
		}
		for field, typ := range c.TypeFacts.Fields {
			lines = append(lines, "TF\t"+rel+"\t"+decl+"\tF"+field+"="+typ)
		}
	}
	return lines
}

// qualIdentityArtifactValue reads one key out of the measurement artifact.
//
// A missing FILE is a hard failure rather than an empty value: verify mode was
// asked for, and an unreadable artifact means the identity was never checked.
func qualIdentityArtifactValue(t *testing.T, key string) string {
	t.Helper()
	body, err := os.ReadFile(qualIdentityArtifact)
	require.NoErrorf(t, err, "reading the measurement artifact %s", qualIdentityArtifact)
	for line := range strings.SplitSeq(string(body), "\n") {
		if rest, ok := strings.CutPrefix(line, key+"="); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}
