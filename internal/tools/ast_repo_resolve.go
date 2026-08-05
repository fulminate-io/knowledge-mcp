// SPDX-License-Identifier: Apache-2.0

// ast_repo_resolve.go — resolving the `repo` argument of an ast call to the
// directory the walk runs over. Split out of ast.go, which holds the intercept,
// the args struct and the operation handlers; this file holds only the
// name-or-path to directory resolution and the one optional deps capability it
// reads.

package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/session"
)

// rootDirSourcer is the OPTIONAL deps capability exposing whether the daemon's
// --root was explicitly set (vs the built-in "." default). Type-asserted rather
// than added to ClientDeps so the many test fakes that never set a root are
// unaffected; the production *client implements it over Config.RootDirSet.
type rootDirSourcer interface{ RootDirSet() bool }

// resolveRepoDir returns the directory the AST walk should run over, honoring
// the repo arg so an ast call from a session rooted at repo Y can target a
// named repo X that is checked out as a sibling directory.
//
// Base is effectiveCwd(ctx, deps): the per-session workspace cwd carried on ctx
// (HTTP transport) when present, else deps.RootDir() (the process --root, the
// stdio default). The session cwd rides in on the chain ctx that InterceptAst
// now threads through, so an HTTP ast call walks the caller's session tree while
// a stdio call walks --root.
//
// Resolution:
//   - empty base → typed "--root is empty" error (unchanged contract).
//   - repoArg == "" → walk base (the current tree) — but fail loud FIRST when
//     there is no session cwd AND --root was left at its "." default, so a
//     rootless daemon does not silently walk its own process cwd.
//   - repoArg is an ABSOLUTE PATH → an explicit directory IS the user's
//     instruction: when it stats as a directory, walk it directly (no sibling
//     probe, no ResolveCwd gate — it can target ANY local checkout, not just a
//     monorepo sibling); when it does not exist (or is not a directory), the
//     FAIL-LOUD typed error fires. This branch runs BEFORE the name-based logic.
//   - repoArg names the SAME repo as base's cwd (its basename / a path component
//     of base) → return base.
//   - repoArg names a DIFFERENT repo recorded in the machine-local manifest
//     (~/.knowledge/repos.json, populated at collect time) → return that recorded
//     directory when it still stats as a dir. This is a recorded fact, not a
//     guess: the manifest stores where the repo was actually collected from on
//     THIS machine.
//   - otherwise → typed error (the FAIL-LOUD floor). We NEVER silently fall back
//     to base for a cross-repo arg: returning base would walk the WRONG tree and
//     hand back results labeled for repoArg, the exact bug this guards against.
//
// The cross-repo path resolves ONLY via the manifest (a collect-time recorded
// path) — never a sibling-dir / git-remote / content guess. A repo name is not a
// portable filesystem path, so absent a manifest entry the fail-loud floor directs
// the caller to an absolute checkout path.
func resolveRepoDir(ctx context.Context, deps ClientDeps, repoArg string) (string, error) {
	base := strings.TrimSpace(effectiveCwd(ctx, deps))
	if base == "" {
		return "", fmt.Errorf("ast: --root is empty; pass a repo path with --root <dir>")
	}
	// Anchor a RELATIVE base to an absolute path. effectiveCwd can hand back a
	// relative root — notably the daemon's default --root of "." when no session
	// WorkspaceCwd is propagated over the HTTP transport. The walker tolerates a
	// relative root (it resolves against the daemon's process cwd), but the
	// sibling probe (filepath.Dir) and the current-repo path match below need a
	// real absolute tree: filepath.Dir(".") is "." with no parent, which is why a
	// cross-repo arg could never find a sibling. filepath.Abs anchors "." to the
	// daemon's process cwd (the repo it was launched in).
	if absBase, absErr := filepath.Abs(base); absErr == nil {
		base = absBase
	}
	repoArg = strings.TrimSpace(repoArg)
	if repoArg == "" {
		// Fail loud when the walk root is a pure default: no session cwd rode in
		// over the transport AND --root was left at its "." built-in default. In
		// that case `base` is just the daemon's process cwd, which almost never
		// is the tree the caller means — walking it silently hands back results
		// labeled for the wrong repo. Two inputs are read SEPARATELY: an explicit
		// --root OR a live session cwd each preserve the walk. A deps that does
		// not expose RootDirSet() (older/partial fakes) keeps the fallback.
		if session.WorkspaceCwdFromContext(ctx) == "" {
			if rs, ok := deps.(rootDirSourcer); ok && !rs.RootDirSet() {
				return "", fmt.Errorf("ast: no repo specified and the daemon has no project root — pass repo:<name|/abs/path> or start the daemon with --root <dir>")
			}
		}
		return base, nil
	}

	// Absolute path: an explicit directory is the user's direct instruction —
	// walk it as-is when it exists, with no sibling probe. This lets ast target
	// ANY local checkout, not just a monorepo sibling. A non-existent /
	// non-directory path hits the fail-loud floor below.
	if filepath.IsAbs(repoArg) {
		if info, statErr := os.Stat(repoArg); statErr == nil && info.IsDir() {
			return repoArg, nil
		}
		return "", fmt.Errorf("ast: repo %q is an absolute path but not an existing directory; pass an existing checkout directory, or omit repo to walk the current tree", repoArg)
	}

	// A bare NAME resolves ONLY when it names the CURRENT tree — the daemon's
	// rooted repo, whose absolute path we know first-hand from `base`. It matches
	// when repoArg is that tree's basename, or appears as a path component (the
	// cwd is a subdir of the repo). This reads the real directory; it is NOT a
	// name→path guess.
	if repoArg == filepath.Base(base) ||
		strings.HasSuffix(base, "/"+repoArg) ||
		strings.Contains(base, "/"+repoArg+"/") {
		return base, nil
	}

	// Cross-repo NAME → machine-local manifest. The ~/.knowledge/repos.json
	// manifest records, at collect time, where each repo was actually collected
	// from on THIS machine (repo name → absolute path). This is a RECORDED FACT,
	// not a portability-breaking guess: when the named repo has been collected
	// here, we know its real directory first-hand. Stat-gate it so a manifest
	// entry whose checkout has since moved/been deleted falls through to the
	// fail-loud floor rather than walking a phantom path.
	if dir, ok := lookupRepoDir(repoArg); ok {
		if info, statErr := os.Stat(dir); statErr == nil && info.IsDir() {
			return dir, nil
		}
	}

	// FAIL-LOUD: the arg names neither the current tree, an absolute path, nor a
	// repo recorded in the machine-local manifest. We deliberately do NOT GUESS a
	// directory from the name (e.g. a sibling probe): a repo name is not a portable
	// filesystem path — it lives at a different location on every machine. The
	// manifest above is the only name→dir source, because it stores the actual
	// collect-time path; absent a manifest entry, an absolute checkout path is the
	// reliable cross-repo target.
	return "", fmt.Errorf("ast: repo %q is not the current tree and is not in the local manifest (~/.knowledge/repos.json — populated when you `collect` a repo). Collect it first, register its path with manage(operation:\"register_repo\", name:%q, root:\"/abs/path\"), pass an absolute checkout path, e.g. repo=\"/path/to/%s\", or omit repo to walk the current tree", repoArg, repoArg, repoArg)
}
