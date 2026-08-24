// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// maxFileSize is the maximum file size to index (500KB).
const maxFileSize = 500 * 1024

// skipExtensions are file extensions excluded from indexing.
// Markdown files are documentation, not code — their content is self-documenting.
var skipExtensions = map[string]bool{
	".md":       true,
	".markdown": true,
}

// skipFiles are exact filenames excluded from indexing.
// Lock files contain pinned dependency versions / hashes, not useful for code understanding.
var skipFiles = map[string]bool{
	// JavaScript/TypeScript
	"package-lock.json":   true,
	"yarn.lock":           true,
	"pnpm-lock.yaml":      true,
	"bun.lock":            true,
	"npm-shrinkwrap.json": true,
	"shrinkwrap.yaml":     true,
	// Go
	"go.sum": true,
	// Rust
	"Cargo.lock": true,
	// Python
	"poetry.lock":  true,
	"Pipfile.lock": true,
	"uv.lock":      true,
	"pdm.lock":     true,
	// Ruby
	"Gemfile.lock": true,
	// PHP
	"composer.lock": true,
	// C#
	"packages.lock.json": true,
	// Java/Kotlin/Gradle
	"gradle.lockfile": true,
	// Swift
	"Package.resolved": true,
	// Dart/Flutter
	"pubspec.lock": true,
	// Elixir
	"mix.lock": true,
	// CocoaPods (Swift/ObjC)
	"Podfile.lock": true,
	// Terraform/HCL
	".terraform.lock.hcl": true,
	// Nix
	"flake.lock": true,
}

// skipPathComponents are directory names checked in file paths for git ls-files filtering.
// These mirror relevant entries from skipDirs that git ls-files won't filter automatically.
var skipPathComponents = []string{
	"vendor", "testdata",
	"deps", "thirdparty", "third_party", "third-party",
	"generated", ".generated",
	"node_modules",
}

// DiscoverFiles returns relative paths of all indexable source files in repoDir.
// Uses `git ls-files` to respect .gitignore (nested .gitignore files, negation patterns, etc.).
// Falls back to filesystem walk if the directory is not a git repo.
//
// It is the discard-the-report form of DiscoverFilesReporting: callers that only
// need the included set keep this signature, and every file the walk declines is
// dropped silently exactly as before. Callers that must disclose WHY a file is
// absent take the reporting form instead.
func DiscoverFiles(ctx context.Context, repoDir string) ([]string, error) {
	files, _, err := DiscoverFilesReporting(ctx, repoDir, DiscoveryOptions{})
	return files, err
}

// discoverWithGit uses `git ls-files` to get tracked files, respecting .gitignore.
// Every candidate the rule chain declines is recorded on rep under the rule that
// declined it; opts.LiftExclusions bypasses the chain entirely.
//
// opts.PackagePrefixes ride along as git pathspecs rather than being filtered
// out afterward, so a narrow scope stops paying to enumerate the whole tree —
// on a 20k-file repo that enumeration, not the matching, is what a single-file
// query spends its time on.
func discoverWithGit(ctx context.Context, repoDir string, opts DiscoveryOptions, rep *DiscoveryReport) ([]string, error) {
	args := append([]string{"ls-files", "--cached", "--others", "--exclude-standard"}, pathspecsFor(opts.PackagePrefixes)...)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var files []string
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		rel := scanner.Text()
		if !opts.LiftExclusions {
			if ok, rule := isIndexable(repoDir, rel); !ok {
				rep.record(rule, rel)
				continue
			}
		}
		files = append(files, rel)
	}
	return files, scanner.Err()
}

// discoverWithWalk is the fallback for non-git repos.
var skipDirs = map[string]bool{
	".git": true, ".svn": true, ".hg": true, ".bzr": true,
	"node_modules": true, ".next": true, ".nuxt": true, ".vite": true,
	"__pycache__": true, ".venv": true, "venv": true,
	"vendor": true, "testdata": true, "target": true,
	".gradle": true, ".mvn": true, "bin": true, "obj": true,
	"Pods": true, "DerivedData": true, ".build": true,
	"build": true, "dist": true, "out": true,
	".idea": true, ".vscode": true, ".vs": true,
	".terraform": true, ".cache": true, "tmp": true,
	// Agent-tool state (worktrees, config) — gitignored by convention, so the
	// git path never sees it; the walk must prune it to match.
	".claude": true,
	// Third-party vendored code (different names across ecosystems).
	"deps": true, "thirdparty": true, "third_party": true, "third-party": true,
	// Generated code output directories.
	"generated": true, ".generated": true,
}

