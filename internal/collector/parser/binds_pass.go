// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"log/slog"
	"maps"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/jsmodule"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// armedLanguages is WHY this package imports jsmodule: that package's init
// installs the ECMAScript BindsResolver arms, and importing it is what runs the
// init. The import is an ordinary used one rather than a blank one on purpose —
// a blank import is invisible to a reader asking why TypeScript resolves
// exactly, and a tidy-imports pass can drop one with no compile error, which
// would disarm the feature in production while every test still passed.
var armedLanguages = jsmodule.ArmedLanguages()

// fillBinds runs each file's registered BindsResolver arm and fills what it
// returns — the per-name binds and the dot scopes — into that file's reference
// site. It returns the number of dot scopes it filled across every file.
//
// THE COUNT IS RETURNED RATHER THAN ONLY LOGGED so a test can drive it
// non-zero. On a corpus with no dot imports the census is structurally zero,
// which makes a wired zero and a hardcoded one indistinguishable in production
// for the whole life of a release; TestFillBinds_DotScopesOnly reads this
// return on a run that fills exactly one, which is what keeps the zero honest.
//
// WHY THE PARSER AND NOT THE CHUNKER. ChunkFile takes a repo-relative path and
// holds no repo handle: the Chunker struct carries a parser, a config and a
// compiled query cache, and nothing else. An arm needs the repo root, the
// discovered file set, and every other file's captured declarations — a
// relative specifier usually carries no extension, so resolving one needs a
// file-existence oracle the chunker cannot offer. So construction happens here,
// after every file has been chunked, while the declarations stay in the
// treesitter package where the seam is declared.
//
// IT FILLS IN PLACE AND NEVER ASSIGNS. The chunker allocated the map when the
// language has an arm, and a parented reference site is a BY-VALUE copy of the
// file-level one taken during chunking. Assigning a fresh map here would update
// the file-level site alone, leaving every reference emitted from inside a
// class or struct reading the header it copied — which would kill the two
// import rules for exactly the parented references that need them, with no
// compile error and no failing gate.
//
// WHERE IT RUNS: strictly after DeduplicateChunks and strictly before the
// declaration index is built. Results are already sorted by FilePath by then,
// so the pass iterates in that order and never ranges byPath to build anything
// ordered.
func fillBinds(rc *treesitter.RepoContext, results []*treesitter.Result) int {
	slog.Debug("collector: binds pass",
		"files", len(results), "armed_languages", len(armedLanguages))

	byPath := make(map[string]*treesitter.Result, len(results))
	for _, r := range results {
		byPath[r.FilePath] = r
	}

	dotScopesFilled := 0
	for _, r := range results {
		built := treesitter.BindsFor(rc, byPath, r)
		// EMPTINESS IS A PROPERTY OF THE WHOLE RESULT, NOT OF Binds ALONE. A
		// Go dot import establishes NO per-name bind — it folds a whole scope
		// in — so a file whose only import is a dot import returns an empty
		// Binds map. Keying this skip on Binds alone would drop that file
		// before its dot scope was ever read, with no compile error and no
		// failing gate. TestFillBinds_DotScopesOnly is the catcher.
		if len(built.Binds) == 0 && len(built.DotScopes) == 0 {
			continue
		}
		if r.Ref == nil {
			// An arm registered AFTER chunking has nothing to fill: the file
			// was chunked with no site at all. Skipping is correct and silent
			// by design — tests that exercise an arm register it before
			// chunking or construct their reference site directly.
			continue
		}
		// THE SITE CHECK AND THE PER-MAP CHECKS ARE SEPARATE. One map standing
		// in for the whole site is not a live defect while both are allocated
		// under the same condition, but if the two allocations ever drift
		// apart it would silently skip a dot-scopes-only file again.
		if len(built.Binds) > 0 && r.Ref.Binds != nil {
			// FILLED IN PLACE into the map the chunker already allocated.
			// Never `r.Ref.Binds = built.Binds`: see above. maps.Copy IS the
			// in-place fill — the equivalent
			// `for k, v := range built.Binds { r.Ref.Binds[k] = v }` is
			// rejected by the modernize linter's mapsloop check, so this
			// spelling is the enforced one rather than a preference.
			maps.Copy(r.Ref.Binds, built.Binds)
		}
		for _, s := range built.DotScopes {
			// IN PLACE for the same reason, by subscript rather than by copy:
			// the arm produces a slice and the site carries a set.
			if r.Ref.DotScopes != nil {
				r.Ref.DotScopes[s] = true
				dotScopesFilled++
			}
		}
	}

	// THE CONSTRUCT CENSUS, EMITTED EVEN WHEN ZERO. A census that only appears
	// when non-zero cannot distinguish "none" from "the counter was never
	// wired", and zero is the value a reader will actually see: no Go arm ships
	// yet, so for the only language dot imports exist in the number is
	// structurally zero.
	//
	// THE KEY IS dot_scopes_filled AND NOT dot_imports_seen. The pass cannot
	// see imports; it sees what an ARM REPORTED. A key promising "imports seen"
	// would read as "this corpus has no dot imports" when what it means is "no
	// arm reported any", and those are different facts.
	slog.Info("collector: dot-scope census", "dot_scopes_filled", dotScopesFilled)

	return dotScopesFilled
}
