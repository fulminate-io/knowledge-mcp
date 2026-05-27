// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"bufio"
	"context"
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
func DiscoverFiles(ctx context.Context, repoDir string) ([]string, error) {
	files, err := discoverWithGit(ctx, repoDir)
	if err != nil {
		slog.Warn("git ls-files failed, falling back to filesystem walk", "error", err)
		return discoverWithWalk(repoDir)
	}
	return files, nil
}

// discoverWithGit uses `git ls-files` to get tracked files, respecting .gitignore.
func discoverWithGit(ctx context.Context, repoDir string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "ls-files", "--cached", "--others", "--exclude-standard")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var files []string
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		rel := scanner.Text()
		if !isIndexable(repoDir, rel) {
			continue
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
	// Third-party vendored code (different names across ecosystems).
	"deps": true, "thirdparty": true, "third_party": true, "third-party": true,
	// Generated code output directories.
	"generated": true, ".generated": true,
}

func discoverWithWalk(repoDir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(repoDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}

		rel, relErr := filepath.Rel(repoDir, path)
		if relErr != nil {
			slog.Warn("discover: relative path error, skipping", "path", path, "error", relErr)
			return nil // relative path error is non-fatal, skip file
		}
		if !isIndexable(repoDir, rel) {
			return nil
		}
		files = append(files, rel)
		return nil
	})
	return files, err
}

// isIndexable checks if a file should be included in the code graph.
func isIndexable(repoDir, rel string) bool {
	name := filepath.Base(rel)
	ext := filepath.Ext(name)

	// Skip markdown — documentation, not code.
	if skipExtensions[ext] {
		return false
	}

	// Skip lock files — dependency resolution data, not code.
	if skipFiles[name] {
		return false
	}

	// Skip TypeScript declaration files — type signatures only, no implementation.
	if strings.HasSuffix(name, ".d.ts") || strings.HasSuffix(name, ".d.mts") || strings.HasSuffix(name, ".d.cts") {
		return false
	}

	// Skip generated Go files.
	if strings.HasSuffix(name, ".pb.go") || strings.HasSuffix(name, "_generated.go") || strings.HasSuffix(name, "_gen.go") {
		return false
	}

	// Skip files in third-party/vendored/generated directories.
	// git ls-files doesn't use skipDirs, so we check path components here.
	for _, dir := range skipPathComponents {
		if strings.Contains(rel, "/"+dir+"/") || strings.HasPrefix(rel, dir+"/") {
			return false
		}
	}

	// Skip unsupported languages.
	if treesitter.DetectLanguage(name) == treesitter.LangUnknown {
		return false
	}

	// Skip large files.
	info, err := os.Stat(filepath.Join(repoDir, rel))
	if err != nil || info.Size() > maxFileSize {
		return false
	}

	return true
}