// discoverWithWalk is the non-git fallback. Directories in skipDirs are pruned
// before anything under them is enumerated, so a pruned subtree is recorded on
// rep as ONE skip_dir entry naming the directory rather than one per file
// beneath it — counting the files would mean walking the very subtrees the prune
// exists to avoid. opts.LiftExclusions descends into them and applies no rule.
//
// opts.PackagePrefixes prune the same way, and are the walk-side counterpart of
// the git pathspecs: a directory no prefix can reach is skipped whole.
func discoverWithWalk(repoDir string, opts DiscoveryOptions, rep *DiscoveryReport) ([]string, error) {
	var files []string
	err := filepath.WalkDir(repoDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Out-of-scope subtrees are pruned FIRST and silently: a directory
			// the caller's prefixes cannot reach was never a candidate, so it is
			// neither walked nor charged to an exclusion rule.
			if !dirCanContainPrefix(relOrPath(repoDir, path), opts.PackagePrefixes) {
				return filepath.SkipDir
			}
			if skipDirs[d.Name()] && !opts.LiftExclusions {
				rep.record(RuleDir, relOrPath(repoDir, path))
				return filepath.SkipDir
			}
			return nil
		}

		rel, relErr := filepath.Rel(repoDir, path)
		if relErr != nil {
			slog.Warn("discover: relative path error, skipping", "path", path, "error", relErr)
			return nil // relative path error is non-fatal, skip file
		}
		// Same rule for a file the prune could not catch — a prefix naming one
		// file leaves its siblings in a directory the walk must still enter.
		if !MatchesPathPrefixes(rel, opts.PackagePrefixes) {
			return nil
		}
		if !opts.LiftExclusions {
			if ok, rule := isIndexable(repoDir, rel); !ok {
				rep.record(rule, rel)
				return nil
			}
		}
		files = append(files, rel)
		return nil
	})
	return files, err
}

// generatedCodeMarker is the Go convention line for generated files
// (golang.org/s/generatedcode): "// Code generated <by tool> DO NOT EDIT.",
// required to appear before the package clause. Matched as a prefix/suffix
// pair rather than a regexp — the convention's variable middle is free text.
const (
	generatedCodeMarkerPrefix = "// Code generated "
	generatedCodeMarkerSuffix = " DO NOT EDIT."
)

// hasGeneratedCodeHeader reports whether the file's head — up to the package
// clause, capped at 4KB — carries the generated-code marker. An unreadable
// candidate reports FALSE: a file we cannot read should fail loudly at parse
// time, not vanish silently under a "generated" label.
func hasGeneratedCodeHeader(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(io.LimitReader(f, 4096))
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, generatedCodeMarkerPrefix) && strings.HasSuffix(line, generatedCodeMarkerSuffix) {
			return true
		}
		if strings.HasPrefix(line, "package ") {
			return false
		}
	}
	return false
}

// relOrPath renders a walked absolute path repo-relative for reporting, falling
// back to the absolute path when it cannot be relativized.
func relOrPath(repoDir, path string) string {
	if rel, err := filepath.Rel(repoDir, path); err == nil {
		return rel
	}
	return path
}

// isIndexable checks if a file should be included in the code graph. It returns
// the name of the rule that declined the file alongside the verdict — empty when
// the file is indexable — so a caller can attribute an absence to the rule that
// caused it rather than reporting it as "no match". The rule names are the
// vocabulary the walk-exclusion census is keyed on; the test order below is
// first-match-wins and is itself load-bearing, since a vendored file over the
// size cap is a skip_path_component and never a skip_too_large.
func isIndexable(repoDir, rel string) (bool, string) {
	name := filepath.Base(rel)
	ext := filepath.Ext(name)

	// Skip markdown — documentation, not code.
	if skipExtensions[ext] {
		return false, RuleExtension
	}

	// Skip lock files — dependency resolution data, not code.
	if skipFiles[name] {
		return false, RuleLockfile
	}

	// Skip TypeScript declaration files — type signatures only, no implementation.
	if strings.HasSuffix(name, ".d.ts") || strings.HasSuffix(name, ".d.mts") || strings.HasSuffix(name, ".d.cts") {
		return false, RuleDTS
	}

	// Skip generated Go files. .pb.go is suffix-sufficient — the protoc
	// convention is universal. The _gen.go/_generated.go suffixes are only a
	// NAMING HINT: hand-written files legitimately carry them (this repo alone
	// had four, all invisible to the graph until 2026-08-18), so those two
	// exclude ONLY when the file itself declares generation via the Go
	// convention marker (golang.org/s/generatedcode). The head read costs one
	// small open per suffix-matched candidate, never per walked file.
	if strings.HasSuffix(name, ".pb.go") {
		return false, RuleGeneratedGo
	}
	if (strings.HasSuffix(name, "_generated.go") || strings.HasSuffix(name, "_gen.go")) &&
		hasGeneratedCodeHeader(filepath.Join(repoDir, rel)) {
		return false, RuleGeneratedGo
	}

	// Skip files in third-party/vendored/generated directories.
	// git ls-files doesn't use skipDirs, so we check path components here.
	for _, dir := range skipPathComponents {
		if strings.Contains(rel, "/"+dir+"/") || strings.HasPrefix(rel, dir+"/") {
			return false, RulePathComponent
		}
	}

	// Skip unsupported languages.
	if treesitter.DetectLanguage(name) == treesitter.LangUnknown {
		return false, RuleUnknownLang
	}

	// Skip large files. A path that fails to stat at all (listed by git but
	// since removed, or unreadable) is declined here too and charged to the same
	// rule: the size cap is the only bucket the exclusion vocabulary carries for
	// this branch, so the count is honest about the file being declined and
	// approximate about which of the two conditions declined it.
	info, err := os.Stat(filepath.Join(repoDir, rel))
	if err != nil || info.Size() > maxFileSize {
		return false, RuleTooLarge
	}

	return true, ""
}
