// SPDX-License-Identifier: Apache-2.0

// install.go — `knowledge install` CLI subcommand. Downloads the
// matching knowledge-server release asset from the public
// knowledge-mcp GitHub releases, verifies SHA256 against the
// checksums.txt manifest, extracts the binary, and atomically
// replaces the sibling knowledge-server next to the running knowledge
// binary (or a caller-supplied --dest).
//
// CLI mode, not MCP mode: writes status lines to stdout, errors to
// stderr.

package bootstrap

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// githubAPIBaseURL is the release-metadata API base (resolves the
// "latest" tag / a pinned tag). releaseBaseURL is the release-asset
// download base. BOTH are package VARS overridable ONLY at build time
// via `-ldflags "-X …/bootstrap.githubAPIBaseURL=…"` /
// `-X …/bootstrap.releaseBaseURL=…` — legit for staging mirrors,
// air-gapped installs, and local/CI testing. There is deliberately NO
// runtime env var, flag, or config key (the shipped binary's endpoints
// are build-time only — the no-endpoint-override rule). Release builds
// keep the canonical GitHub defaults; tests override the vars directly.
//
// Asset download URLs are CONSTRUCTED from releaseBaseURL + tag + name
// (see resolveReleaseURLs) rather than taken from the release JSON's
// browser_download_url, so the download source is a build-time constant
// and a mirror override can never be defeated by remote-supplied URLs.
var (
	githubAPIBaseURL = "https://api.github.com"
	releaseBaseURL   = "https://github.com/fulminate-io/knowledge-mcp/releases/download"
)

// installFlags holds the parsed flags for `knowledge install`.
//
// --check is declared here so flag parsing is shared with the
// read-only-mode body in runCheck.
type installFlags struct {
	dest           string
	check          bool
	allowDowngrade bool
}

// registerInstallFlags registers the `knowledge install` flags on fs, binding
// each into f. Pure register-only seam (no fs.Parse, no --check branch) —
// shared by runInstall (the live CLI path) and the docs generator, which
// VisitAll's the FlagSet to render the flag table. Mirrors registerConfigFlags
// / registerLifecycleFlags.
func registerInstallFlags(fs *flag.FlagSet, f *installFlags) {
	fs.StringVar(&f.dest, "dest", "", "Destination directory for knowledge-server (default: sibling of the running knowledge binary)")
	fs.BoolVar(&f.check, "check", false, "Compare installed server version against latest release without writing")
	fs.BoolVar(&f.allowDowngrade, "allow-downgrade", false, "Permit installing a release OLDER than the currently-installed version (default: refuse)")
}

// runInstall is the entry point dispatched from RunSubcommand. Parses
// flags, branches on --check, otherwise runs the full download +
// verify + extract + atomic-install pipeline.
//
// Returns the release tag that was resolved and installed (the value
// `knowledge setup`'s update leg threads to the post-restart
// version-identity check, since the running process's compiled-in
// bootstrap.Version goes stale after the on-disk binary swap). The
// --check and error paths return "".
func runInstall(args []string) (string, error) {
	fs := flag.NewFlagSet("knowledge install", flag.ContinueOnError)
	var f installFlags
	registerInstallFlags(fs, &f)
	if err := fs.Parse(args); err != nil {
		return "", err
	}

	if f.check {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return "", runCheck(ctx)
	}

	return runInstallFull(f.dest, f.allowDowngrade)
}

// runInstallFull fetches the matching release ONCE, then installs
// both binaries from it: knowledge-server into the sibling directory
// (or --dest) and the knowledge client over the running binary in
// place (unix only). Each binary's tail — download + verify + extract
// + atomic write — aborts on the FIRST verification failure so nothing
// partial reaches disk. See installBothBinaries in install_client.go.
// Returns the resolved release tag (rel.TagName) on success so the
// caller can thread the actually-installed version to a post-install
// check; "" on any error.
func runInstallFull(flagDest string, allowDowngrade bool) (string, error) {
	goos, goarch, err := detectPlatform()
	if err != nil {
		return "", err
	}

	tag, isLatest := resolveReleaseTag(Version)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	fmt.Fprintf(os.Stdout, "knowledge install: fetching release %s for %s-%s\n", releaseTagLabel(tag, isLatest), goos, goarch)

	rel, err := fetchRelease(ctx, githubAPIBaseURL, tag, isLatest)
	if err != nil {
		return "", err
	}

	// Downgrade guard: refuse to install a release OLDER than the
	// currently-installed client (protects a dev/pre-release box, or a
	// user mid-release-window, from a bare `knowledge install` silently
	// clobbering a newer build with an older published release). Skipped
	// when either version is unparseable (e.g. an un-ldflagged "dev"
	// build) or --allow-downgrade is set.
	if !allowDowngrade {
		if cmp, ok := compareReleaseVersions(rel.TagName, Version); ok && cmp < 0 {
			return "", fmt.Errorf("knowledge install: resolved release %s is OLDER than the installed %s — refusing to downgrade. Re-run with --allow-downgrade to override", rel.TagName, Version)
		}
	}

	if err := installBothBinaries(ctx, rel, goos, goarch, flagDest); err != nil {
		return "", err
	}
	return rel.TagName, nil
}

