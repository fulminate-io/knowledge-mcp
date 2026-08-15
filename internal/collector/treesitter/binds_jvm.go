// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"path"
	"sort"
	"strings"
)

// The three JVM-family arms, in their own file for the reason binds_go.go and
// binds_rust.go are: an arm carrying a source-root derivation is no longer one
// of the short uniform arms chunker_binds.go holds together. All three
// registrations are unchanged and still live in RegisterLanguageBindsResolvers.
//
// THE ROOT CAUSE THIS FILE FIXES: the arm probed its candidate paths against
// the REPOSITORY ROOT, so `import com.google.common.base.Preconditions` was
// looked up at com/google/common/base/Preconditions.java while the file sits at
// guava/src/com/google/common/base/Preconditions.java. Real JVM repositories put
// sources under a source root — guava/src, okhttp/src/commonJvmAndroid/kotlin,
// core/src/main/scala — and the package-shaped tail hangs off THAT.
//
// WHAT THE CANDIDATE PATHS ARE FOR, and it is NOT the bind's scope. A JVM
// package IS a namespace: scopeKinds gives java, kotlin and scala
// ScopeDeclaredNamespace, so a declaration is indexed under the package it
// DECLARES and the arm stamps ns:<lang>:<pkg> derived from the SPECIFIER, never
// from a file path. The paths exist only to tell java's two READINGS apart —
// see jvmBinds — so a source root that is never discovered shows up as the
// static reading collapsing into the plain one, binding a member against a
// package no file declares.

// jvmExtension is the file extension each JVM-family language's declarations
// live in. It is also the membership test for the source-root derivation: a
// language absent from this table demonstrates no JVM source root.
var jvmExtension = map[Language]string{
	LangJava:   ".java",
	LangKotlin: ".kt",
	LangScala:  ".scala",
}

// dottedPath turns a dot-separated specifier plus a member name into a
// source-root-relative file path with the given extension: ("a.b", "C",
// ".java") -> "a/b/C.java".
func dottedPath(specifier, name, ext string) string {
	segments := strings.Split(specifier, ".")
	if specifier == "" {
		segments = nil
	}
	segments = append(segments, name)
	return strings.Join(segments, "/") + ext
}

// jvmBinds is the shared body of the three JVM-family arms, which differ only
// in their file extension. Each records the bound name against the scope of the
// PACKAGE its dotted specifier names.
//
// THE BIND NAMES A PACKAGE, NOT A FILE PATH, because a JVM package IS a
// namespace: scopeKinds gives all three languages ScopeDeclaredNamespace, so a
// declaration is indexed under the package it DECLARES rather than the directory
// it sits in. An arm deriving a file scope here would name a string no
// declaration is indexed under, and the import rungs would go silently inert —
// measured, both directions, before this arm was written this way.
//
// THAT ALSO REMOVES THE LAYOUT ASSUMPTION the file-path derivation carried.
// kotlin and scala legally permit a package that does not match its directory,
// and under a package-derived scope those files bind exactly, where a path
// derivation would have missed them.
//
// TWO CANDIDATE PATHS ARE STILL PROBED, but ONLY to tell the two READINGS apart:
// java's `import static a.b.C.d` and its plain `import a.b.C` are not
// distinguishable from the binding alone, and the probe is the evidence that
// settles it without reading a modifier keyword. In the plain reading the
// specifier IS the package and the bound name is a top-level type; in the static
// reading the specifier's last dotted segment is the TYPE, so the package is
// everything before it and that segment is the Container the bound member is
// parented to.
//
// EACH CANDIDATE IS NOW PROBED AT EVERY DISCOVERED SOURCE ROOT rather than at
// the repository root alone, which is what makes the static reading reachable
// in a real repository at all: com/google/common/base/Preconditions.java exists
// nowhere, and guava/src/com/google/common/base/Preconditions.java exists.
//
// AN IMPORT THAT RESOLVES NEITHER CANDIDATE AT ANY ROOT TAKES THE PLAIN
// READING, which is the honest default: the source carries nothing that says
// otherwise, and the resulting bind names a package the index reports empty,
// which TERMINATES the reference rather than letting it manufacture an edge to
// a local of the name.
//
// AN UNRESOLVED IMPORT STILL RECORDS ITS BIND, and that is load-bearing rather
// than a leftover. java.*, org.* and junit.* are SUPPOSED to miss; the bind then
// carries a scope the declaration INDEX does not hold, the external-qualifier
// rung (R2X, resolve_walk.go) reads exactly that condition, and the reference
// terminates instead of manufacturing a dynamic edge to a local declaration of
// the same name. Deleting the fabrication would trade forgone binds for wrong
// edges, which is the worse failure.
//
// THE CENSUS IS WHAT KEEPS THE NUMBER HONEST. binds_entries counts recorded
// binds, fabrications included, and never claimed to count resolutions;
// binds_scopes_unknown counts exactly the recorded binds whose Scope the
// declaration index does not hold, which IS the fabrication count. A reader
// asking whether binds_entries overstates resolutions reads the difference.
func jvmBinds(rc *RepoContext, byPath map[string]*Result, self *Result, lang Language, ext string) BindsResult {
	bindings := fileImportBindings(self)
	if len(bindings) == 0 {
		return BindsResult{}
	}
	roots := jvmCandidateRoots(rc, byPath, self.FilePath)
	binds := make(map[string]Bind, len(bindings))
	for _, b := range bindings {
		if b.Kind != ImportNamed || b.Local == "" {
			continue
		}
		pkg, container := b.Specifier, ""
		plainTail := dottedPath(b.Specifier, b.Imported, ext)
		staticTail := strings.ReplaceAll(b.Specifier, ".", "/") + ext
		if staticTail != plainTail && jvmReadsAsStatic(byPath, roots, plainTail, staticTail) {
			pkg, container = splitLastSegment(b.Specifier, ".")
		}
		binds[b.Local] = Bind{
			Scope:     ScopeID("", lang, NamespaceToken(lang, pkg)),
			Name:      declaredName(b),
			Container: container,
		}
	}
	if len(binds) == 0 {
		return BindsResult{}
	}
	return BindsResult{Binds: binds}
}

