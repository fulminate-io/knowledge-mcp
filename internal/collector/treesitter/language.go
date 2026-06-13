// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"path/filepath"
	"sync"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/bash"
	"github.com/smacker/go-tree-sitter/c"
	"github.com/smacker/go-tree-sitter/cpp"
	"github.com/smacker/go-tree-sitter/csharp"
	"github.com/smacker/go-tree-sitter/css"
	"github.com/smacker/go-tree-sitter/cue"
	"github.com/smacker/go-tree-sitter/dockerfile"
	"github.com/smacker/go-tree-sitter/elixir"
	"github.com/smacker/go-tree-sitter/elm"
	"github.com/smacker/go-tree-sitter/golang"
	"github.com/smacker/go-tree-sitter/groovy"
	"github.com/smacker/go-tree-sitter/hcl"
	"github.com/smacker/go-tree-sitter/html"
	"github.com/smacker/go-tree-sitter/java"
	"github.com/smacker/go-tree-sitter/javascript"
	"github.com/smacker/go-tree-sitter/kotlin"
	"github.com/smacker/go-tree-sitter/lua"
	tree_sitter_markdown "github.com/smacker/go-tree-sitter/markdown/tree-sitter-markdown"
	"github.com/smacker/go-tree-sitter/ocaml"
	"github.com/smacker/go-tree-sitter/php"
	"github.com/smacker/go-tree-sitter/protobuf"
	"github.com/smacker/go-tree-sitter/python"
	"github.com/smacker/go-tree-sitter/ruby"
	"github.com/smacker/go-tree-sitter/rust"
	"github.com/smacker/go-tree-sitter/scala"
	"github.com/smacker/go-tree-sitter/sql"
	"github.com/smacker/go-tree-sitter/svelte"
	"github.com/smacker/go-tree-sitter/swift"
	"github.com/smacker/go-tree-sitter/toml"
	"github.com/smacker/go-tree-sitter/typescript/tsx"
	"github.com/smacker/go-tree-sitter/typescript/typescript"
	"github.com/smacker/go-tree-sitter/yaml"
)

// Language represents a supported programming language.
type Language string

const (
	LangGo         Language = "go"
	LangTypeScript Language = "typescript"
	// LangTSX is the JSX-capable TypeScript dialect (.tsx). It rides the
	// separate tree-sitter typescript/tsx grammar (a strict superset of the
	// typescript grammar) while reusing the same tsQueries — the only upstream
	// library that splits JSX into a sibling grammar, so .tsx needs a distinct
	// Language while .jsx does not (the javascript grammar is JSX-capable).
	LangTSX        Language = "tsx"
	LangPython     Language = "python"
	LangJava       Language = "java"
	LangRust       Language = "rust"
	LangC          Language = "c"
	LangCPP        Language = "cpp"
	LangCSharp     Language = "csharp"
	LangJavaScript Language = "javascript"
	LangRuby       Language = "ruby"
	LangPHP        Language = "php"
	LangSwift      Language = "swift"
	LangKotlin     Language = "kotlin"
	LangScala      Language = "scala"
	LangElixir     Language = "elixir"
	LangLua        Language = "lua"
	LangBash       Language = "bash"
	LangGroovy     Language = "groovy"
	LangElm        Language = "elm"
	LangOCaml      Language = "ocaml"
	LangHCL        Language = "hcl"
	LangProtobuf   Language = "protobuf"
	LangCSS        Language = "css"
	LangHTML       Language = "html"
	LangSQL        Language = "sql"
	LangDockerfile Language = "dockerfile"
	LangSvelte     Language = "svelte"
	LangToml       Language = "toml"
	LangYaml       Language = "yaml"
	LangMarkdown   Language = "markdown"
	LangCue        Language = "cue"
	LangUnknown    Language = "unknown"
)

// TestKind classifies the kind of test code a chunk or node represents.
// The 9-value contract is locked by design:
// test, benchmark, example, fuzz, setup, teardown, fixture, mock, helper. Each
// kind answers a distinct intent — collapsing them would lose the ability to
// rerank or filter per-intent. The empty string (TestKindNone) is the zero
// value meaning "not classified / not test code".
//
// Producer-side type only: `type TestKind string` lives in this package; the
// persisted node's TestKind field is plain `string` (rule 42dba7de — store
// does not import domain packages). The cast happens at chunk-to-node time.
// Mirrors how `type Language string` is producer-typed and stored as `string`.
type TestKind string

const (
	TestKindNone      TestKind = ""
	TestKindTest      TestKind = "test"
	TestKindBenchmark TestKind = "benchmark"
	TestKindExample   TestKind = "example"
	TestKindFuzz      TestKind = "fuzz"
	TestKindSetup     TestKind = "setup"
	TestKindTeardown  TestKind = "teardown"
	TestKindFixture   TestKind = "fixture"
	TestKindMock      TestKind = "mock"
	TestKindHelper    TestKind = "helper"
)

// langEntry maps a Language to its tree-sitter grammar and query definitions.
type langEntry struct {
	lang    *sitter.Language
	queries func() *QuerySet
	cached  *QuerySet
	once    sync.Once
}

// Queries returns the QuerySet, initializing it on first call (thread-safe).
// The QuerySet contains S-expression strings; actual sitter.NewQuery() compilation
// happens in the Chunker when executing queries against the AST.
func (e *langEntry) Queries() *QuerySet {
	e.once.Do(func() {
		e.cached = e.queries()
	})
	return e.cached
}

