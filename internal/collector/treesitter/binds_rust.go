// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"path"
	"strings"
)

// The Rust binds arm, in its own file for the reason binds_go.go is: an arm
// that grew a path model of its own is no longer one of the short uniform arms
// chunker_binds.go holds together, and the 500-line block is what forces the
// separation rather than a preference. Its registration is UNCHANGED and still
// lives in RegisterLanguageBindsResolvers — same symbol, new file.
//
// THE ROOT CAUSE THIS FILE FIXES: the arm used to join a module path onto the
// REPOSITORY ROOT, so `use crate::util::Dir` in ripgrep was looked up at
// crate/util/Dir.rs. That is true of a fixture and of essentially no real
// crate, because a crate's modules hang off ITS OWN ROOT MODULE FILE — src/lib.rs,
// src/main.rs, or, in a workspace member declared by path, a main.rs sitting
// directly in the crate directory.
//
// THE ANCHOR IS THE ROOT MODULE FILE AND NOT Cargo.toml, and that is measured
// rather than preferred: ripgrep's root Cargo.toml declares
// `path = "crates/core/main.rs"` and crates/core carries no Cargo.toml of its
// own, so a Cargo.toml walk anchoring at <dir>/src lands on a directory that
// does not exist and every crate:: import under it misses.
//
// NO TOML IS PARSED ANYWHERE HERE. Every probe is a KEYED byPath lookup, and
// the walk that finds an anchor is bounded by directory depth and cached per
// directory on the RepoContext, so a corpus pays it once per directory rather
// than once per file.

// rustBinds maps a `use` path onto the module file it names, anchored on the
// crate's own root module rather than on the repository root.
//
// AN UNRESOLVED IMPORT STILL RECORDS ITS BIND, through firstPresent, and that is
// the load-bearing half of this arm rather than a leftover. `std::`, `serde::`
// and every other external crate matches no candidate; the bind then carries a
// scope the declaration INDEX does not hold, the external-qualifier rung (R2X,
// resolve_walk.go) reads exactly that condition, and the reference TERMINATES
// instead of manufacturing a dynamic edge to a local declaration of the same
// name. Deleting the fabrication would trade forgone binds for wrong edges,
// which is the worse failure.
//
// THE CENSUS IS WHAT KEEPS THE NUMBER HONEST. binds_entries counts recorded
// binds, fabrications included, and never claimed to count resolutions;
// binds_scopes_unknown counts exactly the recorded binds whose Scope the
// declaration index does not hold, which IS the fabrication count. A reader
// asking whether binds_entries overstates resolutions reads the difference.
func rustBinds(rc *RepoContext, byPath map[string]*Result, self *Result) BindsResult {
	bindings := fileImportBindings(self)
	if len(bindings) == 0 {
		return BindsResult{}
	}
	binds := make(map[string]Bind, len(bindings))
	for _, b := range bindings {
		if b.Kind != ImportNamed || b.Local == "" {
			continue
		}
		target := firstPresent(byPath, rustCandidates(rc, byPath, self.FilePath, b)...)
		binds[b.Local] = Bind{Scope: ScopeID(target, LangRust, ""), Name: declaredName(b)}
	}
	if len(binds) == 0 {
		return BindsResult{}
	}
	return BindsResult{Binds: binds}
}

// rustCandidates builds the ordered candidate list for one import: dispatch the
// specifier's FIRST SEGMENT to one of the four anchor kinds, then hang the
// remaining module path off each anchor.
//
// THE FOUR ANCHOR KINDS ARE THE LANGUAGE'S OWN and are not a heuristic:
//
//	crate::  -> the crate root, the directory holding the root module file
//	self::   -> the importing file's own module directory
//	super::  -> the module one level up from the importing file's
//	<bare>   -> the crate root, which is also the 2015-edition reading
//
// A CHAIN OF super:: SEGMENTS CLIMBS ONLY ONE LEVEL HERE. `super::super::x` is
// rare enough that no occurrence appears in either pinned corpus, and the
// unclimbed reading records a bind that resolves nothing and TERMINATES rather
// than one that resolves the wrong module.
func rustCandidates(rc *RepoContext, byPath map[string]*Result, filePath string, b ImportBinding) []string {
	segments := rustSegments(b.Specifier)
	var anchors []string
	switch {
	case len(segments) > 0 && segments[0] == "crate":
		anchors, segments = rustCrateRoots(rc, byPath, filePath), segments[1:]
	case len(segments) > 0 && segments[0] == "self":
		anchors, segments = []string{rustSelfAnchor(filePath)}, segments[1:]
	case len(segments) > 0 && segments[0] == "super":
		anchors, segments = []string{rustSuperAnchor(filePath)}, segments[1:]
	default:
		anchors = rustCrateRoots(rc, byPath, filePath)
	}

	out := make([]string, 0, len(anchors)*5)
	for _, anchor := range anchors {
		out = append(out, rustLadder(anchor, segments, b.Imported)...)
	}
	return out
}

