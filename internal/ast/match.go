// SPDX-License-Identifier: Apache-2.0

// Package ast (match executor) — walks files in scope, parses each via a
// per-worker treesitter.Parser, runs the v2 manual walker (matchTree) plus
// the JSON where-tree evaluator (evalWhere), and returns RawMatch results.
//
// Reuse:
//
//   - parser.DiscoverFiles for file walking (git-ls-files / fallback walk).
//   - treesitter.NewParser + treesitter.DetectLanguage for parse + lang
//     detect.
//   - chunker-style worker pool from indexer_chunk.go (NumCPU workers,
//     fileCh, sync.Mutex, withChunkRecover panic-shield) — see
//     match_walk.go for the worker pool itself. ONLY the per-file matcher
//     body changes between v1 and v2.
//
// Per-file failure policy: silent-skip-and-warn via slog. Per-file errors
// (parse, read, ErrParseTimeout) increment WalkStats.FilesSkipped and emit
// a slog.Warn. The walk continues; the caller still gets every successfully-
// parsed file's matches. Mirrors ChunkFiles at parser/indexer_chunk.go.
//
// CGO discipline (smacker issue #181): every Match call applies defer
// Close on the Parser (per-worker), the source Tree (per-file), and the
// pattern PatternTree (per-worker — each worker compiles its own from
// cp.Source so no *Tree crosses a goroutine boundary). Sub-pattern trees
// allocated inside evalWhere are owned by the per-worker sub-pattern
// compile cache; their Close fires at worker exit via closeSubPatternCache.

package ast

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/parser"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// errMatchNilPattern is returned when Match is called with a nil
// CompiledPattern or one whose PatternTree is nil. Callers MUST construct
// a CompiledPattern via Compile before calling Match.
var errMatchNilPattern = errors.New("ast/match: nil CompiledPattern or pattern tree")

// Scope narrows the files walked and caps results. Preserved verbatim from
// the closed-set engine — cmd/knowledge/internal/tools/ast.go::scopeFromArgs
// constructs this directly.
type Scope struct {
	// Repo is informational only — Match operates on repoDir directly.
	Repo string

	// PackagePrefixes restricts the walk to files whose repo-relative path
	// starts with any prefix. Empty means no restriction.
	PackagePrefixes []string

	// IncludeTests, when false, drops paths matching the Go-style _test.go
	// suffix.
	IncludeTests bool

	// Limit caps the total RawMatch slice length. <=0 means engine default.
	Limit int
}

// defaultLimit is applied when Scope.Limit <= 0.
const defaultLimit = 100

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
type RawMatch struct {
	FilePath  string             `json:"file_path"`
	StartLine int                `json:"start_line"`
	EndLine   int                `json:"end_line"`
	Captures  map[string]Capture `json:"captures"`
}

