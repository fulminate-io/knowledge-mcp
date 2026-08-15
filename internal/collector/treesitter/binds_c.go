// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"path"
	"strings"
)

// The C and C++ include-graph arm, in its own file for the reason binds_go.go
// and binds_rust.go are: an arm carrying a search ladder is no longer one of
// the short uniform arms chunker_binds.go holds together. Both registrations —
// LangC and LangCPP — are unchanged and still live in
// RegisterLanguageBindsResolvers.
//
// THE ROOT CAUSE THIS FILE FIXES: the arm resolved a quoted include against the
// INCLUDING FILE'S DIRECTORY alone, so leveldb's db/dbformat_test.cc including
// "db/dbformat.h" was looked up at db/db/dbformat.h and every one of that
// corpus's 534 quoted includes missed. Real projects compile with an include
// path, and the conventional members of that path are the repository root,
// include/ and src/.
//
// THE RUNG ORDER IS THE LANGUAGE'S, NOT A PREFERENCE. The C standard specifies
// that a quoted include is searched relative to the including file FIRST; the
// remaining three rungs stand in for the -I flags a build system would supply,
// which this arm deliberately does not parse.
//
// WITHIN-RUNG AMBIGUITY IS STRUCTURALLY IMPOSSIBLE. Each rung produces exactly
// ONE candidate path and byPath is keyed by path, so a rung yields at most one
// hit and there is no closed group to build. That only becomes reachable under
// a design that SEARCHES for a directory named include/ anywhere in the tree —
// the scanning the keyed-lookup rule forbids.

// cIncludeRungs is the ordered search path for a quoted include, first rung
// with a hit winning. dir is the including file's own directory.
//
// IT IS A FUNCTION AND NOT A PACKAGE-LEVEL LIST because the first rung depends
// on the including file; the other three are repository-conventional and
// constant.
func cIncludeRungs(dir, inc string) []string {
	return []string{
		path.Clean(path.Join(dir, inc)),
		path.Clean(inc),
		path.Clean(path.Join("include", inc)),
		path.Clean(path.Join("src", inc)),
	}
}

// cIncludeBinds is the include-graph arm, registered for c and for cpp. C has
// no import NAME LIST, and it needs none: the HEADER's own declarations supply
// the names, and the one-definition rule makes each unique per translation
// unit, so binding a name to the header's file scope is what C's own static
// semantics say rather than a heuristic.
//
// AN ANGLE-FORM INCLUDE RECORDS NOTHING. `<vector>` is a system header, it is
// not in the repo, and C has no name to key a bind on — this is the same shape
// as java's wildcard import (no name to bind), not the same shape as java's
// `import java.util.List` (a name that must be bound even when out of repo).
//
// AN UNRESOLVABLE INCLUDE ALSO RECORDS NOTHING, and the asymmetry with the rust
// and JVM arms is deliberate rather than an omission. Those arms bind a NAME
// THE REFERENCE WRITES, so a best-effort scope is what terminates that name at
// the external-qualifier rung; this arm binds the names it discovers INSIDE a
// resolved header, so an unresolved include has no names to bind and nothing to
// terminate. Fabricating here would invent names no header declares. leveldb's
// residue — gtest/gtest.h, port/port_config.h generated at build time,
// gmock/gmock.h, benchmark/benchmark.h — is exactly that population.
//
// ONLY TOP-LEVEL DECLARATIONS ARE BOUND, because THIS ARM SKIPS every parented
// chunk below and records no Container — an include names a file rather than a
// member of a type, so there is no container for it to name. For C that costs
// nothing; for cpp it means a class member declared in a header is not bound,
// which is stated rather than discovered.
func cIncludeBinds(_ *RepoContext, byPath map[string]*Result, self *Result) BindsResult {
	if self == nil || len(self.Chunks) == 0 {
		return BindsResult{}
	}
	dir := path.Dir(self.FilePath)
	binds := map[string]Bind{}
	for _, inc := range self.Chunks[0].Context.Imports {
		if inc == "" || strings.HasPrefix(inc, "<") {
			continue
		}
		header, target := cResolveInclude(byPath, dir, inc)
		if target == nil {
			continue
		}
		scope := ScopeID(header, self.Language, "")
		for i := range target.Chunks {
			name := target.Chunks[i].Name
			if name == "" || target.Chunks[i].ParentName != "" {
				continue
			}
			binds[name] = Bind{Scope: scope}
		}
	}
	if len(binds) == 0 {
		return BindsResult{}
	}
	return BindsResult{Binds: binds}
}

// cResolveInclude walks the rungs in order and returns the first header byPath
// holds, or a nil result when the include names nothing in the repository.
//
// PERF SHAPE: at most four keyed byPath lookups per quoted include, replacing
// one. No walk, no cache, no allocation beyond the candidate strings.
func cResolveInclude(byPath map[string]*Result, dir, inc string) (string, *Result) {
	for _, candidate := range cIncludeRungs(dir, inc) {
		if target, ok := byPath[candidate]; ok {
			return candidate, target
		}
	}
	return "", nil
}
