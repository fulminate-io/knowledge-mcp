// SPDX-License-Identifier: Apache-2.0

// wrapper_context_test.go — the wrapper context vocabulary. What the census
// next door measures is written in the terms this file checks.
//
// TestWrapperContextLabels checks the vocabulary is complete and honest:
// every registered wrapper carries one of the four context constants, and the
// two grammars whose wrapper NAMED "decl" is really a class body carry the
// member context there.
//
// TestRegisterLangConfigRejectsUnlabelledWrapper checks the registration-time
// validation actually rejects, against a control that must be accepted.
//
// THE REGISTRY IS THE SUBJECT, NEVER A HARDCODED LIST. The tests here and in
// wrapper_census_test.go iterate the LIVE registry and assert a floor on its
// size. An exhaustive-over-the-list assertion is trivially true over a list
// that shrank, so a dropped registration must fail rather than quietly reduce
// what is measured.

package ast

import (
	"context"
	"slices"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// registeredLangFloor is the number of languages registered when this test
// was written. It is a FLOOR, not an equality: adding a grammar is expected
// and must not fail the build, while losing one makes every
// exhaustive-over-the-registry assertion in this file weaker and must.
//
// Lowered 21 -> 20 when PHP was deliberately moved to the deny set for a sigil
// collision (a PHP variable uses the same `$` the pattern DSL reserves) — see
// deniedLanguages in lang_config.go. That is an intended registry change, not
// the accidental loss this tripwire guards against.
const registeredLangFloor = 20

// registeredConfigs returns every registered (language, config) pair, sorted
// by language name so subtests run in a deterministic order.
func registeredConfigs(t *testing.T) []struct {
	lang treesitter.Language
	cfg  LangConfig
} {
	t.Helper()
	names := registeredLangs()
	out := make([]struct {
		lang treesitter.Language
		cfg  LangConfig
	}, 0, len(names))
	for _, name := range names {
		lang := treesitter.Language(name)
		cfg, ok := langConfigFor(lang)
		require.True(t, ok, "registeredLangs listed %s but langConfigFor missed it", name)
		out = append(out, struct {
			lang treesitter.Language
			cfg  LangConfig
		}{lang: lang, cfg: cfg})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].lang < out[j].lang })
	return out
}

// TestWrapperContextLabels asserts the context vocabulary covers the whole
// live registry, and pins the one relabel that changed a wrapper's meaning.
func TestWrapperContextLabels(t *testing.T) {
	configs := registeredConfigs(t)
	require.GreaterOrEqual(t, len(configs), registeredLangFloor,
		"the registry holds %d languages, below the floor of %d — a dropped registration makes every exhaustive assertion here weaker",
		len(configs), registeredLangFloor)

	for _, entry := range configs {
		require.NotEmpty(t, entry.cfg.Wrappers,
			"language %s registered no context wrappers, so no pattern can compile for it", entry.lang)
		for _, w := range entry.cfg.Wrappers {
			require.Truef(t, isValidContext(w.Context),
				"language %s wrapper %q carries Context %q, which is outside the caller-facing vocabulary %q/%q/%q/%q",
				entry.lang, w.Name, w.Context, contextDecl, contextStmt, contextExpr, contextMember)
		}
	}

	// The substantive claim of the relabel. Java and C# both register a
	// wrapper NAMED "decl" whose Prefix opens a class body — that is the
	// member context, and mislabeling it as a declaration is why a
	// statement-shaped pattern silently compiles to a field declaration.
	// Their empty-prefix wrapper, named "top", is the real declaration
	// context. A whole-table pass could be reached by labeling everything
	// decl, so this claim is asserted on its own.
	t.Run("java_class_body_is_member", func(t *testing.T) {
		for _, cfg := range []LangConfig{javaLangConfig, csharpLangConfig} {
			classBody, ok := wrapperNamed(cfg, "decl")
			require.Truef(t, ok, "%s no longer registers a wrapper named decl", cfg.Lang)
			require.Equalf(t, "class __MetaWrapper__ {\n", classBody.Prefix,
				"%s wrapper decl is no longer the class-body wrapper this assertion is about", cfg.Lang)
			require.Equalf(t, contextMember, classBody.Context,
				"%s class-body wrapper must carry the member context, not %q", cfg.Lang, classBody.Context)

			top, ok := wrapperNamed(cfg, "top")
			require.Truef(t, ok, "%s no longer registers a wrapper named top", cfg.Lang)
			require.Emptyf(t, top.Prefix,
				"%s wrapper top is no longer the empty-prefix wrapper this assertion is about", cfg.Lang)
			require.Equalf(t, contextDecl, top.Context,
				"%s empty-prefix wrapper is the declaration context, not %q", cfg.Lang, top.Context)
		}
	})
}