// jvmReadsAsStatic reports whether an import resolves through its STATIC
// candidate — the specifier read as ending in a type name — at any root.
//
// THE PLAIN CANDIDATE IS PROBED FIRST AT EVERY ROOT, before that root's static
// one, so the plain reading keeps the precedence it had before roots existed.
// A root ordering that tried every static candidate first would re-read
// `import a.b.C` as static wherever a file named a/b.java happened to exist.
func jvmReadsAsStatic(byPath map[string]*Result, roots []string, plainTail, staticTail string) bool {
	for _, root := range roots {
		if _, ok := byPath[path.Join(root, plainTail)]; ok {
			return false
		}
		if _, ok := byPath[path.Join(root, staticTail)]; ok {
			return true
		}
	}
	return false
}

// jvmCandidateRoots is the ordered root list one file's imports are probed
// against: RUNG 1, the importing file's own ancestor directories longest first,
// then RUNG 2, the repository-level set of roots some other file demonstrates.
//
// RUNG 2 IS NOT OPTIONAL. Rung 1 can only reach roots that are ANCESTORS of the
// importing file, so it resolves same-source-root imports and misses every
// CROSS-MODULE one: a test under guava-tests/test/... importing a type under
// guava/src/... has no ancestor that reaches it.
//
// RUNG 1 IS DERIVED PER FILE AND CACHED NOWHERE, unlike the rust crate anchor.
// The distinction is what the walk COSTS: the rust anchor probes byPath at every
// ancestor and is worth caching per directory, while this list is pure string
// splitting with no lookup in it. Building it once per file rather than once per
// import is where the cost was.
func jvmCandidateRoots(rc *RepoContext, byPath map[string]*Result, filePath string) []string {
	dir := path.Dir(filePath)
	var rung1 []string
	for d := dir; ; d = path.Dir(d) {
		if d == "." || d == "/" || d == "" {
			break
		}
		rung1 = append(rung1, d)
	}
	// The repository root closes rung 1: it is the layout the arm probed
	// exclusively before roots existed, and a repository whose sources sit at
	// the top still resolves through it.
	rung1 = append(rung1, ".")
	return append(rung1, jvmSourceRoots(rc, byPath)...)
}

