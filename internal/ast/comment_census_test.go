// SPDX-License-Identifier: Apache-2.0

// comment_census_test.go — the per-grammar comment-kind census, and the corpus
// extras partition that keeps the classification honest.
//
// THE QUESTION TestCommentKindCensus ANSWERS. Comment-transparent alignment
// needs to know, per grammar, which node kinds tree-sitter emits for a comment,
// because those are the kinds the walker will skip on the ordinary alignment
// path. That set is a MEASUREMENT, not folklore — a hand list over 21 grammars
// is exactly what this package refuses everywhere else, and the kinds differ
// grammar to grammar (Go's is `comment`, Rust's splits into line_comment and
// block_comment, Kotlin's block form is `multiline_comment`).
//
// THE PROBE, made hermetic like layout_census_test.go beside it: parse one
// inline snippet per grammar written to contain every comment form that grammar
// has, walk the whole tree, and record every node whose IsExtra() is true with
// its kind and its IsNamed(). The artifact is compared against the fresh
// measurement on EVERY run and a divergence fails, naming the regeneration
// command; the env var only selects a write, so an ordinary suite run never
// dirties testdata.
//
// WHY IsExtra IS THE INSTRUMENT AND NOT THE RUNTIME RULE — the thing most
// likely to be read backwards. IsExtra is grammar-derived and exhaustive, which
// is what a census wants, but it is NOT comment-only in real source (Ruby's
// heredoc_body, C#'s preproc regions and PHP's text_interpolation are all
// extras), which disqualifies it as a matcher predicate. The matcher reads the
// declared CommentKinds set; this census measures the grammar so that set can be
// declared. TestExtraKindPartition_Corpus is the half that holds that line: it
// proves every extra the corpus surfaces is either a declared comment kind or a
// named-as-meaningful non-comment, so the classification can never quietly
// swallow a heredoc.
//
// WHAT THE PARTITION IS AND IS NOT — the honest scope is narrower than the
// obvious reading, so it is stated here rather than implied. TestExtraKindPartition_Corpus
// is a SAMPLED PARITY CHECK, not a proof over the corpus. The per-language cap
// is perLanguageExtraSampleCap files and the walk takes them in discovery order,
// which is directory-clustered rather than random: cs-roslyn alone holds ~14000
// .cs files, so the sample is a small neighboring prefix of it, not a spread. A
// grammar update that starts emitting a new extra kind is therefore LIKELY to be
// caught and is NOT GUARANTEED to be — the kind has to appear in the sampled
// prefix. Raising the cap is the lever a future ticket has, at linear cost.
//
// PERF SHAPE: 21 small parses for the hermetic census, one per grammar, serial;
// the corpus partition walks up to a bounded file count per language as parallel
// per-language subtests, the shape TestCorpusIdentityInvariant already uses.

package ast

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// commentCensusEnv names the environment variable that selects the artifact
// write. Unset means "measure and compare, write nothing", so an ordinary suite
// run never dirties testdata. It mirrors AST_WRAPPER_CENSUS_FILE and
// AST_IDENTITY_CENSUS_FILE: the value is the filename written into testdata/.
const commentCensusEnv = "AST_COMMENT_CENSUS_FILE"

// commentCensusName is the committed artifact under testdata/, and the fixed
// path the self-verifying comparison always reads.
const commentCensusName = "comment_kind_census.txt"

// commentProbe is one grammar's snippet, written to contain every comment form
// the grammar has so the hermetic run sees every kind it can emit.
type commentProbe struct {
	lang treesitter.Language
	src  string
}