// registry maps Language → tree-sitter grammar + queries.
var registry = map[Language]*langEntry{
	LangGo:         {lang: golang.GetLanguage(), queries: goQueries},
	LangTypeScript: {lang: typescript.GetLanguage(), queries: tsQueries},
	// LangTSX reuses tsQueries against the JSX-capable tsx grammar: tsx is a
	// strict superset of typescript, so every kind tsQueries captures exists
	// in it (TestAllLanguageQueriesCompile auto-covers this entry).
	LangTSX:        {lang: tsx.GetLanguage(), queries: tsQueries},
	LangPython:     {lang: python.GetLanguage(), queries: pythonQueries},
	LangJava:       {lang: java.GetLanguage(), queries: javaQueries},
	LangRust:       {lang: rust.GetLanguage(), queries: rustQueries},
	LangC:          {lang: c.GetLanguage(), queries: cQueries},
	LangCPP:        {lang: cpp.GetLanguage(), queries: cppQueries},
	LangCSharp:     {lang: csharp.GetLanguage(), queries: csharpQueries},
	LangJavaScript: {lang: javascript.GetLanguage(), queries: jsQueries},
	LangRuby:       {lang: ruby.GetLanguage(), queries: rubyQueries},
	LangPHP:        {lang: php.GetLanguage(), queries: phpQueries},
	LangSwift:      {lang: swift.GetLanguage(), queries: swiftQueries},
	LangKotlin:     {lang: kotlin.GetLanguage(), queries: kotlinQueries},
	LangScala:      {lang: scala.GetLanguage(), queries: scalaQueries},
	LangElixir:     {lang: elixir.GetLanguage(), queries: elixirQueries},
	LangLua:        {lang: lua.GetLanguage(), queries: luaQueries},
	LangBash:       {lang: bash.GetLanguage(), queries: bashQueries},
	LangGroovy:     {lang: groovy.GetLanguage(), queries: groovyQueries},
	LangElm:        {lang: elm.GetLanguage(), queries: elmQueries},
	LangOCaml:      {lang: ocaml.GetLanguage(), queries: ocamlQueries},
	LangHCL:        {lang: hcl.GetLanguage(), queries: hclQueries},
	LangProtobuf:   {lang: protobuf.GetLanguage(), queries: protobufQueries},
	LangCSS:        {lang: css.GetLanguage(), queries: cssQueries},
	LangHTML:       {lang: html.GetLanguage(), queries: htmlQueries},
	LangSQL:        {lang: sql.GetLanguage(), queries: sqlQueries},
	LangDockerfile: {lang: dockerfile.GetLanguage(), queries: dockerfileQueries},
	LangSvelte:     {lang: svelte.GetLanguage(), queries: svelteQueries},
	LangToml:       {lang: toml.GetLanguage(), queries: tomlQueries},
	LangYaml:       {lang: yaml.GetLanguage(), queries: yamlQueries},
	LangMarkdown:   {lang: tree_sitter_markdown.GetLanguage(), queries: markdownQueries},
	LangCue:        {lang: cue.GetLanguage(), queries: cueQueries},
}

// extMap maps file extensions to Language.
var extMap = map[string]Language{
	".go":       LangGo,
	".ts":       LangTypeScript,
	".tsx":      LangTSX,
	".py":       LangPython,
	".pyi":      LangPython,
	".java":     LangJava,
	".rs":       LangRust,
	".c":        LangC,
	".h":        LangC,
	".cpp":      LangCPP,
	".cc":       LangCPP,
	".cxx":      LangCPP,
	".hpp":      LangCPP,
	".hh":       LangCPP,
	".cs":       LangCSharp,
	".js":       LangJavaScript,
	".jsx":      LangJavaScript,
	".mjs":      LangJavaScript,
	".rb":       LangRuby,
	".php":      LangPHP,
	".swift":    LangSwift,
	".kt":       LangKotlin,
	".kts":      LangKotlin,
	".scala":    LangScala,
	".sc":       LangScala,
	".ex":       LangElixir,
	".exs":      LangElixir,
	".lua":      LangLua,
	".sh":       LangBash,
	".bash":     LangBash,
	".zsh":      LangBash,
	".bats":     LangBash,
	".groovy":   LangGroovy,
	".gradle":   LangGroovy,
	".elm":      LangElm,
	".ml":       LangOCaml,
	".mli":      LangOCaml,
	".tf":       LangHCL,
	".hcl":      LangHCL,
	".proto":    LangProtobuf,
	".css":      LangCSS,
	".scss":     LangCSS,
	".html":     LangHTML,
	".htm":      LangHTML,
	".sql":      LangSQL,
	".svelte":   LangSvelte,
	".toml":     LangToml,
	".yaml":     LangYaml,
	".yml":      LangYaml,
	".md":       LangMarkdown,
	".markdown": LangMarkdown,
	".cue":      LangCue,
}

// DetectLanguage returns the Language for a file path based on extension.
// Special cases like Dockerfile (no extension) are handled by filename.
func DetectLanguage(path string) Language {
	ext := filepath.Ext(path)
	if lang, ok := extMap[ext]; ok {
		return lang
	}
	// Handle extensionless files by filename.
	base := filepath.Base(path)
	switch base {
	case "Dockerfile", "dockerfile":
		return LangDockerfile
	case "Makefile", "makefile", "GNUmakefile":
		return LangBash
	}
	return LangUnknown
}

// LanguageGrammar returns the underlying tree-sitter grammar for a language,
// suitable for passing to sitter.NewQuery. Returns (nil, false) for LangUnknown
// or any language not in the registry. cmd/knowledge/internal/ast uses this
// to compile runtime-built S-expression queries against ASTs produced by Parser.Parse;
// the chunker continues to use the package-internal registry directly.
func LanguageGrammar(l Language) (*sitter.Language, bool) {
	entry, ok := registry[l]
	if !ok {
		return nil, false
	}
	return entry.lang, true
}
