// SPDX-License-Identifier: Apache-2.0

// long_tail_lang_deny_test.go — covers that each of the 12
// explicitly denied languages, when passed to Compile, returns an error
// containing the canonical "pattern matching not supported for language"
// prefix and the language name. No registration, no LangConfig — the deny set
// in lang_config.go short-circuits Compile before registry lookup. Eleven are
// config/markup grammars; php is denied for a sigil collision (a PHP variable
// uses the same `$` the pattern DSL reserves), asserted below.

package ast

import (
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

func TestLongTail_DeniedLanguages_CompileReturnsExplicitError(t *testing.T) {
	cases := []struct {
		name string
		lang treesitter.Language
	}{
		{"yaml", treesitter.LangYaml},
		{"toml", treesitter.LangToml},
		{"css", treesitter.LangCSS},
		{"html", treesitter.LangHTML},
		{"sql", treesitter.LangSQL},
		{"dockerfile", treesitter.LangDockerfile},
		{"cue", treesitter.LangCue},
		{"svelte", treesitter.LangSvelte},
		{"markdown", treesitter.LangMarkdown},
		{"protobuf", treesitter.LangProtobuf},
		{"hcl", treesitter.LangHCL},
		{"php", treesitter.LangPHP},
	}

	pat := Pattern{Source: "$X"}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cp, err := Compile(pat, tc.lang, "")
			if cp != nil {
				cp.Close()
				t.Fatalf("Compile(%q) returned non-nil CompiledPattern; want error", tc.lang)
			}
			if err == nil {
				t.Fatalf("Compile(%q) returned nil error; want deny error", tc.lang)
			}
			msg := err.Error()
			if !strings.Contains(msg, "pattern matching not supported for language") {
				t.Errorf("Compile(%q) error = %q; missing canonical \"pattern matching not supported for language\" prefix", tc.lang, msg)
			}
			if !strings.Contains(msg, string(tc.lang)) {
				t.Errorf("Compile(%q) error = %q; missing language name in message", tc.lang, msg)
			}
			// PHP is denied for the sigil collision, not the markup rationale.
			// Pin the reason by a test, not just by source.
			if tc.lang == treesitter.LangPHP && !strings.Contains(msg, "sigil") {
				t.Errorf("Compile(php) error = %q; PHP deny reason must name the sigil collision", msg)
			}
		})
	}
}

// TestLongTail_DeniedLanguages_NoLangConfigRegistered confirms the set never
// got a LangConfig registered (defense-in-depth — if a future contributor
// accidentally adds a LangConfig for a denied language, this test reminds
// them the deny set is the source of truth).
func TestLongTail_DeniedLanguages_NoLangConfigRegistered(t *testing.T) {
	denied := []treesitter.Language{
		treesitter.LangYaml,
		treesitter.LangToml,
		treesitter.LangCSS,
		treesitter.LangHTML,
		treesitter.LangSQL,
		treesitter.LangDockerfile,
		treesitter.LangCue,
		treesitter.LangSvelte,
		treesitter.LangMarkdown,
		treesitter.LangProtobuf,
		treesitter.LangHCL,
		treesitter.LangPHP,
	}
	for _, lang := range denied {
		t.Run(string(lang), func(t *testing.T) {
			if _, ok := langConfigFor(lang); ok {
				t.Errorf("langConfigFor(%q) returned ok=true; denied languages must NOT be registered", lang)
			}
		})
	}
}