// commentProbes covers every registered grammar. Each snippet is a minimal
// valid program carrying that grammar's comment forms and nothing else that
// parses to an extra, so every IsExtra node the walk finds is a comment.
var commentProbes = []commentProbe{
	{lang: treesitter.LangBash, src: "# line\nx=1\n"},
	{lang: treesitter.LangC, src: "// line\n/* block */\nint x;\n"},
	{lang: treesitter.LangCPP, src: "// line\n/* block */\nint x;\n"},
	{lang: treesitter.LangCSharp, src: "// line\n/* block */\nclass C {}\n"},
	{lang: treesitter.LangElixir, src: "# line\nx = 1\n"},
	{lang: treesitter.LangElm, src: "module M exposing (..)\n\n-- line\n\n{- block -}\nx = 1\n"},
	{lang: treesitter.LangGo, src: "package p\n\n// line\n/* block */\nvar x = 1\n"},
	{lang: treesitter.LangGroovy, src: "/** doc */\n// line\n/* block */\ndef x = 1\n"},
	{lang: treesitter.LangJava, src: "// line\n/* block */\nclass C {}\n"},
	{lang: treesitter.LangJavaScript, src: "// line\n/* block */\nvar x = 1;\n"},
	{lang: treesitter.LangKotlin, src: "// line\n/* block */\nfun f() {}\n"},
	{lang: treesitter.LangLua, src: "-- line\n--[[ block ]]\nlocal x = 1\n"},
	{lang: treesitter.LangOCaml, src: "(* comment *)\nlet x = 1\n"},
	{lang: treesitter.LangPython, src: "# line\nx = 1\n"},
	{lang: treesitter.LangRuby, src: "# line\nx = 1\n"},
	{lang: treesitter.LangRust, src: "// line\n/* block */\nfn f() {}\n"},
	{lang: treesitter.LangScala, src: "// line\n/* block */\nobject O\n"},
	{lang: treesitter.LangSwift, src: "// line\n/* block */\nlet x = 1\n"},
	{lang: treesitter.LangTSX, src: "// line\n/* block */\nlet x = 1;\n"},
	{lang: treesitter.LangTypeScript, src: "// line\n/* block */\nlet x = 1;\n"},
}

// commentKindRow is one (language, kind) fact line.
type commentKindRow struct {
	lang  string
	kind  string
	named bool
}

func (r commentKindRow) line() string {
	return fmt.Sprintf("lang=%s kind=%s extra=yes named=%s class=comment", r.lang, r.kind, yesNo(r.named))
}

// commentSummary is the one-per-language roll-up line whose comment_kinds field
// Phase 2's registrations are read straight off.
type commentSummary struct {
	lang  string
	kinds []string
}

func (s commentSummary) line() string {
	return fmt.Sprintf("lang=%s comment_kinds=%s", s.lang, strings.Join(s.kinds, ","))
}

// commentSeeds are the comment-kind sets measured by hand during planning,
// asserted rather than left in prose so a census that disagrees FAILS instead of
// silently replacing them. Every value is sorted, matching the summary line.
// This is the multi-kind guard: a run that flattened every grammar to a single
// kind would fail the multi-kind rows.
var commentSeeds = map[string][]string{
	"bash":       {"comment"},
	"c":          {"comment"},
	"cpp":        {"comment"},
	"csharp":     {"comment"},
	"elixir":     {"comment"},
	"elm":        {"block_comment", "line_comment"},
	"go":         {"comment"},
	"groovy":     {"comment", "groovy_doc"},
	"java":       {"block_comment", "line_comment"},
	"javascript": {"comment"},
	"kotlin":     {"line_comment", "multiline_comment"},
	"lua":        {"comment"},
	"ocaml":      {"comment"},
	"python":     {"comment"},
	"ruby":       {"comment"},
	"rust":       {"block_comment", "line_comment"},
	"scala":      {"block_comment", "comment"},
	"swift":      {"comment", "multiline_comment"},
	"tsx":        {"comment"},
	"typescript": {"comment"},
}

