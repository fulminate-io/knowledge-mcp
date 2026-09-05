// SPDX-License-Identifier: Apache-2.0

// Package ast (match executor) — walks files in scope, parses each via a
// per-worker treesitter.Parser, runs the v2 manual walker (matchTree) plus
// the JSON where-tree evaluator (evalWhere), and returns RawMatch results.
//
// Reuse:
//
//   - parser.DiscoverFilesReporting for file walking (git-ls-files / fallback
//     walk), which also reports what discovery declined and under which rule.
//   - treesitter.NewParser + treesitter.DetectLanguage for parse + lang
//     detect.
//   - chunker-style worker pool from indexer_chunk.go (NumCPU workers,
//     fileCh, sync.Mutex, withChunkRecover panic-shield) — see
//     match_walk.go for the worker pool itself. ONLY the per-file matcher
//     body changes between v1 and v2.
//
// Per-file failure policy: silent-skip-and-warn via slog. Per-file errors
// (read, parse, ErrParseTimeout) each increment their OWN by-cause counter —
// whose sum is WalkStats.FilesSkipped — and emit a slog.Warn. The walk
// continues; the caller still gets every successfully-parsed file's matches.
// Mirrors ChunkFiles at parser/indexer_chunk.go.
//
// CGO discipline (smacker issue #181): every Match call applies defer
// Close on the Parser (per-worker), the source Tree (per-file), and every
// compiled variant's PatternTree (per-worker — each worker compiles its own
// set from cp.Source so no *Tree crosses a goroutine boundary). Sub-pattern trees
// allocated inside evalWhere are owned by the per-worker sub-pattern
// compile cache; their Close fires at worker exit via closeSubPatternCache.

package ast

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/parser"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// errMatchNilPattern is returned when Match is called with a nil
// CompiledPattern or one carrying no compiled variants. Callers MUST construct
// a CompiledPattern via Compile before calling Match.
var errMatchNilPattern = errors.New("ast/match: nil CompiledPattern or pattern tree")

// Scope narrows the files walked. It NEVER caps the walk: every file the
// filters admit is parsed, and every match found is returned. The ast tool's
// `limit` argument is a RENDER bound owned entirely by the tool layer
// (cmd/knowledge/internal/tools/ast.go).
//
// THE TOOL LAYER IS NOT THE ONLY CONSTRUCTOR, and a comment here once said it
// was. The corpus-check scanner builds this struct twice — once for the walk it
// runs per check and once for the scope it hands the zero-scan hint — and the
// check-fixture runner builds it a third time. A caller reading the retired
// claim would conclude that a change to the tool layer's argument handling
// reaches every walk, which it does not.
type Scope struct {
	// Repo is informational only — Match operates on repoDir directly.
	Repo string

	// PackagePrefixes restricts the walk to files whose repo-relative path
	// starts with any prefix. Empty means no restriction.
	PackagePrefixes []string

	// IncludeTests, when false, drops the paths this language's own test-file
	// convention claims — LangConfig.IsTestFile, so the filter means the same
	// thing in Ruby as it does in Go. A language whose convention is nil filters
	// nothing here, because there is nothing to filter BY; the tool layer refuses
	// an explicit include_tests for those up front (tools.validateIncludeTests),
	// so a caller cannot reach this path believing a control is in force.
	IncludeTests bool

	// LiftExclusions walks the files discovery's rule chain would otherwise
	// decline — markdown, lockfiles, generated Go, vendored paths, unsupported
	// languages, oversize files, and (on the non-git walk) pruned directories.
	// It does NOT lift .gitignore, which is the repo's own configuration rather
	// than a rule ast chose, and it does NOT lift the filters in this struct:
	// the language match, PackagePrefixes and IncludeTests are the caller's own
	// narrowing and still apply to whatever discovery returns.
	LiftExclusions bool

	// EmitParseHint, set ONLY on the replace path, asks the walk to record a
	// per-matched-file parse hint {clean, size, digest} into WalkStats.CleanHint
	// so ApplyReplace can skip re-parsing a file the match already parsed clean.
	// The match and count read paths leave it false and compute no digest, so the
	// fnv64a pass is paid only where it saves a tree-sitter parse.
	EmitParseHint bool
}

