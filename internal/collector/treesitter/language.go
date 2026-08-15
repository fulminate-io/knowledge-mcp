// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"path/filepath"
	"slices"
	"strings"
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
// persisted node's TestKind field is plain `string` (the store does not
// import domain packages). The cast happens at chunk-to-node time.
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
	// Every entry below was parse-tested against the grammar it routes to,
	// with HasError() read on the resulting root. An unmapped extension is
	// TOTAL ABSENCE rather than degraded chunking: DetectLanguage falls through
	// to LangUnknown and the discovery gate then declines the file outright, so
	// it never reaches a chunker at all.
	".cjs":     LangJavaScript,
	".mts":     LangTypeScript,
	".cts":     LangTypeScript,
	".hxx":     LangCPP,
	".c++":     LangCPP,
	".h++":     LangCPP,
	".ipp":     LangCPP,
	".tpp":     LangCPP,
	".inl":     LangCPP,
	".csx":     LangCSharp,
	".rake":    LangRuby,
	".gemspec": LangRuby,
	".ru":      LangRuby,
	".sbt":     LangScala,
	".tfvars":  LangHCL,
	".phtml":   LangPHP,
	".pyw":     LangPython,
	".ksh":     LangBash,
	".pgsql":   LangSQL,
	".mysql":   LangSQL,
	".gvy":     LangGroovy,
	".gy":      LangGroovy,
	// .mdx parses clean as markdown, but its JSX is treated as prose.
	// Degraded rather than broken, and strictly better than not being indexed.
	".mdx": LangMarkdown,
}

// DELIBERATELY ABSENT, each measured as haserror=true under the grammar it
// would have routed to, so the absence is a decision rather than an oversight:
//
//	.less, .sass  — their own syntaxes, not CSS
//	.heex         — a template language, not Elixir
//	.xhtml        — an XML prolog, not HTML
//
// The discriminating control ran in the same binary: a well-formed source file
// gives haserror=false under its grammar while a deliberately malformed one
// gives true, so a true above is a property of the input rather than of the
// harness. No deny-list table is needed — DetectLanguage's fallthrough already
// denies everything not listed.
//
// TWO ROUTING PROBLEMS EXTENSIONS CANNOT FIX, recorded here rather than
// attempted:
//
//   - `.h` routes to LangC, which sends C++ headers to the C grammar. Measured:
//     a header holding a template class and a namespace gives haserror=true
//     under c and haserror=false under cpp. That is a WRONG-GRAMMAR problem,
//     not a missing-extension one, and a `.h` may legitimately be either
//     language, so it needs content sniffing rather than a table entry.
//   - `.mli` routes to LangOCaml, which is the IMPLEMENTATION grammar. An
//     interface file gives haserror=true and every `val` signature produces no
//     match. The pinned binding exposes no OCaml interface grammar at all, so
//     this cannot be fixed at the current pin.

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
	case "Rakefile", "Gemfile":
		return LangRuby
	case "Jenkinsfile":
		return LangGroovy
	}
	// `Dockerfile.dev` routes nowhere through either path above: filepath.Ext
	// returns ".dev", which is absent from extMap, and filepath.Base returns
	// "Dockerfile.dev", which the switch does not match. extMap is consulted
	// FIRST, so a file genuinely named `Dockerfile.go` still routes to Go.
	if strings.HasPrefix(base, "Dockerfile.") || strings.HasPrefix(base, "dockerfile.") {
		return LangDockerfile
	}
	// Gemfile.lock is deliberately not routed here: it is a lockfile and
	// discovery already declines it, so a rule here would be overridden
	// elsewhere. CMakeLists.txt is likewise absent — there is no cmake grammar
	// in the registry, so a filename rule would route it to nothing.
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

// LanguageNamingSources reports the three places a declaration's name can come
// from for one language: the TopLevel query's captures, the TestBlocks query,
// and a registered declaration-name resolver.
//
// It exposes the SOURCES rather than a participation verdict, so the rule that
// combines them lives with the gate that cares — and so a gate outside this
// package derives participation from the query text itself instead of from a
// hand-maintained list that rots the first time a query is tightened.
// Everything is zero-valued for an unregistered language.
func LanguageNamingSources(l Language) (topLevel, testBlocks string, hasDeclNameResolver bool) {
	entry, ok := registry[l]
	if !ok {
		return "", "", false
	}
	qs := entry.Queries()
	_, hasResolver := declNameResolvers[l]
	return qs.TopLevel, qs.TestBlocks, hasResolver
}

// RegisteredLanguages returns every language the chunker can parse, sorted by
// name so a caller iterating it runs in a stable order rather than a map's.
//
// It exists so a coverage gate OUTSIDE this package can derive its subject list
// from the registry itself: a table that enumerates languages by hand silently
// shrinks its own proof the day a language is added, while one derived from
// here fails until the new language is accounted for. LangUnknown is absent
// because it is not in the registry.
func RegisteredLanguages() []Language {
	out := make([]Language, 0, len(registry))
	for lang := range registry {
		out = append(out, lang)
	}
	slices.Sort(out)
	return out
}