// WalkStats reports per-Match-call walk metrics.
type WalkStats struct {
	FilesScanned int   `json:"files_scanned"`
	FilesSkipped int   `json:"files_skipped"`
	DurationMS   int64 `json:"duration_ms"`
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

	if cp == nil || cp.Tree == nil {
		return nil, stats, errMatchNilPattern
	}

	files, err := discoverScopedFiles(ctx, repoDir, lang, scope)
	if err != nil {
		return nil, stats, err
	}

	limit := scope.Limit
	if limit <= 0 {
		limit = defaultLimit
	}

	// cp is used here only for the nil-gate above and as the DSL-source
	// reader below — it is never walked by a worker. Each worker re-compiles
	// cp.Source into its own pattern tree + sub-pattern cache so no
	// tree-sitter *Tree is shared across goroutines (see runWorkers).
	matches, scanned, skipped, walkErr := runWorkers(ctx, runWorkerArgs{
		repoDir:       repoDir,
		lang:          lang,
		patternSource: cp.Source,
		where:         where,
		files:         files,
		limit:         limit,
	})
	stats.FilesScanned = scanned
	stats.FilesSkipped = skipped
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
// evalWhere into one worker's cache. Called at worker exit (deferred in the
// runWorkers worker closure). Each worker owns its own cache, so the lock
// is uncontended here — it is held only to satisfy the map's access
// discipline shared with getOrCompileSubPattern.
func closeSubPatternCache(cache map[string]*PatternTree, mu *sync.Mutex) {
	mu.Lock()
	defer mu.Unlock()
	for _, pt := range cache {
		pt.Close()
	}
}

// discoverScopedFiles delegates to parser.DiscoverFiles and applies scope
// filters: language match, PackagePrefixes prefix-match, and the test-file
// suffix drop when IncludeTests is false.
func discoverScopedFiles(ctx context.Context, repoDir string, lang treesitter.Language, scope Scope) ([]string, error) {
	all, err := parser.DiscoverFiles(ctx, repoDir)
	if err != nil {
		return nil, fmt.Errorf("ast/match: discover files: %w", err)
	}
	out := make([]string, 0, len(all))
	for _, rel := range all {
		if treesitter.DetectLanguage(rel) != lang {
			continue
		}
		if !matchesPackagePrefixes(rel, scope.PackagePrefixes) {
			continue
		}
		if !scope.IncludeTests && isGoTestFile(rel) {
			continue
		}
		out = append(out, rel)
	}
	return out, nil
}

// matchesPackagePrefixes returns true when rel begins with any provided
// prefix. Empty prefix slice means no restriction.
func matchesPackagePrefixes(rel string, prefixes []string) bool {
	if len(prefixes) == 0 {
		return true
	}
	for _, p := range prefixes {
		if strings.HasPrefix(rel, p) {
			return true
		}
	}
	return false
}

// isGoTestFile returns true for repo-relative Go test files
// (foo_test.go).  Other languages' test conventions are not filtered here
// yet — per-language predicates land in Phase 5.
func isGoTestFile(rel string) bool {
	return strings.HasSuffix(rel, "_test.go")
}

// CompiledPattern is the engine-internal pattern representation. The v2
// shape carries the PatternTree directly; Close releases it.
//
// The struct is preserved (not renamed) so cmd/knowledge's `defer
// cp.Close()` pattern keeps building.
type CompiledPattern struct {
	// Tree is the v2 PatternTree built by Compile.
	Tree *PatternTree
	// Source retains the raw DSL source for explain/debug output and as the
	// re-compile source for Match's per-worker pattern compilation (each
	// worker re-Parses+Compiles Source so no *Tree is shared across
	// goroutines).
	Source string
	// RootKind is the effective root node's grammar type (e.g.
	// "defer_statement", "call_expression"). Empty when the root is a
	// placeholder — the prefilter is skipped in that case.
	RootKind string
	// rootQuery is a pre-compiled tree-sitter query like
	// `(defer_statement) @root` that finds candidate nodes using the C
	// engine's internal per-type indexing — avoiding a full AST walk and
	// the ~1GB of cachedNode allocations that walk triggers. Nil when the
	// pattern root is a placeholder (any type could match).
	rootQuery *sitter.Query
	// rootDescended is true when effectivePatternNode descended the root
	// through single-named-child wrappers. When true, the candidate
	// prefilter must apply effectiveTargetNode before comparing kinds.
	rootDescended bool
}

// Close releases the embedded PatternTree. Nil-safe.
func (cp *CompiledPattern) Close() {
	if cp == nil {
		return
	}
	if cp.rootQuery != nil {
		cp.rootQuery.Close()
		cp.rootQuery = nil
	}
	if cp.Tree != nil {
		cp.Tree.Close()
		cp.Tree = nil
	}
}

// Compile turns a parsed Pattern into a CompiledPattern by resolving the
// language config and invoking compilePattern. Callers MUST defer
// cp.Close() on the returned value.
//
// The signature is stable — cmd/knowledge/internal/tools/ast.go::handleAstMatch
// calls Compile(pat, lang).
func Compile(pat Pattern, lang treesitter.Language) (*CompiledPattern, error) {
	if isDeniedLanguage(lang) {
		return nil, errLanguageNotSupported(lang)
	}
	cfg, ok := langConfigFor(lang)
	if !ok {
		return nil, errLanguageNotSupported(lang)
	}
	pt, err := compilePattern(context.Background(), pat, cfg)
	if err != nil {
		return nil, err
	}
	cp := &CompiledPattern{Tree: pt, Source: pat.Source}
	initRootQuery(cp, pt, lang)
	return cp, nil
}

// initRootQuery sets RootKind and compiles the tree-sitter query used to
// find candidate nodes without a full AST walk. Skipped when the pattern
// root is a placeholder (any node type could match).
func initRootQuery(cp *CompiledPattern, pt *PatternTree, lang treesitter.Language) {
	if pt.Root == nil {
		return
	}
	eff := effectivePatternNode(pt.Root)
	if _, isPlaceholder := lookupPlaceholder(pt, eff); isPlaceholder {
		return
	}
	cp.RootKind = eff.Type()
	cp.rootDescended = eff != pt.Root
	grammar, ok := treesitter.LanguageGrammar(lang)
	if !ok {
		return
	}
	sexpr := fmt.Sprintf("(%s) @root", cp.RootKind)
	if q, err := sitter.NewQuery([]byte(sexpr), grammar); err == nil {
		cp.rootQuery = q
	}
}