// TestRegisterLangConfigRejectsUnlabeledWrapper proves the init-time
// validation actually fires. A guard that has never been observed to reject
// anything is indistinguishable from one that was never wired, so the
// rejecting cases run beside a well-labeled control that must NOT panic.
func TestRegisterLangConfigRejectsUnlabeledWrapper(t *testing.T) {
	const probeLang = treesitter.Language("ast-context-validation-probe")
	// A regression here would REGISTER the probe rather than panic; drop it
	// so a failure in this test cannot leak into the registry-wide ones.
	t.Cleanup(func() {
		langRegistryMu.Lock()
		defer langRegistryMu.Unlock()
		delete(langRegistry, probeLang)
	})

	cfgWith := func(w ContextWrapper) LangConfig {
		return LangConfig{Lang: probeLang, Reserved: "__META_AST_", Wrappers: []ContextWrapper{w}}
	}

	require.Panics(t, func() {
		registerLangConfig(cfgWith(ContextWrapper{Name: "unlabeled", Prefix: "", Suffix: "\n"}))
	}, "a wrapper with an empty Context must not reach the registry")

	require.Panics(t, func() {
		registerLangConfig(cfgWith(ContextWrapper{Name: "bogus", Context: "statement", Prefix: "", Suffix: "\n"}))
	}, "a wrapper whose Context is outside the four constants must not reach the registry")

	// The known-positive control: the same shape with a valid Context is
	// accepted, so the two rejections above measure the label and not some
	// unrelated property of the probe config.
	require.NotPanics(t, func() {
		registerLangConfig(cfgWith(ContextWrapper{Name: "decl", Context: contextDecl, Prefix: "", Suffix: "\n"}))
	}, "a correctly labeled wrapper must register cleanly")
}

// TestWildcardCompilesInEveryRegisteredLanguage asserts the tool's own
// recommended diagnostic compiles everywhere.
//
// `$_` is what the no-match hint tells a caller to fall back to, so a grammar
// where it does not compile hands that caller a parse failure at exactly the
// moment they are trying to find out why they got a zero. Six of the twenty-one
// registered languages were in that state — java, csharp, c, cpp and php had no
// expression wrapper at all, and elm's shared reserved prefix was not a legal
// identifier in it.
//
// THE TABLE IS THE LIVE REGISTRY, NEVER A HARDCODED LIST, and the floor is what
// keeps that honest: exhaustive-over-the-registry is trivially true over a
// registry that shrank. This is also the inexpressibility branch in its
// executable form — a future registration that genuinely cannot express `$_`
// fails the build here rather than quietly rejoining the old behavior, so no
// hardcoded list of "languages that cannot express the wildcard" is kept. Such
// a list has no members today, and one the next registration would silently
// fall outside of is worse than none.
func TestWildcardCompilesInEveryRegisteredLanguage(t *testing.T) {
	configs := registeredConfigs(t)
	require.GreaterOrEqual(t, len(configs), registeredLangFloor,
		"the registry holds %d languages, below the floor of %d — an exhaustive assertion over a shrunken registry passes by measuring less",
		len(configs), registeredLangFloor)

	for _, entry := range configs {
		t.Run(string(entry.lang), func(t *testing.T) {
			cp, err := Compile(mustParse(t, "$_"), entry.lang, "")
			require.NoErrorf(t, err,
				"the wildcard diagnostic does not compile in %s, so a caller sent here by a no-match hint gets a parse failure instead of an answer",
				entry.lang)
			defer cp.Close()
			require.NotEmptyf(t, cp.Variants,
				"%s compiled the wildcard to no candidate at all", entry.lang)
		})
	}
}

// memberWrapperCase is one grammar's member registration: a CLASS-ONLY pattern
// spelling, the root kind the member wrapper compiles it to, the container kind
// that root must never be, and a fixture carrying members the pattern must find
// beside one it must not.
//
// wantMatches is the whole match count and wantMembers the subset stamped with
// the member root. They differ only for C++, whose gap is a wrong root rather
// than an absent parse: `$T $N;` already compiled under the declaration wrapper
// to a `declaration`, so the union keeps finding the file-scope declaration and
// ADDS the two class fields no pattern could reach before. Splitting the two
// counts is what tells the added reach from the preserved reach.
type memberWrapperCase struct {
	lang        treesitter.Language
	pattern     string
	wantRoot    string
	container   string
	file        string
	source      string
	wantMatches int
	wantMembers int
}

// tsMemberFixture is shared by the typescript and tsx rows and by the identity
// replace. Two members match and one does not: `public label` differs from the
// pattern in anonymous tokens the matcher compares, so a count of 2 rather than
// 3 is what proves the pattern still discriminates.
const tsMemberFixture = `class Widget {
  private readonly name: string;
  private readonly size: number;
  public label: string;
}
`

