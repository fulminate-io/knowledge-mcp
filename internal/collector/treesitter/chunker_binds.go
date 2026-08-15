// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"strings"
)

// The BindsResolver arms for the statically bindable languages whose mapping is
// SHORT — one candidate derivation each, sharing the same scope-ID
// construction. The three families whose derivation grew a path model of its
// own live beside this file instead: binds_rust.go (crate anchors),
// binds_c.go (the quoted-include search ladder) and binds_jvm.go (source-root
// discovery). Every registration for all of them stays in
// RegisterLanguageBindsResolvers below — same symbols, several files.
//
// EVERY ARM'S WHOLE OBLIGATION is to record a bind for every import it can
// attribute, carrying its best-effort Scope. OMISSION IS THE ONLY WAY TO FAIL.
// An arm does NOT decide whether a target is external and does NOT skip an
// out-of-repo import: a java file importing java.util.List MUST produce a bind
// keyed List, or a reference written `List.of(...)` misses the qualified-import
// rung, finds no local container named List, and falls to the dynamic rung —
// which then emits an open-set edge to any LOCAL declaration named List,
// manufacturing exactly the wrong-edge class this work removes. Recording the
// bind is what lets the external-qualifier rung terminate instead. Externality
// is computed index-side; the arm records where it thinks the target is.
//
// A WILDCARD IMPORT IS THE ONE CASE WHERE RECORDING NOTHING IS CORRECT, because
// there is no name to key on. `import x.y.*`, a plain `using Foo.Bar;` and a
// plain `import Foundation` bind no single name and must not be expanded into a
// guess at the target's contents.
//
// AN ARM BINDS A TOP-LEVEL DECLARATION UNLESS IT RECORDS A Container. Both
// import rungs look up a declKey whose Parent is the bind's Container, and the
// empty Container every arm but jvmBinds (binds_jvm.go) records means "no
// parent", which is the top-level key. A member is therefore reachable through
// an import bind ONLY for the import forms that name one — java's
// `import static a.b.C.d` is the only such form across all four arm files — and
// every other arm's proof still targets a top-level declaration.
//
// THE KEYED byPath LOOKUP IS O(1) AND IS THE ONLY PER-IMPORT USE OF IT. No arm
// iterates byPath or rc.Files while resolving an import: that shape is
// quadratic in repo size and is re-paid by every language. THE ONE WHOLE-SET
// PASS THAT EXISTS is the JVM source-root derivation in binds_jvm.go, and it is
// admissible for exactly the reason the rule exists — it runs ONCE per
// repository behind a sync.Once on the RepoContext, so it is O(files) per
// collect rather than O(files) per import.
func init() {
	RegisterLanguageBindsResolvers()
}

// RegisterLanguageBindsResolvers installs every arm BUT the Go one, which has
// its own registrar in binds_go.go. The list stayed whole when the rust, C and
// JVM arms moved to files of their own: a registration split across the files
// that define the arms is how one gets dropped in a move and noticed only by a
// corpus measurement.
//
// It is EXPORTED for the same reason the Go arm's registrar is: a test that
// swaps a probe arm in for one of these languages must RESTORE the real one on
// cleanup rather than unregister it.
//
// THE DISTINCTION IS NOT COSMETIC. UnregisterBindsResolver DELETES the registry
// entry, so a cleanup that unregisters one of these removes the production arm
// for every later test in the same binary — and the symptom is not a missing
// arm, it is references quietly resolving through a different rung in tests
// that run afterwards.
func RegisterLanguageBindsResolvers() {
	RegisterBindsResolver(LangJava, javaBinds)
	RegisterBindsResolver(LangKotlin, kotlinBinds)
	RegisterBindsResolver(LangScala, scalaBinds)
	RegisterBindsResolver(LangPython, pythonBinds)
	RegisterBindsResolver(LangRust, rustBinds)
	RegisterBindsResolver(LangSwift, swiftBinds)
	RegisterBindsResolver(LangCSharp, csharpBinds)
	RegisterBindsResolver(LangPHP, phpBinds)
	RegisterBindsResolver(LangC, cIncludeBinds)
	RegisterBindsResolver(LangCPP, cIncludeBinds)
}

// fileImportBindings returns the import table the chunker recorded for a file,
// read off any chunk because the context is a file-level fact.
func fileImportBindings(self *Result) []ImportBinding {
	if self == nil || len(self.Chunks) == 0 {
		return nil
	}
	return self.Chunks[0].Context.ImportBindings
}