// TestCommentKindCensus measures every registered grammar's comment kinds,
// asserts the planning seeds still hold, and compares the committed artifact
// against the fresh measurement.
func TestCommentKindCensus(t *testing.T) {
	require.Len(t, commentProbes, len(registeredLangs()),
		"every registered grammar needs a comment probe — an unprobed grammar is an unmeasured comment classification")

	var (
		rows      []commentKindRow
		summaries []commentSummary
	)
	for _, probe := range commentProbes {
		probeRows, summary, skip := measureCommentKinds(t, probe)
		if skip != "" {
			// A grammar that cannot be probed is a hard failure here rather than
			// a silent omission: every registered language must contribute a
			// comment_kinds line, and a SKIP would leave the criterion's count of
			// 21 short with no visible cause.
			t.Errorf("lang=%s could not be probed for comment kinds: %s", probe.lang, skip)
			continue
		}
		t.Logf("%s", summary.line())
		rows = append(rows, probeRows...)
		summaries = append(summaries, summary)
	}

	assertCommentSeeds(t, summaries)
	compareCommentCensus(t, rows, summaries)
}

// measureCommentKinds parses one grammar's snippet, walks the tree, and records
// every extra node's kind and namedness. Returns a reasoned skip when the
// snippet does not parse cleanly.
func measureCommentKinds(t *testing.T, probe commentProbe) ([]commentKindRow, commentSummary, string) {
	t.Helper()
	tree, _, ok := parseClean(t, probe.lang, probe.src)
	if !ok {
		return nil, commentSummary{}, "the snippet does not parse cleanly under this grammar"
	}
	defer tree.Close()

	// (kind -> named) deduped across the tree; a snippet carrying two forms of
	// the same kind must record the kind once.
	named := map[string]bool{}
	walkAll(tree.RootNode(), func(n *sitter.Node) {
		if n == nil || !n.IsExtra() {
			return
		}
		named[n.Type()] = n.IsNamed()
	})

	kinds := make([]string, 0, len(named))
	for k := range named {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)

	rows := make([]commentKindRow, 0, len(kinds))
	for _, k := range kinds {
		rows = append(rows, commentKindRow{lang: string(probe.lang), kind: k, named: named[k]})
	}
	return rows, commentSummary{lang: string(probe.lang), kinds: kinds}, ""
}

// assertCommentSeeds compares each grammar's measured comment kinds against the
// planning result. A mismatch is a STOP-AND-REPORT, not a seed edit: either the
// probe snippet is not the construct planning measured, or the grammar moved.
func assertCommentSeeds(t *testing.T, summaries []commentSummary) {
	t.Helper()
	seen := map[string]bool{}
	for _, s := range summaries {
		want, seeded := commentSeeds[s.lang]
		if !seeded {
			t.Errorf("grammar %s produced a census row but has no planning seed — add its measured comment_kinds to commentSeeds", s.lang)
			continue
		}
		seen[s.lang] = true
		if strings.Join(s.kinds, ",") != strings.Join(want, ",") {
			t.Errorf("grammar %s measured comment_kinds=%s, planning measured comment_kinds=%s.\n"+
				"  Do NOT edit the seed to agree with the measurement. Stop and report:\n"+
				"  either the probe snippet is not the construct planning measured, or the grammar moved.",
				s.lang, strings.Join(s.kinds, ","), strings.Join(want, ","))
		}
	}
	for lang := range commentSeeds {
		require.True(t, seen[lang], "seeded grammar %s produced no census row", lang)
	}
}

// compareCommentCensus fails unless the committed artifact matches the fresh
// measurement, and writes it when commentCensusEnv is set.
func compareCommentCensus(t *testing.T, rows []commentKindRow, summaries []commentSummary) {
	t.Helper()
	lines := make([]string, 0, len(rows)+len(summaries))
	for _, r := range rows {
		lines = append(lines, r.line())
	}
	for _, s := range summaries {
		lines = append(lines, s.line())
	}
	sort.Strings(lines)
	want := strings.Join(lines, "\n") + "\n"

	if name := os.Getenv(commentCensusEnv); name != "" {
		require.NoError(t, os.MkdirAll("testdata", 0o750))
		path := filepath.Join("testdata", filepath.Base(name))
		require.NoError(t, os.WriteFile(path, []byte(want), 0o600))
		t.Logf("census written: %s (%d lines)", path, len(lines))
		return
	}

	path := filepath.Join("testdata", commentCensusName)
	got, err := os.ReadFile(path) //nolint:gosec // fixed testdata path
	require.NoError(t, err, "census artifact missing — regenerate with %s=%s", commentCensusEnv, commentCensusName)
	require.Equal(t, want, string(got),
		"census artifact is stale — regenerate with %s=%s go test ./cmd/knowledge/internal/ast/ -run TestCommentKindCensus", commentCensusEnv, commentCensusName)
}