// jvmSourceRoots returns the repository-level source-root set, deriving it ONCE
// per RepoContext.
//
// THE Once AND THE SET ARE FIELDS ON THE RepoContext AND ARE NEVER
// PACKAGE-LEVEL. The ful1347 multi-language corpus harness constructs a fresh
// RepoContext per corpus root and measures SEVEN repositories in ONE process,
// so a package-level Once would derive the first corpus's roots and then serve
// them to every other corpus in the run — resolving one repository's imports
// against another's layout.
// TestJVMSourceRootAnchors/roots_are_derived_per_repo_context is the
// catcher, and it is buildable only because its second corpus is arranged to
// make a leak observable.
func jvmSourceRoots(rc *RepoContext, byPath map[string]*Result) []string {
	if rc == nil {
		return deriveJVMSourceRoots(byPath)
	}
	rc.jvmSourceRootsOnce.Do(func() {
		rc.jvmSourceRoots = deriveJVMSourceRoots(byPath)
	})
	return rc.jvmSourceRoots
}

// deriveJVMSourceRoots admits a directory as a source root when it is the
// prefix a file's OWN package-shaped tail hangs off: the file at
// guava/src/com/google/common/base/Preconditions.java declares package
// com.google.common.base, so guava/src is a root.
//
// THE TAIL IS MATCHED THROUGH NamespaceToken RATHER THAN BY SPLITTING THE
// DECLARED TOKEN BACK APART. The token's sanitiser maps '.' onto '_', and a
// package segment may legally CONTAIN an underscore, so splitting the token
// would mis-derive exactly the packages that do. Rebuilding the token from the
// candidate tail and comparing is exact, and it reuses the one producer every
// other stamper already agrees with.
//
// NO BUILD FILE IS PARSED AND NO DIRECTORY IS SCANNED. The derivation is one
// pass over the already-chunked file set, paid once per collect behind the
// Once, and guava demonstrates only seven distinct roots.
//
// THE ORDER IS DEEPEST FIRST, then lexicographic. Map iteration is random, so
// an unsorted set would make the probe order vary between runs of the same
// corpus; deepest-first mirrors rung 1's longest-first, so the most specific
// root is consulted before a shallower one that could also spell the tail.
func deriveJVMSourceRoots(byPath map[string]*Result) []string {
	set := map[string]bool{}
	for filePath, result := range byPath {
		if result == nil || len(result.Chunks) == 0 {
			continue
		}
		if _, ok := jvmExtension[result.Language]; !ok {
			continue
		}
		declared := result.Chunks[0].Context.PackageName
		if declared == "" {
			continue
		}
		if root, ok := jvmRootOf(filePath, result.Language, declared); ok {
			set[root] = true
		}
	}

	roots := make([]string, 0, len(set))
	for root := range set {
		roots = append(roots, root)
	}
	sort.Slice(roots, func(i, j int) bool {
		di, dj := strings.Count(roots[i], "/"), strings.Count(roots[j], "/")
		if di != dj {
			return di > dj
		}
		return roots[i] < roots[j]
	})
	return roots
}

// jvmRootOf returns the prefix of one file's directory that its declared
// package hangs off, or false when the file's directory does not spell its
// package at all — which is legal in kotlin and scala and simply demonstrates
// no root.
func jvmRootOf(filePath string, lang Language, declared string) (string, bool) {
	dir := path.Dir(filePath)
	if dir == "." || dir == "/" || dir == "" {
		return "", false
	}
	segments := strings.Split(dir, "/")
	for i := len(segments); i >= 0; i-- {
		tail := strings.Join(segments[i:], ".")
		if NamespaceToken(lang, tail) != declared {
			continue
		}
		if i == 0 {
			return ".", true
		}
		return strings.Join(segments[:i], "/"), true
	}
	return "", false
}

func javaBinds(rc *RepoContext, byPath map[string]*Result, self *Result) BindsResult {
	return jvmBinds(rc, byPath, self, LangJava, jvmExtension[LangJava])
}

func kotlinBinds(rc *RepoContext, byPath map[string]*Result, self *Result) BindsResult {
	return jvmBinds(rc, byPath, self, LangKotlin, jvmExtension[LangKotlin])
}

func scalaBinds(rc *RepoContext, byPath map[string]*Result, self *Result) BindsResult {
	return jvmBinds(rc, byPath, self, LangScala, jvmExtension[LangScala])
}
