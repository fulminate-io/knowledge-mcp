// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// The MULTI-ROOT corpus artifact. It EXTENDS the single-root per-language walk
// rather than adding a second probe: same discovery, same chunking, same
// production resolution order, run once PER ROOT and aggregated per corpus.
const (
	// multiLangRootsEnv is an os.PathListSeparator-separated list of ABSOLUTE
	// corpus roots. Unset means SKIP, exactly as corpusRootEnv does at
	// corpus_verification_test.go:60 — the numbers depend on corpora, and a
	// corpus is not something CI can be assumed to have.
	multiLangRootsEnv = "FUL1347_CORPUS_ROOTS"

	// multiLangArtifact is a NEW path, deliberately not the landed pair's.
	// Pointing the landed single-root harness at a new tree would overwrite the
	// COMMITTED txt artifacts and break the landed gates that pin
	// corpus_root=agent in both. It lives in /tmp rather
	// than testdata for the same reason the landed JSON artifact does:
	// committing it would ship seven absolute personal paths into the OSS
	// mirror, which cp -R of cmd/knowledge/internal already carries.
	//
	// THE PATH IS READ BY A SECOND TICKET'S ACCEPTANCE, so it is a fixed
	// constant rather than a configurable one.
	multiLangArtifact = "/tmp/f1347_multi_language_corpus.json"
)

// corpusEntry is one measured root.
//
// THE DIRECTORY BASENAME IS THE PRIMARY-LANGUAGE TAG. Each root holds one
// language's canonical source, so filepath.Base(root) names the language that
// corpus exists to gate, and the test requires it to be a REGISTERED language
// so a mis-named corpus directory fails loudly rather than producing an
// unaddressable entry.
type corpusEntry struct {
	PrimaryLanguage string `json:"primary_language"`
	// CorpusRoot is the absolute path of the tree that was MEASURED, and
	// CorpusCommit is THAT tree's own HEAD — never this repository's. The
	// envelope's Commit carries this repository's.
	CorpusRoot      string `json:"corpus_root"`
	CorpusCommit    string `json:"corpus_commit"`
	FilesDiscovered int    `json:"files_discovered"`
	ChunkNodes      int    `json:"chunk_nodes"`
	// FilesByLanguage and LanguagesWithNoFile are derived from FILE COUNTS, and
	// are deliberately NOT spelled languages_absent_from_corpus. That landed key
	// is derived from resolution ROWS and means something weaker: a corpus can
	// hold files of a language that contributes no row at all, so the two
	// meanings never share a spelling.
	FilesByLanguage     map[string]int `json:"files_by_language"`
	LanguagesWithNoFile []string       `json:"languages_with_no_file"`
	Languages           []multiLangRow `json:"languages"`
}

// multiLangJSON is the artifact envelope.
type multiLangJSON struct {
	// Commit is the HEAD of the KNOWLEDGE repo — the code under measurement.
	Commit  string        `json:"commit"`
	Corpora []corpusEntry `json:"corpora"`
}

// TestFUL1347MultiLanguageCorpus measures every declared root through the
// PRODUCTION resolution order and writes the per-corpus artifact.
//
// THE PER-ROOT LOOP IS SERIAL BY DESIGN. The CPU-bound per-file stage is
// ChunkFilesParallel, which is already parallel and reused unchanged; running
// roots concurrently would interleave writes to the single artifact for no gain.
func TestFUL1347MultiLanguageCorpus(t *testing.T) {
	raw := os.Getenv(multiLangRootsEnv)
	if raw == "" {
		t.Skipf("set %s to a %c-separated list of corpus roots to run the multi-language measurement",
			multiLangRootsEnv, os.PathListSeparator)
	}

	out := multiLangJSON{Commit: knowledgeHeadCommit(t)}
	for _, root := range filepath.SplitList(raw) {
		if root == "" {
			continue
		}
		out.Corpora = append(out.Corpora, measureCorpus(t, root))
	}
	require.NotEmpty(t, out.Corpora, "control: %s named no usable root", multiLangRootsEnv)

	body, err := json.MarshalIndent(out, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(multiLangArtifact, append(body, '\n'), 0o600))
	t.Logf("wrote %s (%d corpora, knowledge commit %s)", multiLangArtifact, len(out.Corpora), out.Commit)
}