// resolveReleaseURLs CONSTRUCTS the archive + checksums.txt download
// URLs for tag from the build-time releaseBaseURL — deliberately NOT
// from the release JSON's browser_download_url, so the download source
// is a build-time constant a mirror override fully controls. Layout
// matches the GitHub releases/download path: <base>/<tag>/<asset>.
func resolveReleaseURLs(tag, asset string) (archiveURL, checksumsURL string) {
	return releaseBaseURL + "/" + tag + "/" + asset,
		releaseBaseURL + "/" + tag + "/checksums.txt"
}

// compareReleaseVersions compares two release tags (vMAJOR.MINOR.PATCH
// with an optional -suffix such as -dev/-rc1). Returns (-1|0|+1, true)
// on a successful parse of BOTH, or (0, false) when either is
// unparseable (e.g. the un-ldflagged "dev" sentinel) so callers can
// skip a comparison they cannot make. The numeric MAJOR.MINOR.PATCH
// core dominates; when cores tie, a suffixed (pre-release) tag sorts
// BELOW an unsuffixed one, and two suffixed tags compare equal at the
// core (a v0.4.11-dev installed vs a v0.4.10 resolved is a downgrade
// because 0.4.11 > 0.4.10, independent of the -dev suffix).
func compareReleaseVersions(a, b string) (int, bool) {
	ca, sa, oka := parseSemverCore(a)
	cb, sb, okb := parseSemverCore(b)
	if !oka || !okb {
		return 0, false
	}
	for i := range 3 {
		if ca[i] != cb[i] {
			if ca[i] < cb[i] {
				return -1, true
			}
			return 1, true
		}
	}
	// Cores tie: a pre-release (has suffix) sorts below a final release.
	switch {
	case sa && !sb:
		return -1, true
	case !sa && sb:
		return 1, true
	default:
		return 0, true
	}
}

// parseSemverCore parses "vMAJOR.MINOR.PATCH[-suffix]" into its numeric
// core [3]int and whether it carried a pre-release suffix. Returns
// ok=false for anything that doesn't match (e.g. "dev", "latest", "").
func parseSemverCore(v string) (core [3]int, prerelease bool, ok bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if v == "" {
		return core, false, false
	}
	if i := strings.IndexByte(v, '-'); i >= 0 {
		prerelease = true
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return core, false, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return core, false, false
		}
		core[i] = n
	}
	return core, prerelease, true
}

// runCheck is the read-only --check body: report, for BOTH the
// knowledge client (the running binary, from bootstrap.Version) and
// the sibling knowledge-server, the installed version, the latest
// published release, and whether an update is available. Never writes
// to disk.
//
// The caller supplies ctx with a deadline so the locator + --version
// exec are bounded — a corrupt or wrong-arch binary that hangs on
// startup must not wedge `knowledge install --check`.
func runCheck(ctx context.Context) error {
	goos, goarch, err := detectPlatform()
	if err != nil {
		return err
	}

	tag, isLatest := resolveReleaseTag(Version)
	resolvedTag := tag
	if isLatest {
		rel, err := fetchRelease(ctx, githubAPIBaseURL, "", true)
		if err != nil {
			return err
		}
		resolvedTag = rel.TagName
	}

	// Client = the running binary; its version is the compiled-in
	// bootstrap.Version.
	reportBinaryStatus("client", Version, resolvedTag, goos, goarch)

	// Server = the sibling knowledge-server; probe its --version,
	// bounded by ctx so a hung binary can't wedge the check.
	installed := "not installed"
	binPath, ferr := findServerBinary()
	if ferr == nil {
		cmd := exec.CommandContext(ctx, binPath, "--version")
		out, execErr := cmd.Output()
		if execErr != nil {
			installed = fmt.Sprintf("installed (version unknown: %v)", execErr)
		} else {
			installed = strings.TrimSpace(string(out))
		}
	}
	reportBinaryStatus("server", installed, resolvedTag, goos, goarch)
	return nil
}

