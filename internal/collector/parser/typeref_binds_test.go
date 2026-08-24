// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"os"
	"path/filepath"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestF0TypeRefThroughBinds covers the index-aware type-text rung's five
// behaviors directly, without a consumer in the way.
//
// It proves what the helper DOES. It proves nothing about whether either
// production consumer calls it — that is what the two wiring catchers are for,
// one per consumer, because a catcher on one proves nothing about the other.
func TestF0TypeRefThroughBinds(t *testing.T) {
	const (
		local = "file:a/local.ts"
		other = "file:a/other.ts"
		app   = "file:a/app.ts"
	)

	t.Run("own_scope_wins", func(t *testing.T) {
		// THE ANTI-SHADOWING CASE. A bind of the same name exists and must NOT
		// override a declaration the reference's own scope really holds — the
		// rung can never change an answer that today names a declared type.
		ix := indexOf(t,
			&declRec{NodeID: "a/local.ts:Greeter", File: "a/local.ts", Scope: local, Name: "Greeter"},
			&declRec{NodeID: "a/other.ts:Greeter", File: "a/other.ts", Scope: other, Name: "Greeter"},
		)
		ref := &treesitter.RefSite{
			File: "a/local.ts", Scope: local, Lang: treesitter.LangTypeScript,
			Binds: map[string]treesitter.Bind{"Greeter": {Scope: other}},
		}
		require.Equal(t, typeRef{Scope: local, Name: "Greeter"},
			resolveTypeTextThroughIndex(ix, ref, "Greeter"),
			"a declaration in the reference's own scope must win over an import bind of the same name")
	})

	t.Run("bind_binds_cross_file", func(t *testing.T) {
		// THE KNOWN-POSITIVE. Without this subtest every other one here would
		// be satisfied by a helper that returns resolveTypeText's answer
		// unconditionally.
		ix := indexOf(t,
			&declRec{NodeID: "a/other.ts:Greeter", File: "a/other.ts", Scope: other, Name: "Greeter"},
		)
		ref := &treesitter.RefSite{
			File: "a/app.ts", Scope: app, Lang: treesitter.LangTypeScript,
			Binds: map[string]treesitter.Bind{
				"Greeter": {Scope: other},
				// An ALIASED import: the local spelling differs from the
				// declared name, and the bind's own name is what gets looked up.
				"Base": {Scope: other, Name: "Greeter"},
			},
		}
		require.Equal(t, typeRef{Scope: other, Name: "Greeter"},
			resolveTypeTextThroughIndex(ix, ref, "Greeter"),
			"an unqualified name the reference's own scope does not declare must bind through the import")
		require.Equal(t, typeRef{Scope: other, Name: "Greeter"},
			resolveTypeTextThroughIndex(ix, ref, "Base"),
			"an aliased bind must look up the bind's OWN name, not the local spelling")
	})

	t.Run("terminating_bind_declines", func(t *testing.T) {
		// The shape a language takes when its arm records a deliberately
		// terminating scope: the bind exists, its target declares nothing, and
		// today's answer must stand rather than be replaced by a scope that
		// holds no declaration.
		ix := indexOf(t,
			&declRec{NodeID: "m/a.swift:Helper", File: "m/a.swift", Scope: "file:m/a.swift", Name: "Helper"},
		)
		ref := &treesitter.RefSite{
			File: "m/b.swift", Scope: "file:m/b.swift", Lang: treesitter.LangSwift,
			Binds: map[string]treesitter.Bind{"Helper": {Scope: "file:"}},
		}
		require.Equal(t, typeRef{Scope: "file:m/b.swift", Name: "Helper"},
			resolveTypeTextThroughIndex(ix, ref, "Helper"),
			"a bind whose target declares nothing must fall through to today's answer, never answer with an empty scope")
	})

	t.Run("no_bind_declines", func(t *testing.T) {
		ix := indexOf(t,
			&declRec{NodeID: "a/other.ts:Greeter", File: "a/other.ts", Scope: other, Name: "Greeter"},
		)
		ref := &treesitter.RefSite{File: "a/app.ts", Scope: app, Lang: treesitter.LangTypeScript}
		require.Equal(t, typeRef{Scope: app, Name: "Greeter"},
			resolveTypeTextThroughIndex(ix, ref, "Greeter"),
			"with no bind at all the rung must return today's answer unchanged, not search the index by name")
	})

	t.Run("qualified_text_unchanged", func(t *testing.T) {
		// A QUALIFIED spelling is already a bind question, and the shared
		// resolver answered it. There is no second bind to consult, so the
		// answer must be byte-identical to the index-blind one.
		ix := indexOf(t,
			&declRec{NodeID: "a/other.ts:Greeter", File: "a/other.ts", Scope: other, Name: "Greeter"},
		)
		ref := &treesitter.RefSite{
			File: "a/app.ts", Scope: app, Lang: treesitter.LangTypeScript,
			Binds: map[string]treesitter.Bind{
				"mod":     {Scope: "file:a/missing.ts"},
				"Greeter": {Scope: other},
			},
		}
		want := resolveTypeText(ref, "mod.Greeter")
		require.Equal(t, want, resolveTypeTextThroughIndex(ix, ref, "mod.Greeter"),
			"a qualified spelling must not be re-bound through the unqualified rung")
		require.Equal(t, typeRef{Scope: "file:a/missing.ts", Name: "Greeter"}, want,
			"control: the qualified answer is the one the shared resolver produced, scope and all")
	})
}

