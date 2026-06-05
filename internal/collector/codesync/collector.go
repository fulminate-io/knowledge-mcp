// SPDX-License-Identifier: Apache-2.0

package codesync

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/coderun"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/parser"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
)

func init() {
	collector.Register(&CodeCollector{})
}

// CodeCollector implements collector.Collector for code repositories.
// It discovers source files via tree-sitter, chunks them, and returns
// graph nodes and edges without touching any database.
type CodeCollector struct{}

// Name returns "code" — the collector type used for registry lookup.
func (c *CodeCollector) Name() string { return "code" }

// Collect performs code collection for the repository at the given rootDir (id).
// It discovers files, chunks with tree-sitter, detects the git branch, and
// returns a CollectResult ready for the collector pipeline to persist.
func (c *CodeCollector) Collect(ctx context.Context, id string, opts collector.CollectOptions) (*collectorwire.CollectResult, error) {
	// Only absolute paths are accepted. Relative paths (including ".",
	// "..", "./foo") are rejected: they depend on the caller's cwd, can
	// be interpreted differently after process moves, and open
	// filepath-traversal surface. They also silently produce useless
	// repo names — `filepath.Base(".") == "."` would key a fresh graph
	// under "." and trigger a full re-summarization parallel to the
	// real repo's graph.
	if !filepath.IsAbs(id) {
		return nil, fmt.Errorf("code: id must be an absolute path; got %q", id)
	}
	rootDir := filepath.Clean(id)
	repoName := filepath.Base(rootDir)
	if repoName == "." || repoName == ".." || repoName == string(filepath.Separator) || repoName == "" {
		return nil, fmt.Errorf("code: id %q resolves to invalid repo name %q", id, repoName)
	}
	if err := validateCodeRoot(rootDir); err != nil {
		return nil, err
	}

	if opts.OnProgress != nil {
		opts.OnProgress(0, 0, "discovering and chunking source files...")
	}

	pop, err := parser.Populate(ctx, repoName, rootDir)
	if err != nil {
		return nil, err
	}

	// Optionally replace tree-sitter CALLS with RTA-precise call graph edges.
	pop = augmentWithPreciseCallGraph(ctx, pop, rootDir)

	if opts.OnProgress != nil {
		opts.OnProgress(len(pop.Nodes), len(pop.Nodes), "collection complete")
	}

	// Convert pre-resolved *knowledgev1.Edge to kgwire.BatchEdge for the collector
	// pipeline (BatchEdge stays the gap carrier the wire-send chain consumes).
	batchEdges := make([]kgwire.BatchEdge, len(pop.Edges))
	for i, e := range pop.Edges {
		batchEdges[i] = kgwire.BatchEdge{
			FromIdx: -1,
			ToIdx:   -1,
			FromID:  e.FromId,
			ToID:    e.ToId,
			Type:    kgtypes.EdgeType(e.Type),
			Weight:  e.Weight,
		}
	}

	// Report the git branch we collected from. The server compares it
	// against the existing graph's recorded default branch (graph metadata
	// SyncDefaultBranchKey) to decide overlay-vs-full-replace. The client
	// no longer makes that decision — git's notion of "default" (main /
	// master via origin/HEAD) is decoupled from the graph's recorded default,
	// which is whatever branch was current the first time the base graph
	// was collected. Empty/"HEAD" is forwarded as-is and the server treats
	// it as a full-replace.
	branch, _ := coderun.DetectBranch(ctx, rootDir)

	// Record the collected HEAD SHA + collection time so the server can
	// persist them onto code-graph metadata. headSHA is soft-errored like
	// DetectBranch — empty on a non-git repo, which the consumers degrade
	// past gracefully.
	headSHA, _ := coderun.HeadCommit(ctx, rootDir)
	syncTime := time.Now().UnixNano()

	// Phase 5: read go.mod and the optional layer-config YAML
	// CLIENT-SIDE and ship them over the wire. The server pod never
	// opens these files — topology analyzers read both from graph
	// metadata (ModulePathKey, LayerConfigKey) instead.
	modulePath, _ := readModulePath(rootDir)
	layerConfig, _ := readLayerConfig(rootDir)

	// CollectResult.Nodes is []*knowledgev1.Node; the parser now builds the
	// typed pointer slice directly, so it flows through unchanged.
	return &collectorwire.CollectResult{
		GraphType:     kgtypes.GraphCode,
		GraphName:     repoName,
		Nodes:         pop.Nodes,
		Edges:         batchEdges,
		CurrentBranch: branch,
		SyncCommit:    headSHA,
		SyncTime:      syncTime,
		ModulePath:    modulePath,
		LayerConfig:   layerConfig,
	}, nil
}

// readModulePath opens <rootDir>/go.mod and returns the `module ...`
// directive. Mirrors the prior pkg/topology/dsm_matrix.go::readModulePath
// (which is being deleted in Phase 5.2). Returns ("", nil) on any failure
// so non-Go repos don't trip the collector — the caller persists the
// empty value and dsm.go server-side gracefully skips when the metadata
// key is empty.
func readModulePath(rootDir string) (string, error) {
	f, err := os.Open(filepath.Join(rootDir, "go.mod"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "module ") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(line, "module"))
		rest = strings.TrimPrefix(rest, `"`)
		rest = strings.TrimSuffix(rest, `"`)
		if idx := strings.Index(rest, "//"); idx >= 0 {
			rest = strings.TrimSpace(rest[:idx])
		}
		if rest == "" {
			return "", nil
		}
		return rest, nil
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", nil
}