// reportBinaryStatus prints the installed/latest/staleness triple for
// one binary (label "client" or "server") given its installed version
// string and the resolved latest release tag.
func reportBinaryStatus(label, installed, resolvedTag, goos, goarch string) {
	fmt.Fprintf(os.Stdout, "knowledge install --check: %s installed = %s\n", label, installed)
	fmt.Fprintf(os.Stdout, "knowledge install --check: %s latest    = %s for %s-%s\n", label, resolvedTag, goos, goarch)

	switch {
	case installed == resolvedTag:
		fmt.Fprintf(os.Stdout, "knowledge install --check: %s up to date\n", label)
	case installed == "not installed" || strings.HasPrefix(installed, "installed (version unknown"):
		fmt.Fprintf(os.Stdout, "knowledge install --check: %s update available — run `knowledge install`\n", label)
	default:
		// When the installed build is NEWER than the resolved latest
		// (a dev/pre-release box, or a user mid-release-window), a plain
		// "update available" would mislead — `knowledge install` would
		// REFUSE the downgrade. Report it as such.
		if cmp, ok := compareReleaseVersions(resolvedTag, installed); ok && cmp < 0 {
			fmt.Fprintf(os.Stdout, "knowledge install --check: %s installed is NEWER than latest (installed=%s, latest=%s) — no update; `knowledge install` would refuse the downgrade\n", label, installed, resolvedTag)
			return
		}
		fmt.Fprintf(os.Stdout, "knowledge install --check: %s update available (installed=%s, latest=%s) — run `knowledge install`\n", label, installed, resolvedTag)
	}
}

// detectPlatform returns the (goos, goarch) pair for the running
// binary. Errors when the pair is not a published release target.
func detectPlatform() (string, string, error) {
	return detectPlatformFor(runtime.GOOS, runtime.GOARCH)
}

// detectPlatformFor is the test-injectable form. Pure function over
// the goos/goarch strings; callers in production pass runtime.GOOS /
// runtime.GOARCH, tests pass synthetic values to assert the
// unsupported-platform branch deterministically on any CI host.
//
// Supported set is pinned to the ci.yml release matrix:
// linux-amd64, linux-arm64, darwin-arm64, windows-amd64. Any other
// pair returns an error. darwin-amd64 (Intel Mac) gets a dedicated
// message because users hitting that pair need to know it's a
// deliberate cut and the workaround (build from source / use an
// arm64 Mac).
func detectPlatformFor(goos, goarch string) (string, string, error) {
	switch goos + "-" + goarch {
	case "linux-amd64", "linux-arm64", "darwin-arm64", "windows-amd64":
		return goos, goarch, nil
	case "darwin-amd64":
		return "", "", fmt.Errorf("darwin-amd64 is not a supported release target; build from source or use an arm64 Mac")
	default:
		return "", "", fmt.Errorf("%s-%s is not a supported release target", goos, goarch)
	}
}

// archiveExt returns the archive extension used by the release
// pipeline for the given goos. Windows ships .zip; everything else
// ships .tar.gz. Matches the release matrix verbatim.
func archiveExt(goos string) string {
	if goos == "windows" {
		return ".zip"
	}
	return ".tar.gz"
}

// assetName returns the release-asset basename for the given goos/
// goarch pair and binary basename (binBase is "knowledge-server" or
// "knowledge"). Matches the `artifact:` strings in the release matrix
// exactly.
func assetName(goos, goarch, binBase string) string {
	return fmt.Sprintf("%s-%s-%s%s", binBase, goos, goarch, archiveExt(goos))
}

// resolveReleaseTag maps the running knowledge binary's version to a
// release-tag selector. The "dev" sentinel (set when the binary is
// built without `-ldflags "-X main.version=..."`) resolves against
// the GitHub "latest" release endpoint; any other version pins to
// the exact tag so a `knowledge install` from a versioned client
// pulls a matching server.
func resolveReleaseTag(v string) (tag string, isLatest bool) {
	if v == "dev" {
		return "", true
	}
	return v, false
}

