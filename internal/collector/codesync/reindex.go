// SPDX-License-Identifier: Apache-2.0

package codesync

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/parser"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// augmentWithPreciseCallGraph runs VTA-based call graph analysis over EVERY Go
// module the indexed files live in, and replaces the tree-sitter heuristic CALLS
// edges WHOSE CALLER IS A GO DECLARATION IN A FILE THE ANALYSIS COVERED with
// type-precise edges. CALLS edges with a non-Go caller, CALLS edges whose Go
// caller lives in a file the analysis could not load, and all non-CALLS edges,
// are preserved unchanged. If the toolchain cannot be resolved or
// BuildGoCallGraph fails, the original populate result is returned unmodified.
//
// The function only runs when a go.mod is present in rootDir.
//
// EVERY RETURN PATH EMITS THE SUMMARY LINE — see logMergeSummary for why.
func augmentWithPreciseCallGraph(ctx context.Context, pop parser.PopulateResult, rootDir string) parser.PopulateResult {
	if _, err := os.Stat(filepath.Join(rootDir, "go.mod")); err != nil {
		slog.Warn("augmentWithPreciseCallGraph: no go.mod found, skipping", "rootDir", rootDir)
		logMergeSummary(mergeCounters{})
		return pop
	}
	if !ensureGoOnPath() {
		slog.Warn("augmentWithPreciseCallGraph: no Go toolchain resolvable, keeping tree-sitter edges")
		logMergeSummary(mergeCounters{toolchainMissing: true})
		return pop
	}
	moduleDirs := goModuleDirs(rootDir, pop.Nodes)
	cg, err := BuildGoCallGraph(ctx, rootDir, moduleDirs)
	degraded := mergeCounters{
		modules:      len(moduleDirs),
		coveredFiles: len(cg.CoveredFiles),
		packageErrs:  cg.PackageErrs,
	}
	if err != nil {
		slog.Warn("augmentWithPreciseCallGraph: BuildGoCallGraph failed, keeping tree-sitter edges", "error", err)
		logMergeSummary(degraded)
		return pop
	}
	if len(cg.CallMap) == 0 {
		slog.Info("augmentWithPreciseCallGraph: no call edges produced, keeping tree-sitter edges")
		logMergeSummary(degraded)
		return pop
	}

	idx := buildNodeIndex(pop.Nodes)
	tsWeights := captureCallEdgeWeights(pop.Edges)
	filtered, removedCalls, keptNonGo, keptUncovered := dropGoCallerCallEdges(pop.Edges, idx, cg.CoveredFiles)
	filtered, added := appendRTACallEdges(filtered, cg.CallMap, idx.declToID, tsWeights)

	logMergeSummary(mergeCounters{
		removed:           removedCalls,
		keptNonGo:         keptNonGo,
		keptUncovered:     keptUncovered,
		added:             added,
		rtaCallers:        len(cg.CallMap),
		modules:           len(moduleDirs),
		coveredFiles:      len(cg.CoveredFiles),
		declKeyCollisions: idx.collisions,
		packageErrs:       cg.PackageErrs,
	})
	pop.Edges = filtered
	return pop
}

// mergeCounters is everything the one-per-collect summary line reports.
//
// toolchainMissing is true exactly when ensureGoOnPath reported false. On the
// no-go.mod path the resolver is never consulted, so it stays false there and
// the zeroed modules and covered_files distinguish that case.
type mergeCounters struct {
	removed           int
	keptNonGo         int
	keptUncovered     int
	added             int
	rtaCallers        int
	modules           int
	coveredFiles      int
	declKeyCollisions int
	packageErrs       int
	toolchainMissing  bool
}

// logMergeSummary emits the ONE authoritative line per collect, on every return
// path of augmentWithPreciseCallGraph including the degraded ones.
//
// WHY IT IS UNCONDITIONAL. This function's defect ran in production printing
// `removed=75148 ... added=35` on every collect, and nobody could tell that was
// wrong from the line alone, because the line reported what the merge DID and
// never what it COULD NOT SEE. Worse, the three early returns — no go.mod, a
// BuildGoCallGraph error, an empty call map — never reached the line at all, so
// 94 consecutive launchd collects degraded to pure tree-sitter behind nothing
// but a WARN that nothing gates. A summary that cannot distinguish augmented
// from degraded is the same blindness class as package_errors reading 0 while
// three modules went unanalyzed.
//
// The keys therefore report coverage as well as action:
//
//	kept_uncovered_go     tree-sitter Go edges preserved because their declaring
//	                      file was never analyzed
//	modules               how many Go modules were analyzed
//	covered_files         how many repo files the analysis actually loaded
//	go_toolchain_missing  the augmentation could not find a `go` binary at all
//
// package_errors belongs on THIS line, not only on BuildGoCallGraph's own
// warning: a Go package that fails to type-check contributes no callers, so its
// files are uncovered and its edges are now preserved rather than lost. Beside
// kept_non_go and kept_uncovered_go, the count lets an operator tell build-error
// thinning from a genuinely empty call graph.
func logMergeSummary(c mergeCounters) {
	slog.Info("precise call graph: replaced tree-sitter CALLS edges",
		"removed", c.removed,
		"kept_non_go", c.keptNonGo,
		"kept_uncovered_go", c.keptUncovered,
		"added", c.added,
		"rta_callers", c.rtaCallers,
		"modules", c.modules,
		"covered_files", c.coveredFiles,
		"decl_key_collisions", c.declKeyCollisions,
		"package_errors", c.packageErrs,
		"go_toolchain_missing", c.toolchainMissing,
	)
}

