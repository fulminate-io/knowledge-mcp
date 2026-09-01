// SPDX-License-Identifier: Apache-2.0

// pathmap.go translates between this repo's layout and the public mirror's.
// The rules are not invented here: they are transcribed from the sync script's
// cp allowlist and from the five consecutive sed rewrites it applies to Go
// files, go.mod and go.sum.
//
// WHAT THIS MAP IS FOR. Reporting, never the scoring join. An alert's line
// number is true only of the mirror tree at the alert's own commit, and no
// commit correspondence between the two repos is recorded anywhere, so moving
// an alert into this repo's coordinates would fabricate a location rather than
// translate one. The map answers "which file HERE does this missed alert
// correspond to", which is what makes a finding actionable.
//
// THE PREFIX NAMES THE INPUT SPACE, uniformly for paths and import paths:
// MapMirror* takes a mirror value and returns the internal one, MapInternal*
// the reverse. The sync script's sed rules rewrite internal import paths into
// mirror ones, so they are implemented by MapInternalImport.
package calibration

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

// pathRule is one member rule of the sync allowlist. Exactly one of prefix or
// exact matching applies, chosen by isPrefix.
type pathRule struct {
	mirror   string
	internal string
	isPrefix bool
}

// pathRules is the allowlist, transcribed rule by rule from the sync script's
// cp lines. Exclusion is by PLACEMENT rather than by an ignore rule: any path
// matching none of these has no counterpart, which is a normal result and not
// an error.
//
// Order is evaluated top to bottom; prefix rules are written longest-first so a
// narrower rule cannot be shadowed by a broader one.
var pathRules = []pathRule{
	{mirror: "internal/", internal: "cmd/knowledge/internal/", isPrefix: true},
	{mirror: "gen/", internal: "gen/", isPrefix: true},
	{mirror: "docs/guides/", internal: "docs/guides/", isPrefix: true},
	{mirror: "main.go", internal: "cmd/knowledge/main.go"},
	{mirror: "go.mod", internal: "cmd/knowledge/go.mod"},
	{mirror: "go.sum", internal: "cmd/knowledge/go.sum"},
	{mirror: "install.sh", internal: "scripts/install.sh"},
	{mirror: "Dockerfile", internal: "cmd/knowledge/Dockerfile"},
	// The source file carries NO leading dot; the sync script adds it on copy.
	{mirror: ".dockerignore", internal: "cmd/knowledge/dockerignore"},
	{mirror: ".claude/KNOWLEDGE_TOOLS.md", internal: ".claude/KNOWLEDGE_TOOLS.md"},
}

// MapMirrorPath maps a repo-relative MIRROR path to its counterpart in this
// repo. A path matching no member rule is PathMirrorOnly with an empty
// counterpart — the mirror carries files this repo never produced (its README,
// its .github/ workflows, its own sync-assets script), and an alert on one of
// those is reported as having no counterpart here rather than dropped.
//
// ERRORS on an absolute path or one containing a ".." element, naming the
// offending value. These functions take repo-relative paths only, and their
// inputs arrive from the code-scanning API and from git — data crossing a
// network boundary, not a developer's literal — so a malformed path is a
// runtime condition to be returned, never a reason to take down whatever is
// scanning. The three PathClass values are all NORMAL classifications: none of
// them can carry a rejection, which is why the error is a third return rather
// than a fourth constant.
func MapMirrorPath(mirror string) (internal string, class PathClass, err error) {
	if err := checkRepoRelative("MapMirrorPath", mirror); err != nil {
		return "", PathMirrorOnly, err
	}
	if isClaudeAsset(mirror) {
		return mirror, PathMapped, nil
	}
	for _, r := range pathRules {
		if r.isPrefix {
			if rest, ok := strings.CutPrefix(mirror, r.mirror); ok {
				return r.internal + rest, PathMapped, nil
			}
			continue
		}
		if mirror == r.mirror {
			return r.internal, PathMapped, nil
		}
	}
	return "", PathMirrorOnly, nil
}

// MapInternalPath maps a repo-relative path in THIS repo to its counterpart in
// the mirror. A path matching no member rule is PathInternalOnly — the sync
// script never copies the server binary, the deploy tree or the proto sources.
//
// ERRORS on an absolute path or one containing a ".." element, for the reason
// given on MapMirrorPath.
func MapInternalPath(internal string) (mirror string, class PathClass, err error) {
	if err := checkRepoRelative("MapInternalPath", internal); err != nil {
		return "", PathInternalOnly, err
	}
	if isClaudeAsset(internal) {
		return internal, PathMapped, nil
	}
	for _, r := range pathRules {
		if r.isPrefix {
			if rest, ok := strings.CutPrefix(internal, r.internal); ok {
				return r.mirror + rest, PathMapped, nil
			}
			continue
		}
		if internal == r.internal {
			return r.mirror, PathMapped, nil
		}
	}
	return "", PathInternalOnly, nil
}