func memberWrapperCases() []memberWrapperCase {
	return []memberWrapperCase{
		{
			lang:      treesitter.LangTypeScript,
			pattern:   "private readonly $N: $T;",
			wantRoot:  "public_field_definition",
			container: "class_body",
			file:      "widget.ts", source: tsMemberFixture,
			wantMatches: 2, wantMembers: 2,
		},
		{
			lang:      treesitter.LangTSX,
			pattern:   "private readonly $N: $T;",
			wantRoot:  "public_field_definition",
			container: "class_body",
			file:      "widget.tsx", source: tsMemberFixture,
			wantMatches: 2, wantMembers: 2,
		},
		{
			// JS has no `private readonly` and no method signatures, so its
			// class-only spelling is a static field.
			lang:      treesitter.LangJavaScript,
			pattern:   "static $N = $V;",
			wantRoot:  "field_definition",
			container: "class_body",
			file:      "widget.js",
			source: `class Widget {
  static defaults = {};
  static limit = 10;
  value = 1;
}
`,
			wantMatches: 2, wantMembers: 2,
		},
		{
			// The types are spelled as identifiers rather than `int`/`double`
			// so the row measures the member wrapper and not whether a
			// placeholder binds across node kinds, which is a different test's
			// subject.
			lang:      treesitter.LangCPP,
			pattern:   "$T $N;",
			wantRoot:  "field_declaration",
			container: "field_declaration_list",
			file:      "widget.cpp",
			source: `class Widget {
  Size width;
  Color tint;
};

Size globalWidth;
`,
			wantMatches: 3, wantMembers: 2,
		},
	}
}

// memberVariant returns the compiled candidate whose context set names member.
func memberVariant(t *testing.T, cp *CompiledPattern) CompiledVariant {
	t.Helper()
	for _, v := range cp.Describe() {
		if slices.Contains(v.Contexts, contextMember) {
			return v
		}
	}
	t.Fatalf("no candidate compiled in the member context; the union produced %+v", cp.Describe())
	return CompiledVariant{}
}

// TestMemberWrapper_TSFamily covers the four grammars that gain a class-body
// wrapper, in the three ways the registration can be half-landed.
//
// The compile leg is not redundant with the match leg. A member wrapper that
// rooted at the class_body rather than at the member would still MATCH class
// bodies — it would just match the wrong construct, and only the root kind
// shows it.
//
// The identity-replace leg is the write-side catcher, and it is the ONLY test
// in the suite that fails when the compiler's absorbed spans are not seeded
// onto the match. tree-sitter-typescript keeps a member's `;` in the class_body
// list, so the compiled root ends one token short of what the caller wrote; an
// identity template still carrying that `;` would splice it beside a source
// that already has one and emit `;;`.
func TestMemberWrapper_TSFamily(t *testing.T) {
	t.Run("member_root_is_not_class_body", func(t *testing.T) {
		for _, tc := range memberWrapperCases() {
			t.Run(string(tc.lang), func(t *testing.T) {
				cp, err := Compile(mustParse(t, tc.pattern), tc.lang, "")
				require.NoErrorf(t, err, "%s must compile a class-member pattern once a class body is registered", tc.lang)
				defer cp.Close()

				v := memberVariant(t, cp)
				require.Equalf(t, tc.wantRoot, v.RootKind,
					"%s member pattern must root at the member itself", tc.lang)
				require.NotEqualf(t, tc.container, v.RootKind,
					"%s rooted at the container that OWNS the member, which matches class bodies rather than members", tc.lang)
			})
		}
	})

	t.Run("member_matches_real_members", func(t *testing.T) {
		for _, tc := range memberWrapperCases() {
			t.Run(string(tc.lang), func(t *testing.T) {
				dir := fixtureRepo(t, map[string]string{tc.file: tc.source})
				cp, err := Compile(mustParse(t, tc.pattern), tc.lang, "")
				require.NoError(t, err)
				defer cp.Close()

				matches, stats, err := Match(context.Background(), dir, tc.lang, cp, nil, Scope{})
				require.NoError(t, err)
				require.Equal(t, 1, stats.FilesScanned)
				require.Lenf(t, matches, tc.wantMatches,
					"%s: the fixture carries a member the pattern must NOT match, so this count is the discriminator", tc.lang)

				members := 0
				for _, m := range matches {
					if m.CompiledKind == tc.wantRoot {
						members++
					}
				}
				require.Equalf(t, tc.wantMembers, members,
					"%s: matches stamped with the member root are the ones no pattern could reach before", tc.lang)
			})
		}
	})

	t.Run("member_identity_replace_is_noop", func(t *testing.T) {
		dir := fixtureRepo(t, map[string]string{"widget.ts": tsMemberFixture})
		const pattern = "private readonly $N: $T;"
		res, matches := runSplice(t, dir, treesitter.LangTypeScript, pattern, pattern, true)

		require.Equal(t, 2, matches,
			"the known-positive control: an identity replace over zero matches produces zero diffs and proves nothing")
		require.Empty(t, res.RefusedFiles)
		require.Empty(t, res.RejectedFiles, "an identity rewrite must survive the re-parse gate")
		for path, diff := range res.Diffs {
			require.Emptyf(t, diff,
				"identity template rewrote %s — the absorbed `;` reached the output instead of being spent against the template:\n%s",
				path, diff)
		}
	})
}

// wrapperNamed returns cfg's wrapper with the given Name.
func wrapperNamed(cfg LangConfig, name string) (ContextWrapper, bool) {
	for _, w := range cfg.Wrappers {
		if w.Name == name {
			return w, true
		}
	}
	return ContextWrapper{}, false
}