// Capture is one named binding from a match. The v2 shape carries:
//
//   - Text — the captured node's source text. For sequence captures,
//     the verbatim source slice from the first matched sibling's
//     StartByte to the last matched sibling's EndByte (per Q4: no
//     judgment about leading / trailing whitespace).
//   - Kind — the tree-sitter node kind (e.g. "identifier"). Empty for
//     sequence captures (the children carry kinds).
//   - Children — per-sibling Capture views for sequence captures. Nil
//     for single-node captures.
//   - Line — 1-based start line of the capture's first byte.
//   - StartByte / EndByte — outer byte span of the capture in the source
//     file (used by B.4's same_node leaf for sequence captures).
//
// JSON tags use omitempty for kind + children so single-node and
// sequence-zero captures render compactly.
type Capture struct {
	Text      string    `json:"text"`
	Kind      string    `json:"kind,omitempty"`
	Children  []Capture `json:"children,omitempty"`
	Line      int       `json:"line"`
	StartByte uint32    `json:"start_byte,omitempty"`
	EndByte   uint32    `json:"end_byte,omitempty"`
}

// RawMatch is one match localized to a file. Captures is keyed by
// placeholder name (without leading $); the special "match" key is
// reserved for the outer-node binding.
//
// Alignment is the complementary record: where Captures covers the positions
// the USER named, Alignment covers every LITERAL pattern token, mapping its
// pattern-side byte range to the source range it matched. Ordered by pattern
// position, placeholder positions excluded. See alignment.go.
type RawMatch struct {
	FilePath  string             `json:"file_path"`
	StartLine int                `json:"start_line"`
	EndLine   int                `json:"end_line"`
	Captures  map[string]Capture `json:"captures"`
	Alignment []TokenAlign       `json:"alignment,omitempty"`
	// DroppedSpans are the PATTERN-side byte ranges this match's sequence
	// promotions threw away — the same coordinate space as
	// TokenAlign.PatStart/PatEnd. A token in one of them was never compared
	// against the target, so it carries no alignment entry; the splice reads
	// them to consume a template token that repeats one instead of emitting it
	// beside source that already carries its own (splice.go).
	DroppedSpans []byteRange `json:"dropped_spans,omitempty"`
	// CommentSpans are the SOURCE-side byte ranges of the comments the ORDINARY
	// alignment path skipped so a comment-free pattern could reach a
	// comment-carrying body (walker.go). Unlike every other byte inside the
	// match they are covered by no alignment entry and no capture — a pattern
	// token was compared against neither — so at the splice they are
	// indistinguishable from bytes the caller is rewriting. splice.go reads them
	// to keep a comment at the EDGE of a rewritten region out of the replaced
	// span; a comment strictly interior to the rewrite is left to the caller's
	// template like any other interior byte.
	CommentSpans []byteRange `json:"comment_spans,omitempty"`
	// CompiledKind and CompiledContexts disclose WHICH compiled variant found
	// this match: the grammar type its pattern root carries, and EVERY
	// caller-facing context (decl / stmt / expr / member) that compiled to
	// that tree. A pattern expressible in two contexts matches the union of
	// both, and without the stamp a caller cannot tell which construct a
	// result came from — nor see that a surprising zero came from compiling
	// into a context they did not mean. The contexts are plural because a
	// fragment legal in several of them often compiles identically under each;
	// naming only the first would report the registry's order as a property of
	// the pattern. CompiledKind is empty when the variant's root is a
	// placeholder, which matches any kind.
	CompiledKind     string   `json:"compiled_kind,omitempty"`
	CompiledContexts []string `json:"compiled_contexts,omitempty"`
}

