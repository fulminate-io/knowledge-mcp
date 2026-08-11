// SPDX-License-Identifier: Apache-2.0

// Package ast (result formatter + enclosing-node resolver) — turns the
// RawMatch slice from Match into the LLM-facing MatchResults shape with
// enclosing_node_id / enclosing_signature fields populated against the code
// graph.
//
// Reuse / shape: mirrors domains/topology/dead_code_review.go:96-145 verbatim.
// Builds a per-call codeNodeIndex (no global cache — a cache shared across
// calls would serve stale enclosing-node data after a re-collect) by iterating
// function-ish nodes via the
// HydratorBackend abstraction and indexing them by (file_path, start_line).
// The backend (graphClientHydratorBackend) owns the function-ish type filter
// client-side, so this file no longer pins its own NodeType list.
//
// Off-by-one tolerance: tree-sitter row counts can differ by 1 from
// go/ast positions because tree-sitter is 0-based and go/ast is 1-based —
// dead_code_review.go addresses the same shape with `for _, delta := range
// []int{0, -1, 1}` (mapOneDeadFunc:155-160). We mirror that ladder here.
//
// HydratorBackend abstraction: the binary split (corrective
// rework) moved the ast tool from server-side to client-side. The server has
// the code graph; the client has the source files. Hydrate takes a
// HydratorBackend interface so each side can supply its own enumeration: the
// production client-side graphClientHydratorBackend (cmd/knowledge/internal/
// tools/ast_hydrator.go) issues a bounded file_symbols query over the wire; the
// NoOpBackend returns zero nodes when the client has no code-graph access. The
// backend emits *knowledgev1.Node values (the wire proto) — the P2-T5 retype
// dropped the server-side node wrapper from the client read path.

package ast

