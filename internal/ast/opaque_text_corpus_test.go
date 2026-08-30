// SPDX-License-Identifier: Apache-2.0

// opaque_text_corpus_test.go — the sampled half of the span-gap census.
//
// WHY THIS EXISTS SEPARATELY FROM THE HERMETIC CENSUS. The probe snippets beside
// it were written by hand, so on their own they prove only that the
// classification covers the constructs their author thought to write. This walks
// real repositories and asserts the SAME verdict table classifies every span-gap
// kind those files actually produce. It is the check that catches a kind no
// snippet happened to contain — and it caught several: Bash's line-continuation
// gaps, C's multi-line preprocessor definitions and Ruby's continued calls are
// all in the table because this walk found them, not because a snippet did.
//
// WHAT THIS IS AND IS NOT — the honest scope, stated rather than implied. This
// is a SAMPLED PARITY CHECK, not a proof over the corpus. The per-language cap is
// perLanguageGapSampleCap files taken in discovery order, which is
// directory-clustered rather than random, so a grammar that starts producing a
// new span-gap kind is LIKELY to be caught and is NOT GUARANTEED to be — the kind
// has to appear in the sampled prefix. Raising the cap is the lever a future
// ticket has, at linear cost. The verdict table is shared with the hermetic
// census on purpose: one classification, measured two ways.

package ast

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// perLanguageGapSampleCap bounds how many of a language's files the partition
// parses. Legible here at its single call site so a future ticket wanting more
// confidence can raise it in one place; the sweep that seeded the verdict table
// used this value and completed in seconds per language.
const perLanguageGapSampleCap = 200

// gapPartitionCells maps each registered language to the fixture repo whose files
// the walk samples. The repos are the corpus_identity_test.go set.
var gapPartitionCells = []extraPartitionCell{
	{lang: treesitter.LangBash, repo: "bash-ohmyzsh"},
	{lang: treesitter.LangC, repo: "c-redis"},
	{lang: treesitter.LangCPP, repo: "cpp-json"},
	{lang: treesitter.LangCSharp, repo: "cs-roslyn"},
	{lang: treesitter.LangElixir, repo: "ex-phoenix"},
	{lang: treesitter.LangElm, repo: "elm-compiler"},
	{lang: treesitter.LangGo, repo: "go-kubernetes"},
	{lang: treesitter.LangGroovy, repo: "groovy-gradle"},
	{lang: treesitter.LangJava, repo: "java-spring"},
	{lang: treesitter.LangJavaScript, repo: "js-react"},
	{lang: treesitter.LangKotlin, repo: "kt-okhttp"},
	{lang: treesitter.LangLua, repo: "lua-openresty"},
	{lang: treesitter.LangOCaml, repo: "ocaml-dune"},
	{lang: treesitter.LangPython, repo: "py-django"},
	{lang: treesitter.LangRuby, repo: "rb-rails"},
	{lang: treesitter.LangRust, repo: "rust-tokio"},
	{lang: treesitter.LangScala, repo: "scala-akka"},
	{lang: treesitter.LangSwift, repo: "swift-vapor"},
	{lang: treesitter.LangTSX, repo: "js-react"},
	{lang: treesitter.LangTypeScript, repo: "ts-vscode"},
}