// readLayerConfig reads `.knowledge/topology_layers.yaml` under rootDir
// and returns the raw bytes as a string. Missing file → ("", nil). The
// server side re-parses the YAML in ConfigFileProvider — we ship the
// raw body (not the parsed struct) so the YAML parser, error messages,
// and version compatibility stay co-located with the analyzer.
func readLayerConfig(rootDir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(rootDir, ".knowledge", "topology_layers.yaml"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}

// repoMarkerFiles enumerates filenames that, when present at the root,
// strongly suggest the directory is a code repository the operator
// intended to collect. The check is OR — any one marker satisfies it.
// Empty test fixtures (used in TestCodeCollector_Collect_Progress) place
// at least one source file at the root, so the file-extension fallback
// in validateCodeRoot covers them without requiring a marker.
var repoMarkerFiles = []string{
	".git",
	".hg",
	".svn",
	"go.mod",
	"package.json",
	"Cargo.toml",
	"pyproject.toml",
	"setup.py",
	"requirements.txt",
	"Gemfile",
	"pom.xml",
	"build.gradle",
	"build.gradle.kts",
	"composer.json",
	"CMakeLists.txt",
	"Makefile",
}

// systemPathPrefixes is the rejection list for paths that are system
// trees regardless of incidental marker files. /etc on macOS carries a
// Makefile (BSD convention) — the marker check alone would let it
// through. Path-based rejection runs before marker-based validation so
// system trees are caught even when they happen to contain qualifying
// files. /var and /lib are excluded so the macOS test tmpdir
// (/var/folders/...) and Linux container repos under /lib still pass.
var systemPathPrefixes = []string{
	"/etc",
	"/usr",
	"/sys",
	"/proc",
	"/dev",
	"/boot",
	"/sbin",
	"/bin",
}

// validateCodeRoot ensures the absolute path points at a real directory,
// isn't a known system tree, AND looks like a code repository. The probe
// found that `collect(type:code, id:"/etc")` was silently
// creating an empty graph because the discovery walk found no source
// files — but the side effect (graph creation, BM25/HNSW index seeding)
// had already landed.
//
// Two-layer validation:
//
//  1. systemPathPrefixes — reject /etc, /usr, /sys, /proc, /dev, /boot,
//     /sbin, /bin upfront. These are system trees the operator almost
//     certainly didn't mean to collect.
//
//  2. Repo signal — require either a marker file (.git, go.mod,
//     package.json, …) OR at least one top-level source file with a
//     recognizable extension. Permissive enough for test fixtures and
//     mono-language sub-trees while rejecting empty / config-only
//     directories.
func validateCodeRoot(rootDir string) error {
	// System-path check runs first so /proc, /boot, /sys, etc. error
	// with the actionable "is under system path" message even on
	// platforms where the directory doesn't exist (macOS doesn't have
	// /proc, Linux doesn't have macOS-specific paths). The os.Stat
	// would otherwise mask the rejection with a "not accessible" error.
	for _, prefix := range systemPathPrefixes {
		if rootDir == prefix || strings.HasPrefix(rootDir, prefix+string(filepath.Separator)) {
			return fmt.Errorf("code: id %q is under system path %q — collect over a real source repository", rootDir, prefix)
		}
	}
	info, err := os.Stat(rootDir)
	if err != nil {
		return fmt.Errorf("code: id %q is not accessible: %w", rootDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("code: id %q is not a directory", rootDir)
	}
	for _, marker := range repoMarkerFiles {
		if _, err := os.Stat(filepath.Join(rootDir, marker)); err == nil {
			return nil
		}
	}
	if hasTopLevelSourceFile(rootDir) {
		return nil
	}
	return fmt.Errorf(
		"code: id %q has no repo marker (.git, go.mod, package.json, …) or source files at the top level — collect over a real source repository",
		rootDir,
	)
}

// hasTopLevelSourceFile returns true if rootDir contains at least one
// file with a recognized source-code extension at the top level.
// Top-level only — recursive scan would be too expensive on large
// system directories like /usr/share. Source extensions span the
// languages tree-sitter already knows about; keeping the list short
// and obvious matches the validation's "is this plausibly a repo?"
// purpose without enumerating every extension we eventually parse.
func hasTopLevelSourceFile(rootDir string) bool {
	entries, err := os.ReadDir(rootDir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		switch ext {
		case ".go", ".py", ".js", ".ts", ".jsx", ".tsx", ".rs",
			".java", ".kt", ".rb", ".php", ".c", ".cc", ".cpp",
			".h", ".hpp", ".cs", ".swift", ".scala", ".lua",
			".sh", ".pl", ".ex", ".exs", ".clj", ".hs", ".ml":
			return true
		}
	}
	return false
}

// codePostPopulate runs BuildHierarchy + LinkStepsToCode against the per-repo
// code graph after collection, entirely over the postpopulate wire seam.
// graphName is the code repo graph; both helpers route their reads/writes via
// kgtypes.GraphCode (→ Target.Repo==graphName). Registered by
// codesync/register_postpopulate.go under the "code" collector key so the
// live-path orchestrator fires it after a code collect.
func codePostPopulate(ctx context.Context, gc postpopulate.GraphCaller, graphName string) error {
	if err := coderun.BuildHierarchy(ctx, gc, graphName); err != nil {
		return fmt.Errorf("codePostPopulate: BuildHierarchy: %w", err)
	}
	if err := coderun.LinkStepsToCode(ctx, gc, graphName); err != nil {
		return fmt.Errorf("codePostPopulate: LinkStepsToCode: %w", err)
	}
	return nil
}