// WalkStats reports per-Match-call walk metrics.
//
// FilesScanned and FilesSkipped account only for files discovery HANDED to the
// walk. The four exclusion fields account for the files it never handed over,
// attributed to the rule that declined each one, so a caller can tell a file
// that produced no match from a file that was never read. They describe
// discovery's own rule chain only: files this Scope's language / prefix /
// test-file filters dropped are not exclusions in this sense, since the caller
// asked for those. The test-file drop is reported instead by TestFilesExcluded
// below, which is a separate stats field and deliberately NOT a new exclusion
// rule: folding it into the report would hand a caller their own request back
// as an exclusion and change excluded_by_rule under a flag that excludes
// nothing.
//
// FilesSkipped is the exact SUM of the three by-cause skip counters below, and
// is computed from them (walkCounters.skippedTotal) rather than tracked
// separately, so the total can never drift from its decomposition. Callers
// keying on files_skipped read the same number they always did; only its
// breakdown is new.
type WalkStats struct {
	FilesScanned int   `json:"files_scanned"`
	FilesSkipped int   `json:"files_skipped"`
	DurationMS   int64 `json:"duration_ms"`
	// SkippedRead, SkippedParseError and SkippedParseLimit decompose
	// FilesSkipped by cause: a file the walk could not READ, one the parser
	// rejected, and one the parser abandoned at its wall-clock operation
	// limit. Three very different operational stories that a single total
	// told identically.
	SkippedRead       int `json:"skipped_read"`
	SkippedParseError int `json:"skipped_parse_error"`
	SkippedParseLimit int `json:"skipped_parse_limit"`
	// FilesWithParseErrors and MatchesFromDegradedTrees describe files that
	// were NOT skipped: the parse succeeded, but tree-sitter had to
	// error-recover, so the tree carries ERROR nodes. Those files are walked
	// and their matches are returned unfiltered — the ticket's rule is report,
	// do not guess — and these two counters are the report. A caller seeing a
	// non-zero matches_from_degraded_trees knows that many results were read
	// off a tree the grammar did not fully accept.
	//
	// NEITHER DETECTS A WRONG TREE BUILT FROM CORRUPTED LEXER STATE, which is
	// a different failure: a grammar whose external scanner keeps its lexer
	// state in process-global variables returns a structurally different tree
	// under concurrency, and it does so with the parse succeeding, HasError
	// false, and nothing skipped. These counters make degraded parses visible;
	// they do not make parses correct.
	FilesWithParseErrors     int `json:"files_with_parse_errors"`
	MatchesFromDegradedTrees int `json:"matches_from_degraded_trees"`
	// TestFilesScanned and TestFilesExcluded report what Scope.IncludeTests did,
	// in both directions, and they are COMPLEMENTS rather than one fact told
	// twice: the first is zero exactly when the filter is on and the second is
	// zero exactly when it is off, and neither is recoverable from the other
	// without a corpus total this walk does not report. A consumer saying "this
	// run reached tests" reads the first; one explaining a zero scan reads the
	// second, which is the only place the cause exists at all.
	//
	// THEY ARE COUNTED AT DISCOVERY, where the filter lives, so they count files
	// ADMITTED TO the walk rather than files the parser then got through.
	// FilesScanned is counted at the other end, by the workers, so a test file
	// the walk could not read is counted here AND in FilesSkipped. The gap is
	// exactly the by-cause skip counters and is never larger.
	//
	// A language with no test-file convention filters nothing, so both read zero
	// however IncludeTests is set — an honest report of a filter that had no
	// vocabulary to act on, which is why the tool layer refuses an explicit
	// include_tests for those languages up front rather than letting a caller
	// read the zeros as an answer.
	TestFilesScanned  int `json:"test_files_scanned"`
	TestFilesExcluded int `json:"test_files_excluded"`
	// ExcludedByRule counts, per rule name, the candidates discovery declined.
	// Counts are exact. A rule present with zero ran and declined nothing; a
	// rule ABSENT never executed on the discovery path that ran — the two are
	// different facts, and DiscoveryPath below says which path that was.
	ExcludedByRule map[string]int `json:"excluded_by_rule,omitempty"`
	// ExcludedSamples names a bounded sample of the declined paths per rule.
	ExcludedSamples map[string][]string `json:"excluded_samples,omitempty"`
	// ExcludedTruncated marks a rule whose declined set outran the sample cap,
	// so "three names" is never read as "three files".
	ExcludedTruncated map[string]bool `json:"excluded_truncated,omitempty"`
	// DiscoveryPath names the discovery path that produced the report, carrying
	// a lifted marker when Scope.LiftExclusions suppressed the rule chain — a
	// run that was not allowed to exclude anything is not a run that had nothing
	// to exclude.
	DiscoveryPath string `json:"discovery_path,omitempty"`
	// CleanHint carries, for the REPLACE path only (Scope.EmitParseHint), a
	// per-matched-file parse hint {clean, size, digest} captured at match time so
	// ApplyReplace can skip the pre-edit re-parse of a file the match already
	// parsed clean. json:"-": it is internal match→replace plumbing, never
	// marshaled, so the wire response shape is unchanged. Nil on the match and
	// count read paths, which leave EmitParseHint false and compute no digest.
	CleanHint map[string]fileParseHint `json:"-"`
}

