// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// testRepo maps a language to a cloned public repository for integration testing.
type testRepo struct {
	lang       Language
	dir        string   // directory name under ~/code/test-repos/
	extensions []string // file extensions to scan
}

var testRepos = []testRepo{
	{LangGo, "go-kubernetes", []string{".go"}},
	{LangTypeScript, "ts-vscode", []string{".ts", ".tsx"}},
	{LangPython, "py-django", []string{".py"}},
	{LangJava, "java-spring", []string{".java"}},
	{LangRust, "rust-tokio", []string{".rs"}},
	{LangC, "c-redis", []string{".c", ".h"}},
	{LangCPP, "cpp-json", []string{".cpp", ".cc", ".hpp"}},
	{LangJavaScript, "js-react", []string{".js", ".jsx"}},
	{LangCSharp, "cs-roslyn", []string{".cs"}},
	{LangRuby, "rb-rails", []string{".rb"}},
	{LangPHP, "php-laravel", []string{".php"}},
	{LangSwift, "swift-vapor", []string{".swift"}},
	{LangKotlin, "kt-okhttp", []string{".kt"}},
	{LangScala, "scala-akka", []string{".scala"}},
	{LangElixir, "ex-phoenix", []string{".ex", ".exs"}},
	{LangLua, "lua-openresty", []string{".lua"}},
	{LangBash, "bash-ohmyzsh", []string{".sh", ".zsh"}},
	{LangGroovy, "groovy-gradle", []string{".groovy", ".gradle"}},
	{LangElm, "elm-compiler", []string{".elm"}},
	{LangOCaml, "ocaml-dune", []string{".ml", ".mli"}},
	{LangHCL, "hcl-terraform-vpc", []string{".tf"}},
	{LangProtobuf, "proto-grpc", []string{".proto"}},
	{LangCSS, "css-bootstrap", []string{".css", ".scss"}},
	{LangHTML, "html-boilerplate", []string{".html"}},
	{LangSQL, "sql-flyway", []string{".sql"}},
	{LangSvelte, "svelte-kit", []string{".svelte"}},
	{LangToml, "toml-hugo", []string{".toml"}},
	{LangYaml, "yaml-ansible", []string{".yaml", ".yml"}},
	{LangMarkdown, "md-github-docs", []string{".md"}},
	{LangCue, "cue-cuelang", []string{".cue"}},
	{LangDockerfile, "docker-official", []string{}}, // handled by filename
}

// langChunkResult captures one language's chunk-rate measurement.
type langChunkResult struct {
	lang       Language
	files      int
	chunks     int
	edges      int
	errors     int
	duration   time.Duration
	skipped    bool
	skipReason string
}

// BenchmarkMultiLangRealRepos chunks every supported language's reference
// repository in parallel and reports per-language and aggregate throughput.
// A benchmark (not a test) for the same reasons as
// BenchmarkMultiLangGraphLoad: depends on cloned repos at
// ~/code/test-repos/, never present on CI/CD, and the work is throughput
// measurement (files/sec). Doesn't run as part of `go test`; opt in with
// `go test -bench MultiLangRealRepos`.
//
// Languages run as parallel goroutines — each builds its own Chunker
// (parser is not thread-safe per package CLAUDE.md). Per-chunk
// correctness invariants (non-empty content, ordered line range) are
// still asserted via b.Errorf so the benchmark also smoke-tests every
// language's grammar and query set.
func BenchmarkMultiLangRealRepos(b *testing.B) {
	homeDir, err := os.UserHomeDir()
	require.NoError(b, err)
	reposDir := filepath.Join(homeDir, "code", "test-repos")

	if _, err := os.Stat(reposDir); os.IsNotExist(err) {
		b.Skipf("test-repos directory not found at %s — clone repos first", reposDir)
	}

	for b.Loop() {
		runMultiLangRealRepos(b, reposDir)
	}
}

func runMultiLangRealRepos(b *testing.B, reposDir string) {
	b.Helper()

	var (
		mu      sync.Mutex
		results []langChunkResult
		wg      sync.WaitGroup
	)

	for _, repo := range testRepos {
		wg.Add(1)
		go func(r testRepo) {
			defer wg.Done()
			res := runOneLangRealRepo(b, reposDir, r)
			mu.Lock()
			results = append(results, res)
			mu.Unlock()
		}(repo)
	}
	wg.Wait()

	sort.Slice(results, func(i, j int) bool { return string(results[i].lang) < string(results[j].lang) })
	logRealReposSummary(b, results)
}

