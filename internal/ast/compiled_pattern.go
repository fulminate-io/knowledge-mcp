// SPDX-License-Identifier: Apache-2.0

// compiled_pattern.go — the CompiledPattern lifecycle: the engine-internal
// compiled representation, its Compile constructor, per-variant root-query
// init, disclosure (Describe), and Close. Split out of match.go so that file
// stays under the file-size cap while keeping match.go focused on the walk
// executor (Match, WalkStats) and the result types.

package ast

import (
	"context"
	"fmt"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// CompiledPattern is the engine-internal pattern representation. It carries
// EVERY context in which the pattern is expressible — one compiledVariant per
// wrapper that hosts it, deduped on structure — because one pattern text can
// be grammatical in several of a language's parse contexts and compiling to
// just one of them means the construct the caller did not write.
//
// The struct is preserved (not renamed) so cmd/knowledge's `defer
// cp.Close()` pattern keeps building.
type CompiledPattern struct {
	// Variants are the compiled candidates in cfg.Wrappers order. The walk
	// runs them all against each file's single parse and stamps every match
	// with the variant that produced it.
	Variants []compiledVariant
	// Source retains the raw DSL source for explain/debug output and as the
	// re-compile source for Match's per-worker pattern compilation (each
	// worker re-Parses+Compiles Source so no *Tree is shared across
	// goroutines).
	Source string
	// pin is the context the caller narrowed the union to, carried so the
	// per-worker recompile reproduces the SAME variant set. A worker that
	// recompiled unpinned would silently widen the union behind the pin.
	pin string
	// Narrowed holds member-context variants the keyword-narrowing rule dropped
	// (engine_variants.go): each is a member reading whose leading token another
	// surviving variant reads as an anonymous keyword. They are NOT walked — they
	// exist only so DescribeNarrowed can disclose what was dropped and why, and
	// they carry no rootQuery. RELEASE OBLIGATION: each holds a tree-sitter Tree
	// that Close() must release; a Narrowed tree Close never frees is C memory no
	// Go-level gate can see leaking.
	Narrowed []patternVariant
}

// Describe renders the compiled candidate set for disclosure: what each variant
// compiled to, which contexts produced it, and which pattern text the hosting
// rules absorbed. It is the caller's view of a compile that would otherwise be
// invisible — including the compile behind a ZERO result, which is the case the
// disclosure exists for.
func (cp *CompiledPattern) Describe() []CompiledVariant {
	if cp == nil {
		return nil
	}
	out := make([]CompiledVariant, 0, len(cp.Variants))
	for i := range cp.Variants {
		v := &cp.Variants[i]
		cv := CompiledVariant{
			Contexts: v.Contexts,
			Wrappers: v.Wrappers,
			RootKind: v.RootKind,
		}
		for _, span := range v.Absorbed {
			if v.Tree == nil || int(span.End) > len(v.Tree.SubstitutedSource) {
				continue
			}
			cv.Absorbed = append(cv.Absorbed, v.Tree.SubstitutedSource[span.Start:span.End])
		}
		out = append(out, cv)
	}
	return out
}

// compiledVariant is one candidate plus the per-variant candidate-search
// state: the tree-sitter query that finds its root kind without a full AST
// walk, and whether reaching that root descended through same-span wrappers.
type compiledVariant struct {
	patternVariant
	// rootQuery is a pre-compiled tree-sitter query like
	// `(defer_statement) @root` that finds candidate nodes using the C
	// engine's internal per-type indexing — avoiding a full AST walk and
	// the ~1GB of cachedNode allocations that walk triggers. Nil when this
	// variant's pattern root is a placeholder (any type could match).
	rootQuery *sitter.Query
	// rootDescended is true when effectivePatternNode descended the root
	// through single-named-child wrappers. When true, the candidate
	// prefilter must apply effectiveTargetNode before comparing kinds.
	rootDescended bool
}

// Close releases every variant's query and pattern tree. Nil-safe.
func (cp *CompiledPattern) Close() {
	if cp == nil {
		return
	}
	for i := range cp.Variants {
		if cp.Variants[i].rootQuery != nil {
			cp.Variants[i].rootQuery.Close()
			cp.Variants[i].rootQuery = nil
		}
		cp.Variants[i].Close()
	}
	cp.Variants = nil
	// Narrowed variants are plain patternVariants (no rootQuery of their own —
	// rootQuery lives on the compiledVariant wrapper), so releasing each one's
	// leaked Tree is the single Close() call below.
	for i := range cp.Narrowed {
		cp.Narrowed[i].Close()
	}
	cp.Narrowed = nil
}

// Compile turns a parsed Pattern into a CompiledPattern by resolving the
// language config and running the union compile. Callers MUST defer
// cp.Close() on the returned value.
//
// pinContext narrows the union to the variants carrying that context; "" keeps
// every context the pattern is expressible in. The pin is retained on the
// result so Match's per-worker recompile reproduces the same candidate set.
func Compile(pat Pattern, lang treesitter.Language, pinContext string) (*CompiledPattern, error) {
	if isDeniedLanguage(lang) {
		return nil, errLanguageNotSupported(lang)
	}
	cfg, ok := langConfigFor(lang)
	if !ok {
		return nil, errLanguageNotSupported(lang)
	}
	variants, narrowed, err := compilePatternVariants(context.Background(), pat, cfg, pinContext)
	if err != nil {
		return nil, err
	}
	cp := &CompiledPattern{Source: pat.Source, pin: pinContext, Variants: make([]compiledVariant, 0, len(variants)), Narrowed: narrowed}
	for _, v := range variants {
		cv := compiledVariant{patternVariant: v}
		initRootQuery(&cv, lang)
		cp.Variants = append(cp.Variants, cv)
	}
	return cp, nil
}

// initRootQuery compiles the tree-sitter query used to find one variant's
// candidate nodes without a full AST walk. Skipped when that variant's pattern
// root is a placeholder (any node type could match), which the compile already
// recorded by leaving RootKind empty.
func initRootQuery(cv *compiledVariant, lang treesitter.Language) {
	if cv.RootKind == "" {
		return
	}
	_, cv.rootDescended = patternRootKind(cv.Tree)
	grammar, ok := treesitter.LanguageGrammar(lang)
	if !ok {
		return
	}
	sexpr := fmt.Sprintf("(%s) @root", cv.RootKind)
	if q, err := sitter.NewQuery([]byte(sexpr), grammar); err == nil {
		cv.rootQuery = q
	}
}