// rustLadder is the candidate ladder under ONE anchor, first match winning.
//
// THE EMPTY-MODULE-PATH CASE IS A LADDER OF ITS OWN AND IS THE SINGLE LARGEST
// POPULATION. `use crate::Item` names an item declared in the crate's own root
// module, so the candidate is that root module FILE — lib.rs, main.rs or mod.rs
// — rather than a directory joined from segments. Replicating this ladder at
// plan time took ripgrep from 194 resolved imports to 386; omitting this case
// is what leaves the other 192 unresolved.
//
// FIRST MATCH WINS IS THE LANGUAGE'S OWN ANSWER, not a preference: rust forbids
// a module having both foo.rs and foo/mod.rs, so at most one rung of a
// well-formed crate can hold the module and there is no ambiguous group to build.
func rustLadder(anchor string, segments []string, imported string) []string {
	// The last two rungs of each ladder name the IMPORTED name as a module of
	// its own. An import that carries no imported name — a bare `use x;` — has
	// no such rung to build, and a candidate ending in a naked extension names
	// no file in any repository.
	mod := path.Join(segments...)
	if len(segments) == 0 {
		out := []string{
			path.Join(anchor, "lib.rs"),
			path.Join(anchor, "main.rs"),
			path.Join(anchor, "mod.rs"),
		}
		if imported == "" {
			return out
		}
		return append(out,
			path.Join(anchor, imported+".rs"),
			path.Join(anchor, imported, "mod.rs"))
	}
	out := []string{
		path.Join(anchor, mod+".rs"),
		path.Join(anchor, mod, "mod.rs"),
	}
	if imported == "" {
		return out
	}
	return append(out,
		path.Join(anchor, mod, imported+".rs"),
		path.Join(anchor, mod, imported, "mod.rs"))
}

// rustSegments splits a `use` specifier on rust's own separator, dropping the
// empty segment a leading or doubled `::` would produce.
func rustSegments(specifier string) []string {
	if specifier == "" {
		return nil
	}
	out := make([]string, 0, strings.Count(specifier, "::")+1)
	for s := range strings.SplitSeq(specifier, "::") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// rustSelfAnchor is the directory the importing file's OWN module holds its
// children in: the file's directory when the file IS a root module file, and
// <dir>/<stem> otherwise — src/util.rs is the module util, whose children live
// in src/util/.
func rustSelfAnchor(filePath string) string {
	dir := path.Dir(filePath)
	if rustRootModuleFile(filePath) {
		return dir
	}
	return path.Join(dir, strings.TrimSuffix(path.Base(filePath), ".rs"))
}

// rustSuperAnchor is the directory the module ABOVE the importing file's holds
// its children in. src/util/inner.rs is util::inner, so super:: is util and its
// directory is src/util; src/util/mod.rs IS util, so super:: is the crate's
// root module and its directory is src.
func rustSuperAnchor(filePath string) string {
	dir := path.Dir(filePath)
	if rustRootModuleFile(filePath) {
		return path.Dir(dir)
	}
	return dir
}

// rustRootModuleFile reports whether a path names a file that IS its module
// rather than a file INSIDE one.
func rustRootModuleFile(filePath string) bool {
	switch path.Base(filePath) {
	case "mod.rs", "lib.rs", "main.rs":
		return true
	}
	return false
}

// rustCrateRoots returns the ordered anchor directories a crate-relative path
// hangs off: the nearest ancestor of the importing file that holds a root
// module file, or — when the walk finds none — the nearest ancestor holding a
// Cargo.toml, tried as <cargoDir>/src then <cargoDir> itself.
//
// THE CACHE IS KEYED BY DIRECTORY AND LIVES ON THE RepoContext, so the walk is
// paid once per directory per collect rather than once per file: a corpus is
// thousands of files across hundreds of directories, and the answer depends on
// the directory alone.
//
// IT IS PER-RepoContext AND NEVER PACKAGE-LEVEL. The ful1347 multi-language
// corpus harness measures seven repositories in ONE process, constructing a
// fresh RepoContext per corpus; a package-level cache would serve the first
// corpus's anchors to all seven, resolving one repository's imports against
// another's layout with no gate able to see it.
func rustCrateRoots(rc *RepoContext, byPath map[string]*Result, filePath string) []string {
	dir := path.Dir(filePath)
	if rc == nil {
		return rustDeriveCrateRoots(byPath, dir)
	}
	rc.rustAnchorMu.Lock()
	defer rc.rustAnchorMu.Unlock()
	if cached, ok := rc.rustAnchors[dir]; ok {
		return cached
	}
	derived := rustDeriveCrateRoots(byPath, dir)
	if rc.rustAnchors == nil {
		rc.rustAnchors = map[string][]string{}
	}
	rc.rustAnchors[dir] = derived
	return derived
}

// rustDeriveCrateRoots walks up from one directory, probing each ancestor for a
// root module file and then for a Cargo.toml. Every probe is a KEYED lookup:
// nothing scans byPath and nothing reads the filesystem.
func rustDeriveCrateRoots(byPath map[string]*Result, dir string) []string {
	for d := dir; ; d = path.Dir(d) {
		if _, ok := byPath[path.Join(d, "lib.rs")]; ok {
			return []string{d}
		}
		if _, ok := byPath[path.Join(d, "main.rs")]; ok {
			return []string{d}
		}
		if d == "." || d == "/" || d == "" {
			break
		}
	}
	// THE FALLBACK, and it is deliberately weaker than the walk above. A crate
	// whose root module file was never chunked — gitignored, generated, or
	// excluded by discovery — still has a manifest, and src/ is the layout
	// cargo creates by default. The repository root is the last resort, which
	// is exactly the pre-fix behavior and keeps a flat single-crate fixture
	// resolving.
	cargoDir := "."
	for d := dir; ; d = path.Dir(d) {
		if _, ok := byPath[path.Join(d, "Cargo.toml")]; ok {
			cargoDir = d
			break
		}
		if d == "." || d == "/" || d == "" {
			break
		}
	}
	return []string{path.Join(cargoDir, "src"), cargoDir}
}