// TestSpanGapPartition_Corpus asserts every span-gap node kind the fixture corpus
// surfaces for a language carries a verdict in gapVerdicts, or is the ERROR
// exemption. A kind in neither fails, naming the language, the kind, the
// occurrence count and a sample of the uncompared bytes — that failure is the
// only thing standing between a future grammar bump and a new silent wildcard.
func TestSpanGapPartition_Corpus(t *testing.T) {
	// The table's own guard runs first and unconditionally, so it fires even in a
	// no-corpus environment: a partition that only ever asserts over the empty set
	// proves nothing. The floor sits under the fourteen languages the seeding
	// sweep classified, so a gutted table fails here rather than passing silently.
	t.Run("verdict_table_is_populated", func(t *testing.T) {
		require.GreaterOrEqualf(t, len(gapVerdicts), 12,
			"gapVerdicts holds %d languages, below the floor of twelve; a partition asserting only over the empty set proves nothing", len(gapVerdicts))
	})

	homeDir, err := os.UserHomeDir()
	require.NoError(t, err)
	reposDir := filepath.Join(homeDir, "code", "test-repos")
	if _, statErr := os.Stat(reposDir); os.IsNotExist(statErr) {
		t.Skipf("test-repos directory not found at %s — clone repos first", reposDir)
	}

	// The cells run inside a group so the corpus-wide known positive below can
	// observe their total: a t.Parallel subtest runs after its parent function
	// returns, so an assertion written beside the loop would read zero every time.
	var totalGaps atomic.Int64
	t.Run("cells", func(t *testing.T) {
		for _, cell := range gapPartitionCells {
			t.Run(string(cell.lang), func(t *testing.T) {
				t.Parallel()
				runGapPartitionCell(t, reposDir, cell, &totalGaps)
			})
		}
	})

	// THE KNOWN POSITIVE, and it is corpus-wide rather than per-cell for a
	// MEASURED reason: six of the twenty grammars — C++, Java, JavaScript,
	// TypeScript, TSX and Elm — produce no span gap at all across their sampled
	// files, because every byte of their literals and comments is covered by a
	// child. A per-cell "this language found a gap" assertion would therefore be
	// asserting something false about a third of the set. What must hold is that
	// the instrument found gaps SOMEWHERE: every assertion in the cells ranges
	// over the observed set, so a firstContentGap that silently returned nothing
	// would satisfy all of them.
	t.Run("the_walk_measured_gaps_somewhere", func(t *testing.T) {
		require.Positive(t, totalGaps.Load(),
			"the partition observed zero span gaps across every language — that is a broken measurement, not twenty gap-free grammars")
	})
}

// runGapPartitionCell walks one language's fixture repo (capped) and asserts
// every observed span-gap kind is classified. A missing repo or a repo with no
// files of the language is a reasoned SKIP, never a silent pass — a subtest that
// walked nothing must not be counted as one that walked and found everything
// classified.
func runGapPartitionCell(t *testing.T, reposDir string, cell extraPartitionCell, totalGaps *atomic.Int64) {
	t.Helper()
	repoDir := filepath.Join(reposDir, cell.repo)
	if _, err := os.Stat(repoDir); err != nil {
		t.Skipf("fixture repo %s is not present in the corpus", cell.repo)
	}

	files, _, err := discoverScopedFiles(context.Background(), repoDir, cell.lang, Scope{IncludeTests: true})
	require.NoError(t, err)
	if len(files) == 0 {
		t.Skipf("no %s files discovered under %s — nothing to partition", cell.lang, cell.repo)
	}
	if len(files) > perLanguageGapSampleCap {
		files = files[:perLanguageGapSampleCap]
	}

	observed := map[string]int{}
	samples := map[string]string{}
	parser := treesitter.NewParser()
	defer parser.Close()
	parsed, gapped := 0, 0
	for _, rel := range files {
		content, readErr := os.ReadFile(filepath.Join(repoDir, rel)) //nolint:gosec // corpus path, read-only
		if readErr != nil {
			continue
		}
		tree, parseErr := parser.Parse(context.Background(), content, cell.lang)
		if parseErr != nil {
			continue
		}
		parsed++
		walkAllIncludingAnonymous(tree.RootNode(), func(n *sitter.Node) {
			gap, hit := firstContentGap(n, content)
			if !hit {
				return
			}
			gapped++
			observed[n.Type()]++
			if _, dup := samples[n.Type()]; !dup {
				samples[n.Type()] = gap
			}
		})
		tree.Close()
	}
	require.Positivef(t, parsed, "no %s file parsed under %s — the walk measured nothing", cell.lang, cell.repo)
	totalGaps.Add(int64(gapped))
	t.Logf("lang=%s parsed=%d gap_nodes=%d gap_kinds=%d", cell.lang, parsed, gapped, len(observed))

	for kind, count := range observed {
		if kind == errorGapKind {
			continue
		}
		if _, ok := gapVerdicts[cell.lang][kind]; ok {
			continue
		}
		t.Errorf("unclassified span-gap kind for %s: %q observed %d times leaves %q uncompared.\n"+
			"  Classify it in gapVerdicts: if the gap holds the node's own CONTENT it is %q and must join this language's OpaqueTextKinds; "+
			"if it holds only a delimiter, a separator, a continuation marker or an operator, record that verdict instead.",
			cell.lang, kind, count, samples[kind], verdictOpaque)
	}
}