// MergeCleanHint folds src's CleanHint into dst's, allocating dst's map on first
// use. It exists so the tool layer's mergeWalkStats can union the hint across
// alternation members without naming the unexported fileParseHint type: a
// replace over several sibling patterns collects a hint per member, and the last
// writer for a path wins (every member sees the same file bytes, so the hint is
// identical anyway).
func MergeCleanHint(dst *WalkStats, src WalkStats) {
	if len(src.CleanHint) == 0 {
		return
	}
	if dst.CleanHint == nil {
		dst.CleanHint = make(map[string]fileParseHint, len(src.CleanHint))
	}
	maps.Copy(dst.CleanHint, src.CleanHint)
}

// Match walks every file under repoDir matching lang+scope, parses each
// with a per-worker treesitter.Parser, runs the v2 walker over each
// candidate node, applies the where-tree filter (when set), and returns
// the aggregated RawMatch slice plus walk metrics. Per-file errors are
// logged and silently skipped; only context cancellation aborts.
//
// where may be nil — meaning "no filter, pure structural match".
//
// The signature deliberately widens from Phase A's 5-arg shape: the new
// `where *WhereNode` parameter is owned by the consumer surface
// (cmd/knowledge handles ParseWhere off the MCP arguments). Phase B' wires
// the consumer side; for the duration of Phase B the consumer surface
// passes nil.
func Match(
	ctx context.Context,
	repoDir string,
	lang treesitter.Language,
	cp *CompiledPattern,
	where *WhereNode,
	scope Scope,
) ([]RawMatch, WalkStats, error) {
	start := time.Now()
	stats := WalkStats{}

	if cp == nil || len(cp.Variants) == 0 {
		return nil, stats, errMatchNilPattern
	}

	files, report, tests, err := discoverScopedFiles(ctx, repoDir, lang, scope)
	if err != nil {
		return nil, stats, err
	}
	tests.applyTo(&stats)
	stats.ExcludedByRule = report.ExcludedByRule
	stats.ExcludedSamples = report.ExcludedSamples
	stats.ExcludedTruncated = report.ExcludedTruncated
	stats.DiscoveryPath = report.DiscoveryPath

	// cp is used here only for the nil-gate above and as the DSL-source
	// reader below — it is never walked by a worker. Each worker re-compiles
	// cp.Source into its own pattern tree + sub-pattern cache so no
	// tree-sitter *Tree is shared across goroutines (see runWorkers).
	matches, cleanHints, counters, walkErr := runWorkers(ctx, runWorkerArgs{
		repoDir:       repoDir,
		lang:          lang,
		patternSource: cp.Source,
		pinContext:    cp.pin,
		where:         where,
		files:         files,
		emitParseHint: scope.EmitParseHint,
	})
	counters.applyTo(&stats)
	stats.CleanHint = cleanHints
	stats.DurationMS = time.Since(start).Milliseconds()

	if ctx.Err() != nil {
		return matches, stats, ctx.Err()
	}
	if walkErr != nil {
		return matches, stats, walkErr
	}
	return matches, stats, nil
}

// closeSubPatternCache releases every sub-pattern PatternTree allocated by
// evalWhere into one worker's cache — every variant of every cached source,
// since a sub-pattern compiles to the same union an outer pattern does.
// Called at worker exit (deferred in the runWorkers worker closure). Each
// worker owns its own cache, so the lock is uncontended here — it is held only
// to satisfy the map's access discipline shared with getOrCompileSubPattern.
func closeSubPatternCache(cache map[string][]patternVariant, mu *sync.Mutex) {
	mu.Lock()
	defer mu.Unlock()
	for _, variants := range cache {
		closeVariants(variants)
	}
}

