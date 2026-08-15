// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"path"
	"strings"
)

// The Go BindsResolver arm, registered at init so it is live at binary load —
// BEFORE anything chunks. That ordering is a property of the design rather
// than a convenience: the chunker allocates a file's Binds and DotScopes maps
// only where a language HAS an arm, so an arm registered after chunking finds
// nil maps and the pass has nothing to fill in place.
func init() {
	RegisterGoBindsResolver()
}

// RegisterGoBindsResolver installs the Go arm. It is EXPORTED so a test that
// swaps in a fake Go arm can RESTORE the real one on cleanup instead of
// unregistering it.
//
// THE DISTINCTION IS NOT COSMETIC. UnregisterBindsResolver DELETES the registry
// entry, so a cleanup that unregisters Go removes the real arm for every later
// test in the same binary — and the symptom is not a missing arm, it is
// cross-package Go references quietly resolving as external in tests that run
// afterwards. Any test needing Go to resolve without this arm swaps in its own
// fake and restores through here.
func RegisterGoBindsResolver() {
	RegisterBindsResolver(LangGo, goBindsResolver)
}

// goBindsResolver maps a Go file's imports onto the scopes they name.
//
// GO'S WHOLE MAPPING IS ONE PREFIX STRIP, because a Go import path under the
// module IS the repo-relative directory of the package it names, and Go's
// resolution unit is the DIRECTORY (scopeKinds[LangGo] = ScopeDir). There is no
// module resolver to build, no extension preference, no index to consult.
//
// THE ARM MAKES NO IN-REPO / OUT-OF-REPO JUDGMENT AND NO BRANCH EXPRESSES ONE.
// strings.TrimPrefix is a no-op when the prefix is absent, so an import outside
// the module keeps its path verbatim and yields a scope the declaration index
// has never heard of — which is exactly the answer the external-qualifier rule
// needs, without the arm asking the question. The index then decides uniformly:
// an under-module path the index holds binds, and one it does not — gitignored
// codegen, a package of only vars, a stdlib path, a third-party path — reaches
// the same termination. The arm cannot distinguish the last two and does not
// need to.
//
// It READS rc.ModulePath and the file's own captured import table, and reads
// neither byPath, rc.Root nor rc.Files. It RETURNS a value and never touches a
// reference site.
func goBindsResolver(rc *RepoContext, _ map[string]*Result, self *Result) BindsResult {
	if rc == nil || self == nil {
		return BindsResult{}
	}
	// RULE B6 — no module path, no honest answer. With an empty module path the
	// prefix would be a bare "/", the strip a no-op on every import, and every
	// scope recorded a full import path the index has never heard of, so every
	// qualified Go reference in the repo would terminate as external. The ZERO
	// result is the truthful output: resolution proceeds exactly as it does for
	// a language with no arm registered, and BindsFor returns this same value
	// in that case, so the two states are byte-identical downstream.
	if rc.ModulePath == "" {
		return BindsResult{}
	}
	if len(self.Chunks) == 0 {
		// No chunk means no file context to read — and a file that produced no
		// chunk emits no reference edge to bind either.
		return BindsResult{}
	}
	bindings := self.Chunks[0].Context.ImportBindings
	if len(bindings) == 0 {
		return BindsResult{}
	}

	binds := make(map[string]Bind, len(bindings))
	var dots []string
	for _, b := range bindings {
		switch b.Local {
		case "_":
			// RULE B1 — a blank import introduces no name, so no reference can
			// name it: bind nothing and report no dot scope. COMPLETE, not a
			// limitation. Checked BEFORE any default key is derived, because
			// "_" is a valid Local value and a naive "Local is non-empty" test
			// would key a qualifier no reference can ever write.
			continue
		case ".":
			// RULE B2 — a dot import introduces no QUALIFIER, so there is no
			// Binds key to hold: it folds a whole scope into the file's
			// unqualified namespace, which is what a dot scope means. The
			// derivation is SHARED with the qualified case below rather than
			// duplicated, so the scope string a dot import reports and the one
			// a plain import binds are stamped by one expression.
			dots = append(dots, goImportScope(rc.ModulePath, b.Specifier))
		default:
			// RULE B3 — the key is the local name when the import declares one,
			// otherwise the last segment of the specifier, which is the package
			// name a reference writes. NO /vN STRIP: a path segment like v1 is
			// a real directory whose package is reached by alias, not by
			// dropping the segment.
			//
			// RULE B3N — Bind.Name STAYS EMPTY FOR GO. The declared-name
			// override applies in the unqualified import rule only, and a Go
			// alias renames the PACKAGE rather than any member of it, exactly
			// as `import * as ns` does — so `kgstore.Node` still names Node at
			// the target.
			key := b.Local
			if key == "" {
				key = path.Base(b.Specifier)
			}
			binds[key] = Bind{Scope: goImportScope(rc.ModulePath, b.Specifier)}
		}
	}
	return BindsResult{Binds: binds, DotScopes: dots}
}

// goImportScope turns one import path into the scope ID of the directory it
// names — RULES B4 AND B5, and the ONLY place a Go scope string is stamped.
//
// IT MUST AGREE BYTE-FOR-BYTE WITH ScopeID, which yields "dir:" +
// filepath.Dir(filePath) for Go. The same string feeds both the qualified
// rule's lookup and the external-qualifier rule's scope-set membership test, so
// a drift between the two stampers breaks binding and termination together —
// and both failures look identical from the outside, like a reference that was
// simply external. TestGoBindsScopeAgreesWithScopeID is the pinning test.
//
// The module-root case is load-bearing: filepath.Dir("main.go") is ".", so an
// import of the module path itself must yield "dir:." or it can never match a
// declaration in the repository root.
func goImportScope(modulePath, specifier string) string {
	if specifier == modulePath {
		return "dir:."
	}
	return "dir:" + strings.TrimPrefix(specifier, modulePath+"/")
}