func runOneLangRealRepo(b *testing.B, reposDir string, repo testRepo) langChunkResult {
	dir := filepath.Join(reposDir, repo.dir)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return langChunkResult{lang: repo.lang, skipped: true, skipReason: "not cloned"}
	}

	files := collectRealRepoFiles(b, dir, repo)
	if len(files) == 0 {
		return langChunkResult{lang: repo.lang, skipped: true, skipReason: "no files"}
	}

	// Per-goroutine chunker — tree-sitter Parser is not thread-safe.
	chunker := NewChunker()
	defer chunker.Close()

	var totalChunks, totalEdges, errorCount int
	start := time.Now()

	for _, path := range files {
		src, err := os.ReadFile(path) // #nosec G304 — paths come from filepath.Walk under reposDir
		if err != nil {
			errorCount++
			continue
		}
		result, err := chunker.ChunkFile(context.Background(), path, src)
		if err != nil {
			errorCount++
			continue
		}
		for _, chunk := range result.Chunks {
			if chunk.Content == "" {
				b.Errorf("%s: empty chunk in %s", repo.lang, path)
			}
			if chunk.StartLine <= 0 {
				b.Errorf("%s: invalid start line in %s", repo.lang, path)
			}
			if chunk.EndLine < chunk.StartLine {
				b.Errorf("%s: end < start in %s", repo.lang, path)
			}
		}
		totalChunks += len(result.Chunks)
		totalEdges += len(result.Edges)
	}

	duration := time.Since(start)

	if totalChunks == 0 {
		b.Errorf("%s: expected chunks from %d files but got 0", repo.lang, len(files))
	}
	if len(files) > 10 {
		errorRate := float64(errorCount) / float64(len(files))
		if errorRate >= 0.05 {
			b.Errorf("%s: error rate %.1f%% too high (%d errors in %d files)",
				repo.lang, errorRate*100, errorCount, len(files))
		}
	}

	return langChunkResult{
		lang: repo.lang, files: len(files),
		chunks: totalChunks, edges: totalEdges, errors: errorCount,
		duration: duration,
	}
}

func collectRealRepoFiles(b *testing.B, dir string, repo testRepo) []string {
	var files []string
	extSet := make(map[string]bool)
	for _, ext := range repo.extensions {
		extSet[ext] = true
	}
	err := filepath.Walk(dir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			b.Logf("skipping inaccessible path: %s: %v", path, walkErr)
			return nil
		}
		if info.IsDir() {
			base := info.Name()
			if base == ".git" || base == "vendor" || base == "node_modules" || base == "build" || base == "dist" || base == "target" || base == "__pycache__" {
				return filepath.SkipDir
			}
			return nil
		}
		if len(files) >= 500 {
			return nil
		}
		if repo.lang == LangDockerfile {
			base := filepath.Base(path)
			if base == "Dockerfile" || base == "dockerfile" || strings.HasPrefix(base, "Dockerfile.") {
				files = append(files, path)
			}
			return nil
		}
		if extSet[filepath.Ext(path)] {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		b.Errorf("walk %s: %v", dir, err)
		return nil
	}
	return files
}

func logRealReposSummary(b *testing.B, results []langChunkResult) {
	b.Helper()
	b.Log("\n=== Multi-Language Chunking Summary ===")
	b.Logf("%-12s %6s %7s %7s %6s %10s %12s", "Language", "Files", "Chunks", "Edges", "Errs", "Time", "Files/sec")
	b.Log(strings.Repeat("-", 68))

	var totalFiles, totalChunks, totalEdges, totalErrors int
	var totalDuration time.Duration
	for _, r := range results {
		if r.skipped {
			b.Logf("%-12s   (skipped: %s)", r.lang, r.skipReason)
			continue
		}
		fps := float64(r.files) / r.duration.Seconds()
		b.Logf("%-12s %6d %7d %7d %6d %10v %10.0f/s",
			r.lang, r.files, r.chunks, r.edges, r.errors,
			r.duration.Round(time.Millisecond), fps)
		totalFiles += r.files
		totalChunks += r.chunks
		totalEdges += r.edges
		totalErrors += r.errors
		totalDuration += r.duration
	}
	b.Log(strings.Repeat("-", 68))
	if totalDuration > 0 {
		fps := float64(totalFiles) / totalDuration.Seconds()
		b.Logf("%-12s %6d %7d %7d %6d %10v %10.0f/s",
			"TOTAL", totalFiles, totalChunks, totalEdges, totalErrors,
			totalDuration.Round(time.Millisecond), fps)
	}
}