// goModuleDirs returns the repo-relative directory of every Go module the
// INDEXED Go files live in, sorted.
//
// WHY THIS DERIVES FROM THE POPULATE RESULT RATHER THAN WALKING FOR go.mod
// FILES. The set of modules analyzed and the set of files whose CALLS edges the
// merge drops must be the SAME set, decided by ONE rule. A second filesystem
// walk would carry its own exclusion rules — parser's skipDirs, isIndexable, the
// git-vs-walk discovery split — and the two would drift, leaving one authority
// deciding what gets analyzed and a different one deciding what gets deleted.
// That divergence IS the defect this change exists to end. Deriving from
// pop.Nodes makes them one rule by construction, and stays self-consistent at
// every corner: a module whose files are all excluded from indexing contributes
// no nodes, is not discovered, is not analyzed, and has no edges to drop.
//
// The upward walk is memoised per directory, so it runs once per directory
// rather than once per node. The result is sorted so load order — and therefore
// the log line and any failure message — is deterministic.
func goModuleDirs(rootDir string, nodes []*knowledgev1.Node) []string {
	nearest := make(map[string]string)
	mods := make(map[string]bool)
	for _, n := range nodes {
		if n.Language != string(treesitter.LangGo) || n.FilePath == "" {
			continue
		}
		dir := path.Dir(filepath.ToSlash(n.FilePath))
		md, seen := nearest[dir]
		if !seen {
			for d := dir; ; {
				if _, err := os.Stat(filepath.Join(rootDir, filepath.FromSlash(d), "go.mod")); err == nil {
					md = d
					break
				}
				parent := path.Dir(d)
				if d == "." || parent == d {
					break
				}
				d = parent
			}
			nearest[dir] = md
		}
		if md != "" {
			mods[md] = true
		}
	}
	out := make([]string, 0, len(mods))
	for md := range mods {
		out = append(out, md)
	}
	slices.Sort(out)
	return out
}

var (
	goPathOnce   sync.Once
	goPathUsable bool
)

// ensureGoOnPath guarantees the Go toolchain is reachable through the PROCESS
// PATH, and reports whether one is usable at all.
//
// THE FAILURE IT CLOSES. Under launchd the daemon runs with
// PATH=/usr/bin:/bin:/usr/sbin:/sbin, because its plist declares no
// EnvironmentVariables, while the toolchain lives outside those four
// directories. In that environment every collect logged "BuildGoCallGraph
// failed, keeping tree-sitter edges" with `exec: "go": executable file not found
// in $PATH` and silently degraded to pure tree-sitter — under exactly the
// process manager production uses.
//
// DO NOT "SIMPLIFY" THIS INTO packages.Config.Env. The pinned x/tools v0.45.0
// execs a BARE exec.Command("go", goArgs...) at internal/gocommand/invoke.go:248
// and only assigns cmd.Env afterwards, at lines 270-272. exec.Command resolves
// the binary through LookPath against the PARENT PROCESS PATH, so cmd.Env — and
// therefore packages.Config.Env — cannot influence which `go` is found, or
// whether one is found at all. Both shapes were probed under a simulated launchd
// environment: an augmented cfg.Env still failed with the identical error, and
// only the process-PATH prepend resolved it. The process PATH is the sole
// available lever.
//
// The prepend is a process-wide mutation in a long-running daemon, so it is
// sync.Once guarded and only reached after exec.LookPath has ALREADY failed. In
// the ordinary developer case this function mutates nothing.
func ensureGoOnPath() bool {
	goPathOnce.Do(func() {
		if _, err := exec.LookPath("go"); err == nil {
			goPathUsable = true
			return
		}
		dir, found := resolveGoToolchainDir()
		if !found {
			slog.Warn("ensureGoOnPath: no Go toolchain found, the precise call graph cannot run",
				"path", os.Getenv("PATH"))
			return
		}
		// os.Setenv is the only lever that can influence x/tools' bare
		// exec.Command("go") — see this function's doc comment.
		if err := os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH")); err != nil {
			slog.Warn("ensureGoOnPath: could not prepend the Go toolchain directory to PATH",
				"dir", dir, "error", err)
			return
		}
		goPathUsable = true
		slog.Info("ensureGoOnPath: prepended the Go toolchain directory to PATH", "dir", dir)
	})
	return goPathUsable
}

