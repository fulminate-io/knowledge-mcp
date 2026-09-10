// SPDX-License-Identifier: Apache-2.0

// style_scope.go — the SCOPE vocabulary the two nodes that carry a style scope
// share: the practice rule and the corpus check that enforces it.
//
// WHY IT LIVES HERE AND NOT BESIDE EITHER READER. The keys are declared in this
// package (metadata_keys.go) and both readers are one import away from each
// other in the wrong direction: the corpus scanner cannot import the tools
// package, because tools imports the scanner. That leaves this package — already
// the home of the two key spellings and of the shared render widths — as the one
// place a practice-side reader and a check-side reader can agree by
// CONSTRUCTION. It is the same move style_render.go records for the widths, made
// for the same reason and at the same moment: a second consumer arrived.
//
// WHAT IS DELIBERATELY NOT HERE IS THE PATH MATCH. A scope path matches at
// path-SEGMENT boundaries through parser.MatchesPathPrefixes, the predicate the
// corpus walk narrows its own scope by, and this package cannot import that one:
// the parser imports kgtypes, so the edge already runs the other way. Splitting
// the match out is not a compromise — it is what keeps ONE matcher. Both readers
// call the parser's predicate directly; only the DECODE is shared here, and a
// second decode of a JSON array is exactly the drift this file exists to stop.

package kgtypes

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"
)

// StyleScope is a rule's or a check's OPTIONAL narrowing, as read off its node
// metadata. The two `...Set` booleans are load-bearing and are not derivable
// from the values: an ABSENT key means "applies everywhere" while a
// present-but-empty one would be a second spelling of the same default, which is
// why nothing writes one.
type StyleScope struct {
	// Repo is the ONE repository name the node applies to, when RepoSet.
	Repo string
	// RepoSet reports that the repo key was present.
	RepoSet bool
	// Paths are the repo-relative path prefixes the node applies under, when
	// PathsSet. They match at path-SEGMENT boundaries.
	Paths []string
	// PathsSet reports that the paths key was present.
	PathsSet bool
}

// Scoped reports whether this scope narrows anything at all. An unscoped node
// applies everywhere, which is the default and the majority case.
func (s StyleScope) Scoped() bool { return s.RepoSet || s.PathsSet }

// StyleScopeFromMetadata reads a scope off node metadata.
//
// A MALFORMED style_scope_paths VALUE IS AN ERROR, not an empty scope. An
// unparseable array would otherwise read as "this node applies everywhere",
// which is the widest possible answer to a question the data could not answer —
// and on the check side that reading would run a scoped check over a whole
// repository its author never meant it to see.
func StyleScopeFromMetadata(md map[string]string) (StyleScope, error) {
	var sc StyleScope
	if repo, ok := md[MetaKeyStyleScopeRepo]; ok {
		sc.Repo, sc.RepoSet = repo, true
	}
	raw, ok := md[MetaKeyStyleScopePaths]
	if !ok {
		return sc, nil
	}
	paths, err := StyleScopePathsDecode(raw)
	if err != nil {
		return StyleScope{}, err
	}
	sc.Paths, sc.PathsSet = paths, true
	return sc, nil
}

// StyleScopePathsDecode reads the JSON-array encoding of the path-scope value.
func StyleScopePathsDecode(raw string) ([]string, error) {
	var paths []string
	if err := json.Unmarshal([]byte(raw), &paths); err != nil {
		return nil, fmt.Errorf(
			"metadata[%s]=%q is not a JSON array of path prefixes: %w",
			MetaKeyStyleScopePaths, raw, err)
	}
	return paths, nil
}

// StyleScopePathsEncode renders path prefixes as the JSON-array metadata value.
//
// A JSON ARRAY RATHER THAN A COMMA JOIN, and the difference is not stylistic: a
// repo-relative path may legally contain a comma and the corpus walk admits and
// scans one, so a comma join silently turns one legitimate path into two that do
// not exist. The array carries every byte a path can hold.
func StyleScopePathsEncode(paths []string) (string, error) {
	b, err := json.Marshal(paths)
	if err != nil {
		return "", fmt.Errorf("encode %s: %w", MetaKeyStyleScopePaths, err)
	}
	return string(b), nil
}

// StyleScopeRepoRefusal validates ONE scope repo value, returning the empty
// string when it is well formed.
//
// A REPO SCOPE IS A GRAPH INSTANCE NAME, WHICH IS A BARE NAME. The code graph is
// keyed by the basename of the checkout, so a value carrying a separator, or a
// leading dot component, is a path somebody wrote where a name belongs and it
// can never equal the name the scan compares it against. Refused naming the
// value rather than trimmed to its basename, because a silently rewritten scope
// is a scope its author cannot see they got wrong.
func StyleScopeRepoRefusal(repo string) string {
	switch {
	case strings.TrimSpace(repo) == "":
		return "is empty"
	case strings.ContainsAny(repo, "/\\"):
		return "contains a path separator; a repo scope is the graph instance NAME, which is the checkout's basename"
	case repo == "." || repo == "..":
		return "is a relative path component, not a repository name"
	}
	return ""
}

// StyleScopePathRefusal validates ONE scope path against the spelling the walk
// uses, returning the empty string when it is well formed.
//
// THE THREE REFUSALS ARE THE THREE WAYS A PATH CAN BE WRITTEN AND NEVER MATCH.
// An absolute path is not repo-relative and matches nothing; a `..` segment
// escapes the repo the scope is expressed against; and any spelling that is not
// already canonical — a leading `./`, a trailing slash, a doubled separator —
// compares unequal to the paths the walk produces. Each is refused naming the
// value rather than normalized, because a silently rewritten scope is a scope
// the author cannot see they got wrong.
func StyleScopePathRefusal(p string) string {
	switch {
	case strings.TrimSpace(p) == "":
		return "is empty"
	case path.IsAbs(p) || strings.HasPrefix(p, "/"):
		return "is absolute; scope paths are repo-relative"
	case p == ".." || strings.HasPrefix(p, "../") ||
		strings.Contains(p, "/../") || strings.HasSuffix(p, "/.."):
		return "contains a `..` segment, which escapes the repository the scope is relative to"
	case path.Clean(p) != p:
		return fmt.Sprintf("is not the walk's repo-relative spelling (it would canonicalize to %q)", path.Clean(p))
	}
	return ""
}