// releaseTagLabel is a tiny formatter for the user-facing status
// line. "latest" is friendlier than the empty string.
func releaseTagLabel(tag string, isLatest bool) string {
	if isLatest {
		return "latest"
	}
	return tag
}

// parseChecksums parses GNU sha256sum text-mode output:
//
//	<64 lowercase hex chars><two spaces>[*]<filename>
//
// The leading asterisk on the filename appears in binary-mode output
// from `sha256sum -b`; the release pipeline uses text mode but the
// parser tolerates both. Blank lines and malformed lines are silently
// skipped. Returns a filename → hex-digest map (digests lowercased).
func parseChecksums(raw []byte) map[string]string {
	out := make(map[string]string)
	for line := range strings.SplitSeq(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		idx := strings.Index(line, "  ")
		if idx != 64 {
			continue
		}
		digest := line[:64]
		name := strings.TrimPrefix(line[idx+2:], "*")
		if name == "" {
			continue
		}
		out[name] = strings.ToLower(digest)
	}
	return out
}

// verifyChecksum hashes archiveBytes with SHA-256 and constant-time
// compares against expectedHex. Returns a clear error naming the
// asset on mismatch. expectedHex must already be lowercase hex
// (parseChecksums returns lowercase).
func verifyChecksum(archiveBytes []byte, expectedHex, asset string) error {
	sum := sha256.Sum256(archiveBytes)
	got := hex.EncodeToString(sum[:])
	if subtle.ConstantTimeCompare([]byte(got), []byte(strings.ToLower(expectedHex))) != 1 {
		return fmt.Errorf("sha256 mismatch for %s: expected %s, got %s", asset, expectedHex, got)
	}
	return nil
}

// resolveInstallDest returns the destination DIRECTORY into which
// knowledge-server will be installed. When the user passed --dest,
// return the tilde-expanded form unchanged. When --dest is empty,
// derive the directory from the running knowledge binary so the new
// server lands next to it (the canonical install layout).
//
// EvalSymlinks resolves Homebrew-style symlinks so the install
// lands in the real Cellar directory next to the real binary —
// matching lifecycle.go sibling-lookup behavior.
func resolveInstallDest(flagDest string) (string, error) {
	if flagDest != "" {
		return expandTilde(flagDest), nil
	}
	exe, err := getExecutable()
	if err != nil {
		return "", fmt.Errorf("resolve running binary path: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Dir(exe), nil
}

// writeAtomic writes binBytes to a tempfile inside destDir, fsyncs
// it, then os.Renames it onto its final path. On Windows the final
// path is removed first because Windows can't atomically replace an
// open file via Rename (ERROR_ACCESS_DENIED). The tempfile is mode
// 0o755 on unix so the resulting binary is immediately executable.
//
// Returns the absolute final path on success. On any failure after
// the tempfile is created the tempfile is removed (best effort).
// Permission errors surface as fs.ErrPermission so the caller can
// translate into the multi-line UX hint without parsing error text.
//
// finalBase is the binary basename to install ("knowledge-server" or
// "knowledge"); the +".exe" suffix is applied on Windows.
func writeAtomic(destDir string, binBytes []byte, goos, finalBase string) (string, error) {
	finalName := finalBase
	if goos == "windows" {
		finalName = finalBase + ".exe"
	}
	finalPath := filepath.Join(destDir, finalName)
	if abs, err := filepath.Abs(finalPath); err == nil {
		finalPath = abs
	}

	tmp, err := os.CreateTemp(destDir, "knowledge-server-install-*")
	if err != nil {
		return "", fmt.Errorf("create tempfile in %s: %w", destDir, err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }

	if _, err := tmp.Write(binBytes); err != nil {
		_ = tmp.Close()
		cleanup()
		return "", fmt.Errorf("write tempfile %s: %w", tmpPath, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return "", fmt.Errorf("fsync tempfile %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", fmt.Errorf("close tempfile %s: %w", tmpPath, err)
	}
	if goos != "windows" {
		if err := os.Chmod(tmpPath, 0o755); err != nil { //nolint:gosec // executable bit required so the installed server runs
			cleanup()
			return "", fmt.Errorf("chmod tempfile %s: %w", tmpPath, err)
		}
	}
	if goos == "windows" {
		if err := os.Remove(finalPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			cleanup()
			return "", fmt.Errorf("remove existing %s: %w", finalPath, err)
		}
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		cleanup()
		return "", err
	}
	return finalPath, nil
}
