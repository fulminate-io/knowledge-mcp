// SPDX-License-Identifier: Apache-2.0

// install.go — `knowledge install` CLI subcommand. Downloads the
// matching knowledge-server release asset from the public
// knowledge-mcp GitHub releases, verifies SHA256 against the
// checksums.txt manifest, extracts the binary, and atomically
// replaces the sibling knowledge-server next to the running stdio
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
	"strings"
	"time"
)

// githubAPIBaseURL is the production base URL for the public release
// API. Overridable as a parameter (not a flag) so install_test.go can
// point fetchers at an httptest.Server. The OSS server distribution
// rides github.com/fulminate-io/knowledge-mcp releases.
const githubAPIBaseURL = "https://api.github.com"

// installFlags holds the parsed flags for `knowledge install`.
//
// --check is declared here so flag parsing is shared with the
// read-only-mode body in runCheck.
type installFlags struct {
	dest  string
	check bool
}

// runInstall is the entry point dispatched from RunSubcommand. Parses
// flags, branches on --check, otherwise runs the full download +
// verify + extract + atomic-install pipeline.
func runInstall(args []string) error {
	fs := flag.NewFlagSet("knowledge install", flag.ContinueOnError)
	var f installFlags
	fs.StringVar(&f.dest, "dest", "", "Destination directory for knowledge-server (default: sibling of running stdio binary)")
	fs.BoolVar(&f.check, "check", false, "Compare installed server version against latest release without writing")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if f.check {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return runCheck(ctx)
	}

	return runInstallFull(f.dest)
}

// runInstallFull implements the download + verify + extract + write
// pipeline. The pipeline aborts on the FIRST verification failure
// (sha256 mismatch / archive-shape violation) — nothing reaches the
// destination until every check passes.
func runInstallFull(flagDest string) error {
	goos, goarch, err := detectPlatform()
	if err != nil {
		return err
	}

	tag, isLatest := resolveReleaseTag(Version)
	asset := assetName(goos, goarch)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	fmt.Fprintf(os.Stdout, "knowledge install: fetching release %s for %s-%s\n", releaseTagLabel(tag, isLatest), goos, goarch)

	rel, err := fetchRelease(ctx, githubAPIBaseURL, tag, isLatest)
	if err != nil {
		return err
	}
	archiveURL, checksumsURL, err := resolveReleaseURLs(rel, asset)
	if err != nil {
		return err
	}

	archiveBytes, err := downloadAsset(ctx, archiveURL)
	if err != nil {
		return fmt.Errorf("download %s: %w", asset, err)
	}
	checksumsBytes, err := downloadAsset(ctx, checksumsURL)
	if err != nil {
		return fmt.Errorf("download checksums.txt: %w", err)
	}

	checksums := parseChecksums(checksumsBytes)
	expected, ok := checksums[asset]
	if !ok {
		return fmt.Errorf("checksums.txt missing entry for %s", asset)
	}
	if err := verifyChecksum(archiveBytes, expected, asset); err != nil {
		return err
	}

	binBytes, err := extractArchive(archiveBytes, goos)
	if err != nil {
		return err
	}

	destDir, err := resolveInstallDest(flagDest)
	if err != nil {
		return err
	}
	absDestDir, _ := filepath.Abs(destDir)

	finalPath, err := writeAtomic(destDir, binBytes, goos)
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return fmt.Errorf("knowledge install: cannot write knowledge-server to %s: permission denied.\n  Retry as: sudo knowledge install\n  Or pick a writable directory on $PATH: knowledge install --dest ~/.local/bin", absDestDir)
		}
		return err
	}

	fmt.Fprintf(os.Stdout, "knowledge install: wrote %s (%d bytes, %s)\n", finalPath, len(binBytes), rel.TagName)
	return nil
}

// resolveReleaseURLs picks the archive asset + the checksums.txt
// asset from the release JSON. Returns a clear error when either is
// missing — typically means the release was published for a
// different platform set or the upload was incomplete.
func resolveReleaseURLs(rel *releaseResponse, asset string) (archiveURL, checksumsURL string, err error) {
	for _, a := range rel.Assets {
		switch a.Name {
		case asset:
			archiveURL = a.BrowserDownloadURL
		case "checksums.txt":
			checksumsURL = a.BrowserDownloadURL
		}
	}
	if archiveURL == "" {
		return "", "", fmt.Errorf("release %s has no asset named %s", rel.TagName, asset)
	}
	if checksumsURL == "" {
		return "", "", fmt.Errorf("release %s has no checksums.txt asset", rel.TagName)
	}
	return archiveURL, checksumsURL, nil
}

// runCheck is the read-only --check body: report the installed
// server version, the latest published release, and whether an
// update is available. Never writes to disk.
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

	fmt.Fprintf(os.Stdout, "knowledge install --check: installed = %s\n", installed)
	fmt.Fprintf(os.Stdout, "knowledge install --check: latest    = %s for %s-%s\n", resolvedTag, goos, goarch)

	switch {
	case installed == resolvedTag:
		fmt.Fprintln(os.Stdout, "knowledge install --check: up to date")
	case installed == "not installed" || strings.HasPrefix(installed, "installed (version unknown"):
		fmt.Fprintln(os.Stdout, "knowledge install --check: update available — run `knowledge install`")
	default:
		fmt.Fprintf(os.Stdout, "knowledge install --check: update available (installed=%s, latest=%s) — run `knowledge install`\n", installed, resolvedTag)
	}
	return nil
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
// goarch pair. Matches the `artifact:` strings in the release matrix
// exactly.
func assetName(goos, goarch string) string {
	return fmt.Sprintf("knowledge-server-%s-%s%s", goos, goarch, archiveExt(goos))
}

// resolveReleaseTag maps the running stdio binary's version to a
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
// derive the directory from the running stdio binary so the new
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
func writeAtomic(destDir string, binBytes []byte, goos string) (string, error) {
	finalName := "knowledge-server"
	if goos == "windows" {
		finalName = "knowledge-server.exe"
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
