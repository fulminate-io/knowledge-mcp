// SPDX-License-Identifier: Apache-2.0

package codesync

import (
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// This file holds the KEYING half of the precise call graph: the VTA seed, the
// repo relativizer, and the decl-key derivation. The LOADING half — the
// GoCallGraph result type and the per-module BuildGoCallGraph loop — lives in
// callgraph_go_modules.go.

// repoDeclaredFunctions returns every SSA function DECLARED inside the repo.
// That set is what VTA is seeded with, and it defines both which functions are
// analyzed and which are keyable — one rule, so the two cannot drift.
//
// ssautil.AllFunctions reaches functions from the members AND METHOD SETS of
// every package. That is why it replaces a package-members seed: methods are
// not package members, so a members-only seed never walked a single method body
// and no method ever contributed an outgoing edge. AllFunctions also reaches
// dependency and stdlib functions; the relativizer filter excludes them.
//
// declaringFunction is used in its ok form, not compared against nil: a
// synthetic wrapper has no declaration, and reading a field off it panics —
// this and formatCallGraphName are its only two call sites and they must agree
// about that contract.
//
// The INSTANCE is added to the set, not the origin: analyzing the instantiated
// body is what produces its call edges.
func repoDeclaredFunctions(prog *ssa.Program, rel repoRelativizer) map[*ssa.Function]bool {
	funcSet := make(map[*ssa.Function]bool)
	for fn := range ssautil.AllFunctions(prog) {
		decl, ok := declaringFunction(fn)
		if !ok || fn.Prog == nil {
			continue
		}
		pos := fn.Prog.Fset.Position(decl.Pos())
		if !pos.IsValid() {
			continue
		}
		if _, inRepo := rel.rel(pos.Filename); !inRepo {
			continue
		}
		funcSet[fn] = true
	}
	return funcSet
}

// repoRelativizer turns an absolute source-position filename into a
// repo-relative path, and reports whether the file lies inside the repo at all.
//
// It holds the repo root in up to two spellings, and the RAW root is tried
// first because that is the one that normally matches: go/packages reports
// position filenames carrying exactly the prefix handed to packages.Config.Dir.
// On a machine where /tmp is a symlink to /private/tmp, a root of /tmp/x yields
// positions under /tmp/x, so resolving symlinks as the PRIMARY spelling would
// relativize every file to "../../.." and lose every lookup. The resolved
// spelling is kept only as a fallback for the inverse case.
type repoRelativizer struct {
	roots []string
}

// newRepoRelativizer builds a relativizer for rootDir.
func newRepoRelativizer(rootDir string) repoRelativizer {
	roots := []string{filepath.Clean(rootDir)}
	if resolved, err := filepath.EvalSymlinks(roots[0]); err == nil && resolved != roots[0] {
		roots = append(roots, resolved)
	}
	return repoRelativizer{roots: roots}
}

// rel returns the slash-separated repo-relative form of abs and true, or
// ("", false) when abs lies outside the repo.
//
// An out-of-repo position is REJECTED rather than passed through: an
// absolute-path key can never match a collector node ID, so it would bind
// nothing while still looking like a key.
func (r repoRelativizer) rel(abs string) (string, bool) {
	for _, root := range r.roots {
		rp, err := filepath.Rel(root, abs)
		if err != nil {
			continue
		}
		if filepath.IsAbs(rp) || rp == ".." || strings.HasPrefix(rp, ".."+string(filepath.Separator)) {
			continue
		}
		return filepath.ToSlash(rp), true
	}
	return "", false
}

// declaringFunction returns the SOURCE DECLARATION fn was built from and true,
// or (nil, false) when fn has no source declaration at all. A synthetic wrapper
// (method wrapper, bound-method wrapper, thunk) has Pkg == nil AND Origin() == nil
// and is NOT a declaration; callers must check ok BEFORE reading any field.
//
// The two-value form is the contract, not a style choice. A bare-pointer
// version invites the call-site shape "resolve, then guard fn.Pkg == nil",
// which dereferences the very nil it was meant to guard. A bound method value
// (f := s.Handle) produces exactly such a wrapper, the wrapper reaches the call
// graph, and augmentWithPreciseCallGraph has no recover — so that shape takes
// down the whole collect.
func declaringFunction(fn *ssa.Function) (*ssa.Function, bool) {
	if fn == nil {
		return nil, false
	}
	if fn.Pkg != nil {
		return fn, true
	}
	// A generic instantiation carries a nil Pkg and a Name() with the type
	// arguments appended ("Get[time.Duration]"); its origin is the single
	// declaration the collector actually indexed, so every instantiation of one
	// generic collapses onto that one node.
	if origin := fn.Origin(); origin != nil && origin.Pkg != nil {
		return origin, true
	}
	return nil, false
}

// extractCallEdges walks the VTA call graph and builds a caller→callees map
// keyed by decl key.
//
// A nil Pkg is deliberately NOT a skip condition here: generic instantiations
// carry one and resolve to their origin declaration. formatCallGraphName
// returning "" is the single gate on whether a function contributes a key.
func extractCallEdges(cg *callgraph.Graph, rel repoRelativizer) map[string][]string {
	result := make(map[string][]string)
	seen := make(map[string]map[string]bool)

	for fn, node := range cg.Nodes {
		callerKey := formatCallGraphName(fn, rel)
		if callerKey == "" {
			continue
		}
		for _, edge := range node.Out {
			calleeKey := formatCallGraphName(edge.Callee.Func, rel)
			if calleeKey == "" {
				continue
			}
			if _, ok := seen[callerKey]; !ok {
				seen[callerKey] = make(map[string]bool)
			}
			if !seen[callerKey][calleeKey] {
				seen[callerKey][calleeKey] = true
				result[callerKey] = append(result[callerKey], calleeKey)
			}
		}
	}
	return result
}

// formatCallGraphName returns the decl key for target: the repo-relative file
// that DECLARES it, a colon, then the symbol — "<file>:FuncName" for a function
// and "<file>:ReceiverType.MethodName" for a method.
//
// Returns "" for anything with no in-repo source declaration: synthetic
// wrappers, functions from dependencies, and positions outside the repo.
//
// Every field is read on the resolved declaration, after ok is checked — the
// nil contract declaringFunction documents.
func formatCallGraphName(target *ssa.Function, rel repoRelativizer) string {
	fn, ok := declaringFunction(target)
	if !ok || fn.Prog == nil {
		return ""
	}
	pos := fn.Prog.Fset.Position(fn.Pos())
	if !pos.IsValid() {
		return ""
	}
	file, ok := rel.rel(pos.Filename)
	if !ok {
		return ""
	}
	if recv := fn.Signature.Recv(); recv != nil {
		return file + ":" + receiverTypeName(recv.Type().String()) + "." + fn.Name()
	}
	return file + ":" + fn.Name()
}

// receiverTypeName reduces an SSA receiver type string to the bare type name.
//
// ORDER IS LOAD-BEARING: cut the type-argument list FIRST, then the package
// qualifier, then the pointer star. Cutting the qualifier first lets a
// QUALIFIED type argument swallow the result — a distManager receiver whose
// type argument is *.../bm25.CorpusStats derives to "CorpusStats", and a
// Box[time.Duration] receiver derives to "Duration".
//
// The general lesson, stated here because it is what this whole change is
// about: when a keying fix rewrites one half of a key, RE-DERIVE THE OTHER HALF
// TOO. This fix rewrote the file half of the decl key, and the symbol half had
// been inherited unexamined — wrong on both sides of the merge.
func receiverTypeName(typeName string) string {
	if idx := strings.Index(typeName, "["); idx >= 0 {
		typeName = typeName[:idx]
	}
	if idx := strings.LastIndex(typeName, "."); idx >= 0 {
		typeName = typeName[idx+1:]
	}
	return strings.TrimPrefix(typeName, "*")
}