// measureCorpus runs one root through the production pipeline in the production
// ORDER — DiscoverFiles, ChunkFilesParallel, DeduplicateChunks, resolveSlotEdges,
// ReadModulePath and fillBinds, the index build, then resolveEdgesWithStats.
//
// fillBinds is not optional here. This instrument is a SECOND construction site
// of the repo context and does not go through Populate, so without it every
// binds arm returns its zero result on every file — no error, no other row
// moving, and the binds census silently reporting the pre-arm picture.
func measureCorpus(t *testing.T, root string) corpusEntry {
	t.Helper()
	// Sanitized before it reaches discovery: an operator-supplied root is an
	// external input, and the walk joins it with every discovered path.
	root = filepath.Clean(root)
	require.True(t, filepath.IsAbs(root), "%s entries must be absolute paths, got %q", multiLangRootsEnv, root)

	primary := filepath.Base(root)
	require.True(t, registeredLanguage(primary),
		"the corpus directory basename is the primary-language tag and must name a REGISTERED language, got %q", primary)

	ctx := context.Background()
	files, err := DiscoverFiles(ctx, root)
	require.NoError(t, err)
	require.NotEmpty(t, files, "control: discovery found no files under %s", root)

	results, err := ChunkFilesParallel(ctx, root, files)
	require.NoError(t, err)
	require.NotEmpty(t, results, "control: chunking produced no results under %s", root)

	DeduplicateChunks(results)
	resolveSlotEdges(results)
	modulePath, _ := ReadModulePath(root)
	rc := treesitter.RepoContext{Root: root, ModulePath: modulePath, Files: files}
	fillBinds(&rc, results)

	ix := newDeclIndex(0)
	nodeIDs := map[string]bool{}
	ownerLang := map[string]string{} // node ID → the language of the file that declared it
	chunkNodes := 0
	for _, r := range results {
		nodeIDs[r.FilePath] = true
		ownerLang[r.FilePath] = string(r.Language)
		for _, chunk := range r.Chunks {
			if kgtypes.NodeType(chunk.ChunkType).IsComment() {
				continue
			}
			id := ChunkNodeID(chunk)
			nodeIDs[id] = true
			ownerLang[id] = string(r.Language)
			chunkNodes++
			indexDeclaration(ix, r, chunk, id)
		}
	}

	edges, _ := resolveEdgesWithStats(results, ix, nodeIDs)

	byLang := filesByLanguage(results)
	return corpusEntry{
		PrimaryLanguage:     primary,
		CorpusRoot:          root,
		CorpusCommit:        headCommitAt(t, root),
		FilesDiscovered:     len(files),
		ChunkNodes:          chunkNodes,
		FilesByLanguage:     byLang,
		LanguagesWithNoFile: languagesWithNoFile(byLang),
		Languages:           multiLangRows(t, results, ix, edges, ownerLang),
	}
}

// filesByLanguage counts the FILES each language contributed, the same
// derivation corpusReport.countLanguages uses.
func filesByLanguage(results []*treesitter.Result) map[string]int {
	out := map[string]int{}
	for _, r := range results {
		out[string(r.Language)]++
	}
	return out
}

// languagesWithNoFile is every REGISTERED language that contributed no FILE to
// this corpus — a file-derived list, unlike the landed row-derived one.
func languagesWithNoFile(byLang map[string]int) []string {
	absent := []string{}
	for _, lang := range treesitter.RegisteredLanguages() {
		if byLang[string(lang)] == 0 {
			absent = append(absent, string(lang))
		}
	}
	sort.Strings(absent)
	return absent
}

// registeredLanguage reports whether a name is a registered treesitter language.
func registeredLanguage(name string) bool {
	for _, lang := range treesitter.RegisteredLanguages() {
		if string(lang) == name {
			return true
		}
	}
	return false
}

// headCommitAt reads one repository's HEAD.
//
// It is the generalization of knowledgeHeadCommit (corpus_json_test.go), which
// now delegates to it: the single-root harness only ever needed this
// repository's HEAD, while the multi-root walk needs the OTHER repositories' too.
//
// It is a hard failure rather than an empty string: every number this artifact
// carries is attributable to an exact tree, and an entry stamped with a blank
// commit would be a measurement of an unidentified checkout.
func headCommitAt(t *testing.T, dir string) string {
	t.Helper()
	// The dir is an operator-supplied measurement input, already cleaned and
	// required absolute by measureCorpus before it reaches here, and it is passed
	// as a single -C ARGUMENT rather than through a shell — the same treatment
	// the landed baseline read gives its own operator-supplied path.
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output() //nolint:gosec // operator-supplied measurement input
	require.NoError(t, err, "the HEAD of %s must be readable", dir)
	return strings.TrimSpace(string(out))
}
