// SPDX-License-Identifier: Apache-2.0

package bm25

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// Environment levers for the corpus generator. It is INERT unless both are set,
// like the benchmark it feeds.
const (
	dictcorpusSrcEnv = "BM25_CORPUS_SRC"
	dictcorpusOutEnv = "BM25_CORPUS_OUT"
)

// TestGenerateDictbenchCorpus writes a serialVersion-2 corpus for the dictionary
// benchmark to measure.
//
// It exists because the corpus that WAS on disk is unreadable: it was written in
// the retired layout, this changeset refuses that layout by design, and the
// encoder for it is deleted. So the measurement corpus has to be built from
// documents, through the shipped Format.Build — which is also what the decision
// step asks for.
//
// The documents are this repository's own source and prose files. That is a
// deliberate choice and its limits should be read with it: the vocabulary,
// term-length distribution and per-document size are real, and the query trace
// the benchmark replays is real engineering traffic ABOUT this repository, so
// the two match. What it is NOT is the 746 MB live snapshot the design
// prototyped against — it is smaller, and any absolute latency taken from it is
// this corpus's number, not that one's. The decision it informs is a RELATIVE
// ordering between three encodings over identical documents, which is the
// comparison that survives the difference.
func TestGenerateDictbenchCorpus(t *testing.T) {
	src, out := os.Getenv(dictcorpusSrcEnv), os.Getenv(dictcorpusOutEnv)
	if src == "" || out == "" {
		t.Skipf("%s and %s must both be set to generate a benchmark corpus", dictcorpusSrcEnv, dictcorpusOutEnv)
	}
	require.NoError(t, os.MkdirAll(out, 0o750)) //nolint:gosec // operator-supplied corpus output dir

	docs := collectRepoDocs(t, src)
	require.Greater(t, len(docs), 500,
		"a corpus of %d documents is too small to separate three dictionary encodings", len(docs))

	// One segment per batch, mirroring how the engine seals: many mid-sized
	// segments rather than one huge one, since the fan-out shape is what a
	// per-(query, segment) page census measures.
	const perSegment = 256
	var segments, bytes int
	for i := 0; i < len(docs); i += perSegment {
		batch := docs[i:min(i+perSegment, len(docs))]
		seg, err := Format{}.Build(batch)
		require.NoError(t, err)
		blob, err := seg.Encode()
		require.NoError(t, err)
		sum := sha256.Sum256(blob)
		name := hex.EncodeToString(sum[:]) + ".seg"
		require.NoError(t, os.WriteFile(filepath.Join(out, name), blob, 0o600)) //nolint:gosec // operator-supplied corpus output dir
		segments++
		bytes += len(blob)
	}
	t.Logf("generated %d documents into %d segments (%d bytes) at %s", len(docs), segments, bytes, out)
}

// collectRepoDocs turns a source tree into indexable documents: one per file,
// with the path as the symbol name and the file's text as the content.
func collectRepoDocs(t *testing.T, root string) []searchengine.Document {
	t.Helper()
	var docs []searchengine.Document
	//nolint:gosec // operator-supplied corpus root
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			if d != nil && d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules") {
				return fs.SkipDir
			}
			return nil //nolint:nilerr // an unreadable entry is skipped, not fatal to corpus generation
		}
		switch filepath.Ext(path) {
		case ".go", ".md", ".yml", ".yaml", ".proto", ".sh":
		default:
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil || info.Size() > 512<<10 {
			return nil //nolint:nilerr // a file that cannot be stat-ed is skipped; a corpus scan is not a transaction
		}
		body, readErr := os.ReadFile(path) //nolint:gosec // operator-supplied corpus root
		if readErr != nil {
			return nil //nolint:nilerr // a file that cannot be read is skipped; a corpus scan is not a transaction
		}
		rel, _ := filepath.Rel(root, path)
		text := string(body)
		docs = append(docs, searchengine.Document{
			ID: rel,
			Fields: map[string]string{
				searchengine.FieldSymbolName: filepath.Base(rel),
				searchengine.FieldSummary:    firstLines(text, 5),
				searchengine.FieldContent:    text,
			},
		})
		return nil
	})
	require.NoError(t, err)
	return docs
}

// firstLines returns the first n lines of s, standing in for the summary a real
// node carries.
func firstLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, " ")
}