import (
	"context"
	"fmt"
	"sort"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// HydratorBackend abstracts enumeration of function-ish code-graph nodes for
// Hydrate's enclosing-node lookup. The production adapter
// (graphClientHydratorBackend) scopes a file_symbols query to the raw match
// set's files; the NoOpBackend is a no-op (the client has no code-graph
// access). Both emit *knowledgev1.Node values via the visitor callback so
// Hydrate can build its codeNodeIndex without retaining a slice in memory.
//
// Implementations MUST emit only function-ish nodes (function_declaration,
// method_declaration, etc.) — Hydrate does not re-filter. files is the unique
// set of file paths from the raw match set; backends that can scope their walk
// MUST honor it. Empty files = walk all (or no-op for backends with nothing to
// emit).
type HydratorBackend interface {
	IterateFunctionish(ctx context.Context, files []string, fn func(*knowledgev1.Node) error) error
}

// NoOpBackend is a HydratorBackend that emits zero nodes. The client-side
// ast intercept uses this when the client doesn't carry the code graph
// in-process — enclosing-node IDs are unavailable without an extra RPC, which
// the corrective rework explicitly avoids.
type NoOpBackend struct{}

// IterateFunctionish returns nil immediately — the client has no nodes to
// emit. Hydrate then leaves EnclosingNodeID + EnclosingSignature empty for
// every match.
func (NoOpBackend) IterateFunctionish(_ context.Context, _ []string, _ func(*knowledgev1.Node) error) error {
	return nil
}

// MatchResult is one hydrated match with the code-graph node ID + signature
// of the enclosing function/method (when one was found in the code graph).
type MatchResult struct {
	FilePath           string             `json:"file_path"`
	StartLine          int                `json:"start_line"`
	EndLine            int                `json:"end_line"`
	Captures           map[string]Capture `json:"captures"`
	EnclosingNodeID    string             `json:"enclosing_node_id,omitempty"`
	EnclosingSignature string             `json:"enclosing_signature,omitempty"`
	// CompiledKind and CompiledContexts carry the RawMatch stamp through to
	// the caller: the grammar type the matching variant compiled to, and every
	// context that compiled to it. Two results with different kinds mean the
	// pattern was grammatical in two constructs and matched both.
	CompiledKind     string   `json:"compiled_kind,omitempty"`
	CompiledContexts []string `json:"compiled_contexts,omitempty"`
}

// CompiledVariant is one candidate of the union, described for the caller: the
// tree it compiled to, every context that produced that tree, the wrapper names
// behind those contexts, and the pattern text the hosting rules absorbed.
//
// Absorbed carries the absorbed tokens' TEXTS rather than their offsets,
// because the text is the actionable part: a caller who sees [";"] learns the
// `;` they typed is owned by the enclosing construct and is not constraining
// the match. The wrapper-side out-of-span spans are deliberately absent — they
// are bytes of a wrapper the caller never wrote and could not act on.
type CompiledVariant struct {
	Contexts []string `json:"contexts"`
	Wrappers []string `json:"wrappers,omitempty"`
	RootKind string   `json:"root_kind,omitempty"`
	Absorbed []string `json:"absorbed,omitempty"`
	// Reason is EMPTY for every variant rendered under `compiled` — a surviving
	// candidate has nothing to explain. It is populated only for a NARROWED
	// entry (DescribeNarrowed), naming why the member reading was dropped and the
	// context:"member" remedy that restores it.
	Reason string `json:"reason,omitempty"`
}

// Stats merges Match's WalkStats with Hydrate's own metrics. FilesSkipped
// passes through verbatim from WalkStats so callers can detect lossy walks, and
// so do the three by-cause skip counters that decompose it, the two
// degraded-parse counters, and the four exclusion fields — a caller reading a
// zero result needs to know which files discovery declined, which the walk could
// not read or parse, and which parsed only after error recovery, before
// concluding the pattern found nothing.
//
// The field list mirrors WalkStats exactly: Hydrate converts one to the other,
// so a field added to either without the other stops that conversion compiling.
// CleanHint rides along for that mirror only — it is replace-path plumbing,
// always nil on the match+Hydrate path (which never sets EmitParseHint), and is
// tagged json:"-" on both structs so it never reaches the wire.
type Stats struct {
	FilesScanned             int                      `json:"files_scanned"`
	FilesSkipped             int                      `json:"files_skipped"`
	DurationMS               int64                    `json:"duration_ms"`
	SkippedRead              int                      `json:"skipped_read"`
	SkippedParseError        int                      `json:"skipped_parse_error"`
	SkippedParseLimit        int                      `json:"skipped_parse_limit"`
	FilesWithParseErrors     int                      `json:"files_with_parse_errors"`
	MatchesFromDegradedTrees int                      `json:"matches_from_degraded_trees"`
	ExcludedByRule           map[string]int           `json:"excluded_by_rule,omitempty"`
	ExcludedSamples          map[string][]string      `json:"excluded_samples,omitempty"`
	ExcludedTruncated        map[string]bool          `json:"excluded_truncated,omitempty"`
	DiscoveryPath            string                   `json:"discovery_path,omitempty"`
	CleanHint                map[string]fileParseHint `json:"-"`
}

// MatchResults is the LLM-facing output of a Match+Hydrate round-trip. Hint
// is populated when len(Matches) == 0 with the ticket-specified guidance text.
// WalkedRoot echoes the directory the walk actually ran over so the caller can
// tell which tree produced (or failed to produce) the matches; the handler
// populates it from the resolved repoDir.
type MatchResults struct {
	Matches []MatchResult `json:"matches"`
	// Total is the FULL-WALK match count, before the tool layer's render
	// bound. len(Matches) may be smaller than Total; Total never is. The json
	// key matches operation=count's own `total` key on purpose, so both ops
	// answer "how many are there" under one name. Populated by the handler,
	// which owns the render bound — Hydrate itself never sets it.
	Total      int    `json:"total"`
	Stats      Stats  `json:"stats"`
	WalkedRoot string `json:"walked_root,omitempty"`
	Hint       string `json:"hint,omitempty"`
	// Compiled describes every candidate the pattern compiled to. It is
	// populated even when Matches is EMPTY — that is the case it exists for,
	// since a zero result is diagnosable only once the caller can see which
	// construct their pattern became. The handler owns it: only the tool layer
	// holds the compile.
	Compiled []CompiledVariant `json:"compiled,omitempty"`
	// Narrowed describes the member-context variants the keyword-narrowing rule
	// dropped, each carrying a Reason naming the drop and the context:"member"
	// remedy. Empty on almost every compile; present when a statement keyword was
	// also a legal member name and the member reading was set aside.
	Narrowed []CompiledVariant `json:"narrowed,omitempty"`
}

// emptyResultHint is the LLM-facing guidance text emitted when Hydrate
// returns zero matches. It names the causes a caller can act on, in the order
// worth checking: what the pattern actually COMPILED to (visible in the
// `compiled` field, whose contexts are a SET per variant), the context pin that
// selects a different variant, the scope filters that may have excluded the
// files, and finally the wildcard + kind-leaf probe for the outer construct.
// The retired text blamed the caller's scope first, which is the least likely
// cause of a zero and the least diagnosable from the result.
//
// Kept as ONE UNBROKEN LINE: a reflowed concatenation would break the literal
// phrase its gate greps for.
const emptyResultHint = "no matches — read the `compiled` field for the root kinds this pattern compiled to and the contexts that produced each; pin context:\"decl\"|\"stmt\"|\"expr\"|\"member\" to select a different variant, check package_prefixes / include_tests are not excluding the files you meant, or use pattern:\"$_\" with where:{kind:{of:\"$match\",is:\"<kind>\"}} to confirm the outer construct exists"

// ZeroScanHint is the LLM-facing guidance text emitted when the walk scanned
// ZERO files (FilesScanned == 0), distinct from scanned-but-no-match (which
// keeps emptyResultHint). walkedRoot is the resolved directory the walk ran
// over; language is the raw user-supplied language string.
//
// It NAMES THE CAUSE rather than always blaming the root. A wrong root is only
// one of three ways to scan nothing, and it was the one this hint asserted for
// all of them — measured on c-redis, package_prefixes:["src/module.c"] returned
// "wrong root?" for a file that exists, is tracked, and is C, when the real
// cause was the 500KB size rule. In precedence order:
//
//  1. A DISCOVERY RULE declined files of the language asked for. This outranks
//     the prefix filter because it is the more specific fact: the caller's scope
//     did reach a file and a rule then took it away, which lift_exclusions can
//     undo.
//  2. PACKAGE_PREFIXES were supplied and nothing of that language survived them.
//  3. Neither — nothing of that language is under this root at all, which is the
//     wrong-root case and keeps the original wording.
//
// The cause is read off the exclusion report the walk already produced. It never
// re-walks: a hint that costs a second discovery pass would be paid for by every
// zero-result call, and the report is the record of what discovery declined.
func ZeroScanHint(walkedRoot, language string, scope Scope, stats WalkStats) string {
	if rule, sample, n := excludedOfLanguage(stats, treesitter.Language(language)); rule != "" {
		return fmt.Sprintf(
			"walked %s: nothing scanned because discovery declined %d %s file(s) under the %s rule (e.g. %s) — the root is fine, that rule is why the result is empty; pass lift_exclusions:true to walk them anyway",
			walkedRoot, n, language, rule, sample)
	}
	if len(scope.PackagePrefixes) > 0 {
		return fmt.Sprintf(
			"walked %s: no %s files under package_prefixes %s — prefixes match whole path SEGMENTS, so \"pkg\" is the pkg directory and never pkgextra; widen or drop package_prefixes to search the rest of the root",
			walkedRoot, language, strings.Join(scope.PackagePrefixes, ", "))
	}
	return fmt.Sprintf("walked %s: no %s files found — wrong root? pass repo:<name|/abs/path> or check --root", walkedRoot, language)
}

// DegradedZeroHint is the LLM-facing guidance text emitted when a zero result
// was computed over a corpus that did not fully parse. It is the third member of
// this file's zero-result family, and it follows the same discipline as
// ZeroScanHint: name the CAUSE, and read it off the report the walk already
// produced rather than re-walking.
//
// WHAT IT IS FOR. A zero and a zero over a broken parse are different answers
// that look identical. tree-sitter error-recovers a file it cannot fully parse
// and the walk reports it in FilesWithParseErrors rather than skipping it, so a
// construct sitting inside a recovered region can be absent from the tree
// entirely — and the caller reads "0" as "not there". The counter alone does not
// say this; a caller has to know what error recovery implies to read it. The
// warning says it.
//
// THE RULE: the result is zero AND FilesWithParseErrors is greater than zero. It
// deliberately does NOT consult MatchesFromDegradedTrees, which is definitionally
// zero on a zero result — keying on it would make the warning unreachable.
//
// IT NAMES THE COMPILED ROOT KINDS because that is what makes the warning
// actionable rather than merely alarming: knowing the pattern compiled to, say, a
// self-closing element lets the caller judge whether a degraded file could
// plausibly have held one. That is the useful half of narrowing by construct
// family, delivered as information the caller can act on instead of as a filter
// applied to data already known to be unreliable.
//
// It CARRIES the scanned-but-no-match guidance rather than replacing it: the
// ordinary causes of a zero are still live, and a caller that reads the
// degradation warning still needs the pin-a-context and check-your-prefixes
// advice. Both call sites emit this one text so the two cannot drift.
//
// Kept as ONE UNBROKEN LINE, for the reason emptyResultHint records above.
func DegradedZeroHint(stats WalkStats, compiled []CompiledVariant) string {
	return fmt.Sprintf("zero results over a corpus that did not fully parse — %d of %d scanned file(s) carried parse errors and were read off error-recovered trees, so this zero is NOT evidence of absence: a construct inside a recovered region can be missing from the tree the matcher walked. This pattern compiled to root kind(s) %s — if a degraded file could plausibly hold that construct, re-run scoped to it with package_prefixes before concluding the code is not there. Beyond degradation: %s",
		stats.FilesWithParseErrors, stats.FilesScanned, compiledRootKinds(compiled), emptyResultHint)
}

// compiledRootKinds renders the distinct root kinds a pattern compiled to, in
// variant order. A placeholder-rooted pattern has no root kind at all, and says
// so rather than rendering an empty list: "no root kind" is the fact that
// explains why such a pattern cannot be reasoned about by construct family.
func compiledRootKinds(compiled []CompiledVariant) string {
	seen := map[string]bool{}
	kinds := make([]string, 0, len(compiled))
	for _, v := range compiled {
		if v.RootKind == "" || seen[v.RootKind] {
			continue
		}
		seen[v.RootKind] = true
		kinds = append(kinds, v.RootKind)
	}
	if len(kinds) == 0 {
		return "no specific kind (placeholder root, so it matches any construct)"
	}
	return strings.Join(kinds, ", ")
}

// excludedOfLanguage finds a discovery rule that declined at least one file of
// the requested language, returning the rule name, one sample path and the
// rule's total count. It reads the samples rather than only the counts, because
// a rule that declined a lockfile says nothing about a caller searching for Go —
// naming it would trade one misdirection for another.
//
// Rules are considered in a stable order so the same walk always reports the
// same cause; map iteration order would otherwise make the hint flap between
// runs that declined files under two rules.
func excludedOfLanguage(stats WalkStats, lang treesitter.Language) (rule, sample string, count int) {
	names := make([]string, 0, len(stats.ExcludedSamples))
	for name := range stats.ExcludedSamples {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		for _, path := range stats.ExcludedSamples[name] {
			if treesitter.DetectLanguage(path) == lang {
				return name, path, stats.ExcludedByRule[name]
			}
		}
	}
	return "", "", 0
}

// codeNodeIndex resolves a (file_path, line) to the smallest enclosing
// function-ish code-graph node. Per-call, NOT cached.
//
// Indexed per-file as a slice of (start_line, end_line, node) triples sorted
// by ascending range size (smallest first) so lookup picks the tightest
// enclosing scope when functions nest (e.g., a closure inside a method).
//
// AST matches happen at arbitrary statement lines INSIDE function bodies —
// `defer X.Close()` at line 168 inside a function declared at line 153 is the
// shape we need to support. The earlier line-keyed map design only worked
// when the match line equaled the function-decl line (its prior caller in
// dead_code_review.go was specifically looking up dead-function-decl
// locations, not statements inside function bodies).
type codeNodeIndex struct {
	byFile map[string][]rangeEntry
}

// rangeEntry is one function-ish node with its inclusive line range.
type rangeEntry struct {
	startLine int
	endLine   int
	node      *knowledgev1.Node
}

// Hydrate joins a RawMatch slice to code-graph node IDs by (file_path, line).
// Builds the codeNodeIndex once per call (mirrors the per-Run shape in
// dead_code_review.go — no caching, no global state). Populates
// EnclosingNodeID + EnclosingSignature when an enclosing node is found via
// the backend; leaves both empty when no match (the file isn't in the code
// graph yet, which is expected for fresh repos, OR the caller is the
// client-side intercept using NoOpBackend).
//
// A nil backend is tolerated as shorthand for NoOpBackend so callers in the
// pre-refactor early-bring-up path keep compiling without an explicit shim.
//
// When raws is empty, returns MatchResults with Matches=nil and Hint set to
// the LLM-facing guidance text.
func Hydrate(ctx context.Context, backend HydratorBackend, raws []RawMatch, walk WalkStats) (MatchResults, error) {
	out := MatchResults{
		Stats: Stats(walk),
	}
	if len(raws) == 0 {
		out.Hint = emptyResultHint
		return out, nil
	}
	if backend == nil {
		backend = NoOpBackend{}
	}

	// Collect unique file paths from raws so backends that can scope their
	// walk (e.g., the client-side query(file_symbols) call) can do one
	// round trip rather than walking the whole graph.
	seen := make(map[string]struct{}, len(raws))
	files := make([]string, 0, len(raws))
	for _, r := range raws {
		if r.FilePath == "" {
			continue
		}
		if _, dup := seen[r.FilePath]; dup {
			continue
		}
		seen[r.FilePath] = struct{}{}
		files = append(files, r.FilePath)
	}

	idx, err := buildCodeNodeIndex(ctx, backend, files)
	if err != nil {
		return MatchResults{}, err
	}

	out.Matches = make([]MatchResult, 0, len(raws))
	for _, r := range raws {
		mr := MatchResult{
			FilePath:         r.FilePath,
			StartLine:        r.StartLine,
			EndLine:          r.EndLine,
			Captures:         r.Captures,
			CompiledKind:     r.CompiledKind,
			CompiledContexts: r.CompiledContexts,
		}
		if node, ok := idx.lookup(r.FilePath, r.StartLine); ok {
			mr.EnclosingNodeID = node.GetId()
			mr.EnclosingSignature = node.GetSignature()
		}
		out.Matches = append(out.Matches, mr)
	}
	return out, nil
}

// buildCodeNodeIndex enumerates every function-ish node via the backend and
// indexes them per-file as line-range triples. NoOpBackend short-circuits to
// an empty index, leaving every Hydrate match with empty EnclosingNodeID.
// Returns a non-nil but empty index when the backend emits zero nodes rather
// than an error so Hydrate keeps emitting matches.
//
// Per-file slices are sorted by range size (smallest first) so range lookup
// picks the tightest enclosing scope when nodes nest.
func buildCodeNodeIndex(ctx context.Context, backend HydratorBackend, files []string) (*codeNodeIndex, error) {
	idx := &codeNodeIndex{byFile: make(map[string][]rangeEntry)}
	if err := backend.IterateFunctionish(ctx, files, func(n *knowledgev1.Node) error {
		if n.GetFilePath() == "" || n.GetStartLine() <= 0 {
			return nil
		}
		// EndLine 0 means "single-line" or "unknown end" — treat as
		// StartLine to avoid zero-length ranges that match everything.
		end := n.GetEndLine()
		if end <= 0 {
			end = n.GetStartLine()
		}
		idx.byFile[n.GetFilePath()] = append(idx.byFile[n.GetFilePath()], rangeEntry{
			startLine: int(n.GetStartLine()),
			endLine:   int(end),
			node:      n,
		})
		return nil
	}); err != nil {
		return nil, fmt.Errorf("ast/result: list code nodes: %w", err)
	}
	for file, entries := range idx.byFile {
		// Sort by range size ascending so the first containing entry
		// during lookup is the tightest enclosing scope (handles nested
		// closures / methods inside types).
		sort.Slice(entries, func(i, j int) bool {
			return (entries[i].endLine - entries[i].startLine) < (entries[j].endLine - entries[j].startLine)
		})
		idx.byFile[file] = entries
	}
	return idx, nil
}

// lookup finds the smallest function-ish node whose line range encloses
// the given (file, line). Returns false when no node contains the line.
//
// Implementation: per-file slices are pre-sorted by range size ascending so
// the first containing entry IS the tightest enclosing scope.
func (idx *codeNodeIndex) lookup(file string, line int) (*knowledgev1.Node, bool) {
	entries, ok := idx.byFile[file]
	if !ok {
		return nil, false
	}
	// ±1 line tolerance on both bounds absorbs the 0-based / 1-based
	// off-by-one between tree-sitter and the chunker's stored StartLine.
	// Same discipline as the prior key-based lookup.
	for _, e := range entries {
		if line >= e.startLine-1 && line <= e.endLine+1 {
			return e.node, true
		}
	}
	return nil, false
}