// perLanguageExtraSampleCap bounds how many of a language's files the corpus
// partition parses. The sampling rate is legible here at its single call site
// so a future ticket that wants more confidence can raise it in one place; the
// planning sweep used 300 and completed in seconds per language.
const perLanguageExtraSampleCap = 300

// extraPartitionCell maps a registered language to the fixture repo whose files
// the partition walks. The repos are the corpus_identity_test.go set, but with
// NO prefix: the meaningful extras the table names (Ruby's heredoc_body, C#'s
// preproc regions) sit wherever the corpus put them, and a narrow subtree could
// miss them, so the walk sees the whole repo capped at perLanguageExtraSampleCap
// files.
type extraPartitionCell struct {
	lang treesitter.Language
	repo string
}

var extraPartitionCells = []extraPartitionCell{
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

// knownMeaningfulExtras names, per language, the non-comment node kinds
// tree-sitter reports as extras that carry MEANING and so must never be treated
// as ignorable. It is test-only knowledge by nature: no matcher path reads it,
// and its entire job is to make the partition assertion exhaustive — a heredoc
// body, a preprocessor region or a compiler directive deleted from alignment
// would corrupt a match silently. Putting it in non-test source would imply the
// engine consults it, which it never does.
//
// The set is exactly what the planning corpus sweep measured, minus groovy_doc
// (a comment, on the comment side) and minus ERROR (a recovery artifact, handled
// as its own named exemption below rather than a per-language entry, because a
// language cannot opt out of error recovery).
var knownMeaningfulExtras = map[treesitter.Language]map[string]struct{}{
	treesitter.LangCSharp: {"preproc_endregion": {}, "preproc_nullable": {}, "preproc_pragma": {}, "preproc_region": {}},
	treesitter.LangRuby:   {"heredoc_body": {}},
	treesitter.LangSwift:  {"directive": {}},
	treesitter.LangOCaml:  {"attribute": {}},
}

// errorExtraKind is the parser's error-recovery node. It is exempted from the
// partition everywhere rather than per-language: an ERROR is never a
// grammar-declared extra, and a per-language entry would imply a language could
// opt out of it.
const errorExtraKind = "ERROR"

// TestExtraKindPartition_Corpus asserts that every IsExtra node kind the fixture
// corpus surfaces for a language is either one of that language's declared
// comment kinds (read from the Step 1.1 artifact), an entry in
// knownMeaningfulExtras, or the ERROR exemption. A kind in NONE of these fails
// the test naming the language, the kind and the occurrence count — that is the
// only thing standing between the comment classification and quietly swallowing
// a heredoc body.
func TestExtraKindPartition_Corpus(t *testing.T) {
	// The table's own guard runs first and unconditionally, so it fires even in a
	// no-corpus environment: a partition that only ever asserts over the empty
	// set proves nothing, and the floor of four sits under the five languages the
	// sweep measured so a dropped fixture does not false-pass while a gutted table
	// still does.
	t.Run("meaningful_extras_table_is_populated", func(t *testing.T) {
		require.GreaterOrEqualf(t, len(knownMeaningfulExtras), 4,
			"knownMeaningfulExtras holds %d languages, below the floor of four; a partition asserting only over the empty set proves nothing", len(knownMeaningfulExtras))
	})

	homeDir, err := os.UserHomeDir()
	require.NoError(t, err)
	reposDir := filepath.Join(homeDir, "code", "test-repos")
	if _, statErr := os.Stat(reposDir); os.IsNotExist(statErr) {
		t.Skipf("test-repos directory not found at %s — clone repos first", reposDir)
	}

	commentKinds := loadCommentKindsArtifact(t)

	for _, cell := range extraPartitionCells {
		t.Run(string(cell.lang), func(t *testing.T) {
			t.Parallel()
			runExtraPartitionCell(t, reposDir, cell, commentKinds[string(cell.lang)])
		})
	}
}

// runExtraPartitionCell walks one language's fixture repo (capped) and asserts
// every observed extra kind is classified. A missing repo or a repo with no
// files of the language is a reasoned SKIP, never a silent pass — a subtest that
// walked nothing must not be counted as one that walked and found everything
// classified.
func runExtraPartitionCell(t *testing.T, reposDir string, cell extraPartitionCell, comments map[string]struct{}) {
	t.Helper()
	repoDir := filepath.Join(reposDir, cell.repo)
	if _, err := os.Stat(repoDir); err != nil {
		t.Skipf("fixture repo %s is not present in the corpus", cell.repo)
	}

	files, _, _, err := discoverScopedFiles(context.Background(), repoDir, cell.lang, Scope{IncludeTests: true})
	require.NoError(t, err)
	if len(files) == 0 {
		t.Skipf("no %s files discovered under %s — nothing to partition", cell.lang, cell.repo)
	}
	if len(files) > perLanguageExtraSampleCap {
		files = files[:perLanguageExtraSampleCap]
	}

	// kind -> occurrence count, across the sampled files.
	observed := map[string]int{}
	parser := treesitter.NewParser()
	defer parser.Close()
	parsed := 0
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
		walkAll(tree.RootNode(), func(n *sitter.Node) {
			if n != nil && n.IsExtra() {
				observed[n.Type()]++
			}
		})
		tree.Close()
	}
	require.Positivef(t, parsed, "no %s file parsed under %s — the walk measured nothing", cell.lang, cell.repo)

	meaningful := knownMeaningfulExtras[cell.lang]
	for kind, count := range observed {
		if kind == errorExtraKind {
			continue
		}
		if _, ok := comments[kind]; ok {
			continue
		}
		if _, ok := meaningful[kind]; ok {
			continue
		}
		t.Errorf("unclassified extra kind for %s: %q observed %d times is neither a declared comment kind nor a known meaningful extra.\n"+
			"  Classify it: if it is a comment, it belongs in this language's CommentKinds (regenerate the 1.1 census); if it carries meaning, add it to knownMeaningfulExtras with a reason.",
			cell.lang, kind, count)
	}
}