// TestF0CrossFileR2TThroughBinds is the TYPED-QUALIFIER consumer's wiring
// catcher: it fails if the helper is declared but typedQualifierTarget still
// calls the index-blind resolver.
//
// It installs a FAKE qualifier-types arm for TypeScript over the production
// one, so THE CLEANUP RESTORES THE PRODUCTION REGISTRATION rather than deleting
// the entry. UnregisterQualifierTypes deletes rather than parks, so a cleanup
// that merely unregistered would leave TypeScript unarmed for every later test
// in the same binary — and the symptom would not be a missing arm but TypeScript
// method calls quietly resolving through a lower rung in tests that happen to
// run afterwards.
func TestF0CrossFileR2TThroughBinds(t *testing.T) {
	treesitter.RegisterQualifierTypes(treesitter.LangTypeScript,
		func(_ *sitter.Node, _ []byte) map[string]treesitter.QualType {
			// Every TypeScript declaration in this fixture reports the same one
			// qualifier: `g` is a Greeter. The arm is a stand-in for a real
			// per-language walk, and its only job is to put an IMPORTED type
			// spelling in front of the rung.
			return map[string]treesitter.QualType{"g": {Text: "Greeter"}}
		})
	t.Cleanup(treesitter.RegisterECMAQualifierTypes)

	// THE FIXTURE IS MATERIALIZED ON DISK AND A REAL RepoContext IS PASSED,
	// because the ECMAScript module resolver given no root and no discovered
	// file set resolves nothing: with an empty RepoContext no bind is
	// established at all and this catcher would go red against CORRECT wiring,
	// for a reason that has nothing to do with the rung under test.
	files := []fixtureFile{
		{path: "web/greeter.ts", src: "export class Greeter {\n  greet(): number { return 1; }\n}\n"},
		{path: "web/app.ts", src: "import { Greeter } from './greeter';\n\n" +
			"export function run(g: Greeter): number {\n  return g.greet();\n}\n"},
	}
	root := t.TempDir()
	discovered := make([]string, 0, len(files))
	for _, f := range files {
		full := filepath.Join(root, filepath.FromSlash(f.path))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o750))
		require.NoError(t, os.WriteFile(full, []byte(f.src), 0o600))
		discovered = append(discovered, f.path)
	}
	res := chunkResultsToPopulate("testrepo",
		&treesitter.RepoContext{Root: root, Files: discovered}, chunkFixture(t, files))

	const (
		caller = "web/app.ts:run"
		target = "web/greeter.ts:Greeter.greet"
	)
	var found bool
	for _, e := range res.Edges {
		if e.Type == string(kgtypes.EdgeCalls) && e.FromId == caller && e.ToId == target {
			found = true
			break
		}
	}
	require.Truef(t, found,
		"no CALLS edge %s -> %s: the qualifier is typed to an IMPORTED class, so the typed-qualifier rung resolves it only through the index-aware type-text helper — this is the catcher for that consumer still calling the index-blind resolver",
		caller, target)
}
