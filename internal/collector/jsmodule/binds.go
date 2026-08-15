// SPDX-License-Identifier: Apache-2.0

package jsmodule

import (
	"log/slog"
	"strings"
	"sync"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// The ECMAScript BindsResolver arm. ONE arm serves all three languages: they
// share a module system, and the only per-language difference — the extension
// preference order — is derived by Resolve from the IMPORTER's own path rather
// than from a language argument.
//
// Registration happens here, at process start, so it is always in place before
// any collect chunks a file. That ordering is load-bearing: the chunker
// allocates a file's Binds map only when an arm is registered AT CHUNK TIME, so
// an arm installed later would find nil maps and fill nothing.
func init() {
	treesitter.RegisterBindsResolver(treesitter.LangTypeScript, bindsArm)
	treesitter.RegisterBindsResolver(treesitter.LangTSX, bindsArm)
	treesitter.RegisterBindsResolver(treesitter.LangJavaScript, bindsArm)
}

// ArmedLanguages returns the languages this package installs a BindsResolver
// arm for.
//
// It exists so the package that needs the init above to run can import this
// package ORDINARILY rather than blankly. A blank import is invisible to a
// reader asking why TypeScript references resolve exactly, and a tidy-imports
// pass can drop one without a compile error — which would silently disarm the
// whole feature in production while every test in this package still passed.
func ArmedLanguages() []treesitter.Language {
	return []treesitter.Language{
		treesitter.LangTypeScript,
		treesitter.LangTSX,
		treesitter.LangJavaScript,
	}
}

// armState is the per-collect state the arm needs: one Resolver, built once.
type armState struct {
	resolver *Resolver
}

// resolverCache holds one armState per in-flight collect, KEYED BY THE
// RepoContext POINTER.
//
// bindsArm is called once per file — 641 times on the acceptance corpus — and a
// naive implementation would re-read every tsconfig and rebuild the file set on
// every one of those calls. The RepoContext pointer is one value per collect by
// construction, so this map holds exactly one entry per in-flight collect and
// two concurrent collects of different repositories cannot see each other's
// file sets.
//
// THIS IS THE ONE PLACE PER-COLLECT CACHING LIVES. The rest of this package
// stays a pure unit with no process state, which is what lets its tests build
// resolvers freely.
var resolverCache sync.Map

// bindsArm maps one file's imports onto the binds they establish.
//
// IT RETURNS A VALUE AND TOUCHES NO RefSite. The parser's fillBinds is what
// writes, and it fills the chunker-allocated map IN PLACE because a parented
// reference site is a by-value copy taken during chunking. An arm that reached
// into self.Ref.Binds directly would reintroduce that aliasing defect from the
// other side.
//
// IT REPORTS NO DOT SCOPES. BindsResult carries them for the wildcard-style
// import that folds a whole scope into the importing file's unqualified
// namespace; ECMAScript has no such form — `import * as ns` binds the MODULE
// under one name, which is an ordinary per-name bind and is recorded as one.
//
// A TYPE-ONLY IMPORT BINDS EXACTLY LIKE A VALUE IMPORT. TypeScript type
// references are captured as bare type identifiers and reach the unqualified
// import rule the same way a call does, so filtering type-only bindings out
// would drop precisely the references the largest TypeScript edge class is made
// of. ImportBinding.TypeOnly is recorded for a future consumer and is not read
// here.
func bindsArm(
	rc *treesitter.RepoContext, byPath map[string]*treesitter.Result, self *treesitter.Result,
) treesitter.BindsResult {
	if rc == nil || self == nil {
		return treesitter.BindsResult{}
	}
	ctx, ok := fileContext(self)
	if !ok || len(ctx.ImportBindings) == 0 {
		return treesitter.BindsResult{}
	}
	state := armStateFor(rc, byPath)
	if state.resolver == nil {
		return treesitter.BindsResult{}
	}

	binds := make(map[string]treesitter.Bind, len(ctx.ImportBindings))
	for _, b := range ctx.ImportBindings {
		if b.Local == "" {
			// No local name, so there is no key to record under. Reached by a
			// side-effect import, which Resolve refuses for the same reason.
			continue
		}
		target, outcome := state.resolver.Resolve(self.FilePath, b.Specifier, b.Imported, b.Kind)
		switch outcome {
		case OutcomeRefused:
			// Binds no local name at all: nothing to record.
			continue
		case OutcomeOutOfRepo:
			// AN EMPTY SCOPE, DELIBERATELY, AND NEVER A FABRICATED ONE. There
			// is no in-repo path to name, and a synthetic "file:react" could
			// collide with a real repository-root file named react — turning an
			// inert bind into a wrong one. An empty scope is definitively
			// absent from the declaration index, so the qualified rule
			// terminates and the unqualified one falls through.
			binds[b.Local] = treesitter.Bind{Name: b.Imported}
		case OutcomeBound, OutcomeNoNamedDecls, OutcomeUndiscovered:
			// ONE ARM FOR ALL THREE, WITH NO SPECIAL CASE. A file that declares
			// nothing, and a candidate path nothing was discovered at, are
			// recorded exactly like a resolved declaration: the scope simply
			// never entered the index's scope set, and the index is what
			// answers externality. A branch here would mean the ruling has been
			// misread.
			binds[b.Local] = treesitter.Bind{
				Scope: treesitter.ScopeID(target.File, treesitter.DetectLanguage(target.File), ""),
				Name:  target.Name,
			}
		}
	}
	return treesitter.BindsResult{Binds: binds}
}

// armStateFor returns the collect's Resolver, building it on first use.
func armStateFor(rc *treesitter.RepoContext, byPath map[string]*treesitter.Result) *armState {
	if v, ok := resolverCache.Load(rc); ok {
		if state, ok := v.(*armState); ok {
			return state
		}
	}
	built := buildArmState(rc, byPath)
	actual, _ := resolverCache.LoadOrStore(rc, built)
	if state, ok := actual.(*armState); ok {
		return state
	}
	return built
}

// buildArmState derives the exports table from every file's chunks and builds
// one Resolver over it.
//
// A resolver that cannot be built leaves an armState with no resolver, so the
// arm records nothing and resolution falls back to the name-based rungs. A
// malformed repository must never fail a collect.
func buildArmState(rc *treesitter.RepoContext, byPath map[string]*treesitter.Result) *armState {
	exports := make(map[string]FileExports, len(byPath))
	for path, result := range byPath {
		exports[path] = fileExportsOf(result)
	}
	resolver, err := NewResolver(rc.Root, rc.Files, exports)
	if err != nil {
		slog.Warn("jsmodule: no module resolver for this collect", "root", rc.Root, "err", err)
		return &armState{}
	}
	return &armState{resolver: resolver}
}

// fileExportsOf reads what one file offers importers out of its chunk results.
//
// DECLARED HOLDS NON-EMPTY CHUNK NAMES WITH THE COLLISION SUFFIX STRIPPED. Both
// details serve NAME RESOLUTION — they make a lookup for an imported name ask
// the same question the declaration index answers — and neither decides
// externality, which the index's own scope set answers.
func fileExportsOf(result *treesitter.Result) FileExports {
	fe := FileExports{Declared: map[string]bool{}}
	if result == nil {
		return fe
	}
	for _, chunk := range result.Chunks {
		if chunk.Name == "" {
			continue
		}
		fe.Declared[baseDeclName(chunk.Name)] = true
	}
	if ctx, ok := fileContext(result); ok {
		fe.ReExports = ctx.ReExports
		fe.DefaultName = ctx.DefaultExportName
	}
	return fe
}

// fileContext returns a file's chunk context, which is where the chunker
// attaches its file-level import facts. Every chunk of one file carries the
// same context, so the first is representative; a file that produced no chunk
// at all has no context to read and also emits no reference edge to bind.
func fileContext(result *treesitter.Result) (treesitter.ChunkContext, bool) {
	if result == nil || len(result.Chunks) == 0 {
		return treesitter.ChunkContext{}, false
	}
	return result.Chunks[0].Context, true
}

// baseDeclName strips the "#<astPathHash>" suffix a declaration takes when its
// name collides inside its own file.
//
// It mirrors the parser-side helper of the same name rather than importing it:
// the parser imports THIS package to install the arms, so a dependency the
// other way would be a cycle. The rule it encodes is the declaration index's,
// not this package's — a reference writes Thing and never Thing#a1b2c3d4.
func baseDeclName(name string) string {
	base, _, _ := strings.Cut(name, "#")
	return base
}