// resolveGoToolchainDir returns the directory holding a usable `go` binary and
// true, or ("", false) when none qualifies.
//
// exec.LookPath is tried FIRST — the ordinary developer case, and the only case
// in which nothing is mutated. On a miss it probes the standard install
// locations in order: $GOROOT/bin when GOROOT is set, then the official tarball
// location, Homebrew on Apple silicon, /usr/local, MacPorts, and the per-user
// GOPATH bin. A candidate qualifies only when os.Stat reports a non-directory
// carrying an executable bit.
func resolveGoToolchainDir() (dir string, found bool) {
	if p, err := exec.LookPath("go"); err == nil {
		return filepath.Dir(p), true
	}
	var candidates []string
	if goroot := os.Getenv("GOROOT"); goroot != "" {
		candidates = append(candidates, filepath.Join(goroot, "bin", "go"))
	}
	candidates = append(candidates,
		"/usr/local/go/bin/go",
		"/opt/homebrew/bin/go",
		"/usr/local/bin/go",
		"/opt/local/bin/go",
	)
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidates = append(candidates, filepath.Join(home, "go", "bin", "go"))
	}
	for _, c := range candidates {
		info, err := os.Stat(c) //nolint:gosec // G703: the candidates are this function's own fixed list plus $GOROOT/bin and $HOME/go/bin, all read-only stats of a process-owned environment, never request-derived.
		if err != nil || info.IsDir() || info.Mode().Perm()&0o111 == 0 {
			continue
		}
		return filepath.Dir(c), true
	}
	return "", false
}

// nodeIndex is the one-pass index of a populate result that the RTA merge
// needs: the decl-key → node ID lookup the precise call graph binds through,
// the node ID → language lookup the drop guard reads, and the node ID →
// declaring file lookup the coverage gate reads.
//
// fileByID and the analysis's covered-file set are spelled the same way —
// repo-relative and slash-separated — by two authorities that already agree
// byte-for-byte: the covered set comes from the relativizer applied to
// go/packages positions, fileByID from the collector's own Node.FilePath. That
// agreement is what makes the decl key an identity rather than a guess, and the
// coverage gate depends on the same agreement.
//
// collisions counts decl keys seen more than once. Under the decl-key
// derivation the key IS the node ID, so a repeat means duplicate node IDs —
// which parser.DeduplicateChunks exists to make impossible. It is an invariant
// alarm, not a case to serve.
type nodeIndex struct {
	declToID   map[string]string
	langByID   map[string]string
	fileByID   map[string]string
	collisions int
}

// buildNodeIndex builds all three lookups in a single traversal of nodes.
//
// The decl key is the node ID itself, gated on the ID actually having the
// "<FilePath>:<symbol>" shape parser.ChunkNodeID produces. The gate is a PREFIX
// test rather than a cut at the last colon, because symbol TEXT can contain a
// colon: cutting there split TypeScript IDs such as
// "web/src/app/api/accounts.ts:createAccount.{ data: result, error }" onto a key
// four unrelated declarations shared — 6 measured collisions on a real corpus,
// 0 under this derivation.
//
// A duplicate key is REFUSED, not resolved: the key is withdrawn so neither
// declaration binds, and the withdrawal is permanent so a third sighting cannot
// re-establish it. "Never last-write-wins" means an ambiguous key produces no
// edge at all — a coin-flip edge is strictly worse than a missing one.
func buildNodeIndex(nodes []*knowledgev1.Node) nodeIndex {
	idx := nodeIndex{
		declToID: make(map[string]string, len(nodes)),
		langByID: make(map[string]string, len(nodes)),
		fileByID: make(map[string]string, len(nodes)),
	}
	withdrawn := make(map[string]bool)
	for _, n := range nodes {
		if n.Id == "" {
			continue
		}
		idx.langByID[n.Id] = n.Language
		if n.FilePath != "" {
			idx.fileByID[n.Id] = filepath.ToSlash(n.FilePath)
		}
		if n.SymbolName == "" || n.FilePath == "" {
			continue
		}
		prefix := n.FilePath + ":"
		if !strings.HasPrefix(n.Id, prefix) || len(n.Id) <= len(prefix) {
			continue
		}
		key := n.Id
		if withdrawn[key] {
			idx.collisions++
			slog.Error("precise call graph: duplicate decl key, refusing to bind", "decl_key", key)
			continue
		}
		if _, dup := idx.declToID[key]; dup {
			idx.collisions++
			slog.Error("precise call graph: duplicate decl key, refusing to bind", "decl_key", key)
			delete(idx.declToID, key)
			withdrawn[key] = true
			continue
		}
		idx.declToID[key] = n.Id
	}
	return idx
}