// isClaudeAsset reports whether p is one of the agent or skill definitions the
// sync script copies at identical paths. The first two are globs rather than
// fixed names, so they are matched here instead of sitting in the rule table;
// the governance file is a single FLAT file at the root of the skills tree and
// is matched by equality.
//
// The governance arm is not redundant with the skills glob: "*" in path.Match
// does not span separators, so ".claude/skills/*/SKILL.md" matches only files
// one directory down. Equality rather than a ".claude/skills/*.md" glob is
// deliberate — the sync script ships exactly this one flat file, and a
// directory-wide glob would classify an unshipped sibling as mapped.
func isClaudeAsset(p string) bool {
	if p == claudeGovernancePath {
		return true
	}
	if ok, _ := path.Match(".claude/agents/*.md", p); ok {
		return true
	}
	ok, _ := path.Match(".claude/skills/*/SKILL.md", p)
	return ok
}

// claudeGovernancePath is the flat cross-agent governance file, shipped at an
// identical path by the sync script's own named cp line.
const claudeGovernancePath = ".claude/skills/GOVERNANCE.md"

// checkRepoRelative enforces the repo-relative precondition. Both messages name
// the offending value and say what the accepted vocabulary is, so a caller
// reading the error alone can tell what it was handed and what it should hand
// instead.
func checkRepoRelative(fn, p string) error {
	if strings.HasPrefix(p, "/") {
		return fmt.Errorf("calibration: %s takes a repo-relative path; %q is absolute", fn, p)
	}
	for seg := range strings.SplitSeq(p, "/") {
		if seg == ".." {
			return fmt.Errorf("calibration: %s takes a repo-relative path; %q contains a %q element and escapes the root", fn, p, "..")
		}
	}
	return nil
}

// The module-path halves of the two module identifiers. Spelled as split
// constants so no single literal in this file is a whole module path that the
// sync script's own sed rules would rewrite when this file is copied to the
// mirror.
const (
	moduleHost     = "github.com/fulminate-io/"
	internalModule = moduleHost + "knowledge"
	mirrorModule   = moduleHost + "knowledge-mcp"
)

// notAlreadyRewritten is sed rule four: rewrite the module root only when the
// following character is not a hyphen, so an already-rewritten path is not
// turned into knowledge-mcp-mcp. Go's regexp has no lookahead, so this is the
// capture-and-reinsert form the sed itself uses. Hoisted to package scope
// because the census calls the mappers once per file.
var notAlreadyRewritten = regexp.MustCompile(regexp.QuoteMeta(internalModule) + `([^-])`)

// MapInternalImport rewrites an import path in THIS repo's module space into
// the mirror's, applying the five sed rules IN THE ORDER THE SYNC SCRIPT
// APPLIES THEM. The order is load-bearing: rule 1 must run before rule 2, or
// rule 2 rewrites the shorter prefix first and rule 1 can never fire.
func MapInternalImport(internalImport string) string {
	out := internalImport
	// 1. the client's internal tree.
	out = strings.ReplaceAll(out, internalModule+"/cmd/knowledge/internal", mirrorModule+"/internal")
	// 2. the client module root.
	out = strings.ReplaceAll(out, internalModule+"/cmd/knowledge", mirrorModule)
	// 3. the generated protobuf tree.
	out = strings.ReplaceAll(out, internalModule+"/gen", mirrorModule+"/gen")
	// 4. any remaining occurrence followed by a non-hyphen character.
	out = notAlreadyRewritten.ReplaceAllString(out, mirrorModule+"$1")
	// 5. the bare root module at end of line. NOT redundant with rule 4: rule 4
	// requires a following character, so it cannot match when nothing follows,
	// which is exactly the shape of the go.mod module line and of any import of
	// the bare root module.
	if rest, ok := strings.CutSuffix(out, internalModule); ok {
		out = rest + mirrorModule
	}
	return out
}

// MapMirrorImport is the inverse of MapInternalImport: it rewrites a mirror
// import path back into this repo's module space. An import already expressed
// in internal coordinates is returned unchanged, which is what keeps a double
// rewrite from producing knowledge-mcp-mcp.
//
// ONE AMBIGUITY IS RESOLVED HERE RATHER THAN HIDDEN. The forward direction maps
// two distinct internal roots onto the same mirror root — the client module
// (rule 2) and the bare repository module (rule 5) — so the inverse of the bare
// mirror root is not unique. It resolves to the CLIENT module, because that is
// the root every real import in the mirrored subtree came from.
func MapMirrorImport(mirrorImport string) string {
	switch {
	case strings.HasPrefix(mirrorImport, mirrorModule+"/internal"):
		return internalModule + "/cmd/knowledge/internal" + strings.TrimPrefix(mirrorImport, mirrorModule+"/internal")
	case strings.HasPrefix(mirrorImport, mirrorModule+"/gen"):
		return internalModule + "/gen" + strings.TrimPrefix(mirrorImport, mirrorModule+"/gen")
	case strings.HasPrefix(mirrorImport, mirrorModule):
		return internalModule + "/cmd/knowledge" + strings.TrimPrefix(mirrorImport, mirrorModule)
	default:
		return mirrorImport
	}
}