// loadCommentKindsArtifact parses the Step 1.1 census artifact into a per-language
// set of declared comment kinds. Reading the committed artifact rather than
// LangConfig.CommentKinds keeps this step free of any forward dependency on
// Phase 2 — the artifact stays authoritative because a Phase 2 criterion asserts
// the registrations and the artifact agree.
func loadCommentKindsArtifact(t *testing.T) map[string]map[string]struct{} {
	t.Helper()
	path := filepath.Join("testdata", commentCensusName)
	f, err := os.Open(path) //nolint:gosec // fixed testdata path
	require.NoError(t, err, "comment-kind census artifact missing — run Step 1.1 (%s=%s) first", commentCensusEnv, commentCensusName)
	defer f.Close()

	out := map[string]map[string]struct{}{}
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		line := scan.Text()
		lang, csv, ok := strings.Cut(line, " comment_kinds=")
		if !ok {
			continue
		}
		lang = strings.TrimPrefix(lang, "lang=")
		set := map[string]struct{}{}
		for k := range strings.SplitSeq(csv, ",") {
			if k != "" {
				set[k] = struct{}{}
			}
		}
		out[lang] = set
	}
	require.NoError(t, scan.Err())
	require.NotEmpty(t, out, "no comment_kinds summary lines parsed from %s", path)
	return out
}