// firstPresent returns the first candidate present in byPath, or the FIRST
// candidate when none is. Returning the first rather than "" is the contract:
// an unresolvable import still records its bind, carrying a scope the index
// will report as holding nothing, which is what terminates the reference
// instead of letting it manufacture a dynamic edge to a local of the same name.
func firstPresent(byPath map[string]*Result, candidates ...string) string {
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if _, ok := byPath[c]; ok {
			return c
		}
	}
	for _, c := range candidates {
		if c != "" {
			return c
		}
	}
	return ""
}

// declaredName is the DECLARED name at the target when an alias renamed it, and
// empty when the reference's own name is already the declared one. It is what
// the unqualified import rung reads to look up `y` for a reference written `z`.
func declaredName(b ImportBinding) string {
	if b.Imported != "" && b.Imported != b.Local {
		return b.Imported
	}
	return ""
}

// pythonBinds maps `from x.y import a as b` onto the MODULE x.y — a package
// directory resolves through its __init__.py — and keys the bind by the local
// name. A plain `import x.y` binds the MODULE itself under its own name.
func pythonBinds(_ *RepoContext, byPath map[string]*Result, self *Result) BindsResult {
	bindings := fileImportBindings(self)
	binds := make(map[string]Bind, len(bindings))
	for _, b := range bindings {
		if b.Local == "" || b.Kind == ImportWildcard {
			continue
		}
		module := strings.ReplaceAll(b.Specifier, ".", "/")
		target := firstPresent(byPath, module+".py", module+"/__init__.py")
		binds[b.Local] = Bind{Scope: ScopeID(target, LangPython, ""), Name: declaredName(b)}
	}
	if len(binds) == 0 {
		return BindsResult{}
	}
	return BindsResult{Binds: binds}
}

// swiftBinds records the declaration-import forms — `import struct Foo.Bar` —
// and nothing for a plain `import Foundation`, which names a MODULE rather than
// a member.
//
// A SWIFT IMPORT NAMES NO FILE PATH THE SOURCE CONTAINS, so the arm records
// ScopeID("", LangSwift, "") — the literal "file:" — which equals no file's
// scope. Every swift bind therefore TERMINATES at the external-qualifier rung
// instead of falling into the dynamic one. That is the honest outcome for a
// mapping the source does not carry, and it is stated here rather than left to
// look like an accident.
func swiftBinds(_ *RepoContext, _ map[string]*Result, self *Result) BindsResult {
	bindings := fileImportBindings(self)
	binds := make(map[string]Bind, len(bindings))
	for _, b := range bindings {
		if b.Kind != ImportNamed || b.Local == "" {
			continue
		}
		binds[b.Local] = Bind{Scope: ScopeID("", LangSwift, ""), Name: declaredName(b)}
	}
	if len(binds) == 0 {
		return BindsResult{}
	}
	return BindsResult{Binds: binds}
}

// csharpBinds and phpBinds are the two DECLARED-NAMESPACE languages, and they
// take ScopeID's other branch: the target is identified by the namespace it
// declares rather than by a file path, so the arm passes a namespace TOKEN and
// an empty path.
//
// THE TOKEN IS BUILT THE WAY THE CHUNKER BUILDS IT — the language prefix plus
// the same sanitiser — because edge resolution reads everything before the
// FIRST '.' as the namespace token, so a C# `App.Models` assembled any other
// way is split in half.
//
// ONE NARROW MISS IS KNOWN AND IS NOT WORKED AROUND: ScopeID falls back to a
// directory scope when a file's declared namespace equals its
// directory-derived one, so a C# file declaring `namespace Foo;` inside a
// directory named Foo is indexed under a dir scope while this arm builds a
// namespace one. The two disagree, the index reports the scope empty, and the
// reference TERMINATES — a missing edge, never a wrong one. Do not add a
// directory probe to paper over it.
func csharpBinds(_ *RepoContext, _ map[string]*Result, self *Result) BindsResult {
	return declaredNamespaceBinds(self, LangCSharp)
}

func phpBinds(_ *RepoContext, _ map[string]*Result, self *Result) BindsResult {
	return declaredNamespaceBinds(self, LangPHP)
}

func declaredNamespaceBinds(self *Result, lang Language) BindsResult {
	bindings := fileImportBindings(self)
	binds := make(map[string]Bind, len(bindings))
	for _, b := range bindings {
		if b.Kind != ImportNamed || b.Local == "" || b.Specifier == "" {
			continue
		}
		binds[b.Local] = Bind{
			Scope: ScopeID("", lang, NamespaceToken(lang, b.Specifier)),
			Name:  declaredName(b),
		}
	}
	if len(binds) == 0 {
		return BindsResult{}
	}
	return BindsResult{Binds: binds}
}