// testFileTally is what the test-file branch of the scope filter did: how many
// of this language's test files were admitted to the walk, and how many were
// dropped. It is a third return rather than two more ints because the two
// numbers are one fact about one branch and a caller that reads one without the
// other cannot tell "the filter was off" from "there are no test files here".
type testFileTally struct {
	scanned  int
	excluded int
}

// applyTo copies the tally onto the stats the walk reports.
func (t testFileTally) applyTo(s *WalkStats) {
	s.TestFilesScanned = t.scanned
	s.TestFilesExcluded = t.excluded
}

// discoverScopedFiles delegates to parser.DiscoverFilesReporting and applies
// scope filters: language match, PackagePrefixes prefix-match, and the
// per-language test-file drop when IncludeTests is false.
//
// It returns discovery's exclusion report alongside the files so the walk can
// disclose what it never saw. The report is discovery's own and is deliberately
// NOT extended with the scope filters applied below: those are the caller's
// narrowing, and folding them in would report a caller's own request back to
// them as an exclusion. The test-file branch is reported through the SEPARATE
// tally instead, for the same reason.
func discoverScopedFiles(ctx context.Context, repoDir string, lang treesitter.Language, scope Scope) ([]string, parser.DiscoveryReport, testFileTally, error) {
	all, report, err := parser.DiscoverFilesReporting(ctx, repoDir, parser.DiscoveryOptions{
		LiftExclusions:  scope.LiftExclusions,
		PackagePrefixes: scope.PackagePrefixes,
	})
	if err != nil {
		return nil, report, testFileTally{}, fmt.Errorf("ast/match: discover files: %w", err)
	}
	// The language config is loop-invariant — one language per call — and
	// langConfigFor takes a registry read lock, so it is resolved once here
	// rather than per candidate path. A miss leaves isTest nil, which filters
	// nothing: an unregistered language reaches a hard error at Compile, and a
	// registered one with no convention is refused at the tool layer.
	var isTest func(string) bool
	if cfg, ok := langConfigFor(lang); ok {
		isTest = cfg.IsTestFile
	}
	out := make([]string, 0, len(all))
	var tests testFileTally
	for _, rel := range all {
		if treesitter.DetectLanguage(rel) != lang {
			continue
		}
		if !matchesPackagePrefixes(rel, scope.PackagePrefixes) {
			continue
		}
		// THE BRANCH IS SPLIT RATHER THAN SHORT-CIRCUITED so both sides are
		// counted. The retired one-line condition could only ever observe the
		// drop; the admitted test file — the number a caller needs to tell a run
		// that reached tests from one that did not — was invisible to it.
		if isTest != nil && isTest(rel) {
			if !scope.IncludeTests {
				tests.excluded++
				continue
			}
			tests.scanned++
		}
		out = append(out, rel)
	}
	return out, report, tests, nil
}

// matchesPackagePrefixes reports whether rel is admitted by the caller's
// package_prefixes, at PATH-SEGMENT boundaries: "pkg" means the pkg directory
// and everything under it, never the sibling pkgextra, and a prefix naming a
// single file matches that file. Empty prefix slice means no restriction.
//
// It delegates to parser.MatchesPathPrefixes rather than testing prefixes here,
// because discovery applies the same prefixes while PRUNING (git pathspecs on
// the git path, a directory prune on the walk) and the two answers must be the
// same answer. This predicate is the second, redundant application over an
// already-pruned set — kept because the pruning is discovery's optimization and
// this is the scope's own guarantee, and the two should not be one line of
// refactoring apart from disagreeing. It was a bare strings.HasPrefix until this
// change, which is how a scope of "django/contrib/admin" also rewrote
// django/contrib/admindocs.
func matchesPackagePrefixes(rel string, prefixes []string) bool {
	return parser.MatchesPathPrefixes(rel, prefixes)
}