// captureCallEdgeWeights snapshots the (FromID, ToID) → Weight map from
// the existing CALLS edges so the RTA merge can re-attach tree-sitter
// call counts to pairs both layers agree on.
func captureCallEdgeWeights(edges []*knowledgev1.Edge) map[[2]string]float64 {
	tsWeights := make(map[[2]string]float64)
	for _, e := range edges {
		if kgtypes.EdgeType(e.Type) != kgtypes.EdgeCalls {
			continue
		}
		tsWeights[[2]string{e.FromId, e.ToId}] = e.Weight
	}
	return tsWeights
}

// dropGoCallerCallEdges returns a new slice containing every edge from the input
// EXCEPT the CALLS edges whose CALLER is a Go declaration IN A FILE THE PRECISE
// ANALYSIS COVERED, plus the count removed, the count of non-Go-caller CALLS
// edges kept, and the count of Go-caller CALLS edges kept because their file was
// never analyzed.
//
// The rule keys on the FROM endpoint, not on both endpoints. VTA enumerates the
// call sites inside every analyzed Go function, so an edge whose caller is a Go
// declaration is fully spoken for by the precise graph — including a
// cross-language one, which the Go call graph can never emit and which is
// therefore a false claim inside a domain the precise graph owns. An edge whose
// caller is NOT Go lies outside that domain and the precise graph will never
// re-add it, so dropping it would delete a call edge nothing can replace.
//
// THE COVERAGE GATE IS THAT SAME RULE, APPLIED WHERE IT USED TO BE ASSUMED AWAY.
// A Go caller in a file the analysis never loaded is in exactly the position the
// paragraph above describes: the precise graph will never re-add its edge. That
// population is permanent and normal, not exceptional — files excluded by build
// tags on the building platform, Go files under testdata directories the go tool
// ignores by design, and any module that fails to type-check. So the drop is
// conditional on covered[fileByID[e.FromId]], and an uncovered Go caller's edge
// survives and is counted in keptUncovered.
//
// An endpoint with no entry in langByID reads as non-Go and SURVIVES.
// parser.resolveEdges already filters edges to known node IDs, so this only
// fires on an unexpected shape, and keeping the edge is the conservative half.
//
// The edges are *knowledgev1.Edge pointers, so retained edges are appended by
// pointer — no copylocks-safe field-by-field rebuild needed.
func dropGoCallerCallEdges(
	edges []*knowledgev1.Edge,
	idx nodeIndex,
	covered map[string]bool,
) (filtered []*knowledgev1.Edge, removed, keptNonGo, keptUncovered int) {
	filtered = make([]*knowledgev1.Edge, 0, len(edges))
	for _, e := range edges {
		if kgtypes.EdgeType(e.Type) == kgtypes.EdgeCalls {
			switch {
			case idx.langByID[e.FromId] != string(treesitter.LangGo):
				keptNonGo++
			case covered[idx.fileByID[e.FromId]]:
				removed++
				continue
			default:
				keptUncovered++
			}
		}
		filtered = append(filtered, e)
	}
	return filtered, removed, keptNonGo, keptUncovered
}

// appendRTACallEdges walks the RTA call map and appends a CALLS edge for
// every (caller, callee) pair where both endpoints resolve to a node ID,
// re-attaching the tree-sitter Weight when the same pair was seen by
// both layers and defaulting to Weight=1 for RTA-only pairs.
func appendRTACallEdges(
	dst []*knowledgev1.Edge,
	callMap map[string][]string,
	declToID map[string]string,
	tsWeights map[[2]string]float64,
) ([]*knowledgev1.Edge, int) {
	var added int
	seen := make(map[[2]string]bool)
	for callerKey, callees := range callMap {
		callerID, ok := declToID[callerKey]
		if !ok {
			continue
		}
		for _, calleeKey := range callees {
			calleeID, ok := declToID[calleeKey]
			if !ok {
				continue
			}
			pair := [2]string{callerID, calleeID}
			if seen[pair] {
				continue
			}
			seen[pair] = true
			weight := tsWeights[pair]
			if weight == 0 {
				weight = 1
			}
			dst = append(dst, &knowledgev1.Edge{
				FromId: callerID,
				ToId:   calleeID,
				Type:   string(kgtypes.EdgeCalls),
				Weight: weight,
			})
			added++
		}
	}
	return dst, added
}
