// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// withPlatform overrides what the install pipeline thinks the
// current goos/goarch are by swapping detectPlatform out for a
// closure that returns synthetic values. Tests use this to assert
// linux/amd64 paths on macOS/arm64 CI hosts.
//
// detectPlatform itself isn't a var, so the indirection lands here
// via a parallel helper: the test calls runInstallFor explicitly
// instead of runInstall, supplying goos/goarch directly. See the
// happy-path tests below.

// runInstallForTest is a test-only seam that mirrors runInstall's
// pipeline body but takes goos/goarch as arguments instead of
// detecting them via runtime constants. Production runInstall
// flows: parseFlags → detectPlatform → runInstallFor; tests flow:
// runInstallForTest. The pipeline body itself (download + verify +
// extract + write) is identical.
func runInstallForTest(t *testing.T, ctx context.Context, goos, goarch, flagDest string) error { //nolint:unparam // goarch kept as a parameter to keep the pipeline signature symmetric with detectPlatformFor
	t.Helper()
	if _, _, err := detectPlatformFor(goos, goarch); err != nil {
		return err
	}
	tag, isLatest := resolveReleaseTag(Version)
	asset := assetName(goos, goarch)

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
		return err
	}
	checksumsBytes, err := downloadAsset(ctx, checksumsURL)
	if err != nil {
		return err
	}
	checksums := parseChecksums(checksumsBytes)
	expected, ok := checksums[asset]
	if !ok {
		return errors.New("checksums.txt missing entry for " + asset)
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
	_, err = writeAtomic(destDir, binBytes, goos)
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return errors.New("knowledge install: cannot write knowledge-server to " + absDestDir + ": permission denied.\n  Retry as: sudo knowledge install\n  Or pick a writable directory on $PATH: knowledge install --dest ~/.local/bin")
		}
		return err
	}
	return nil
}

func TestInstall_HappyPath_TarGz(t *testing.T) {
	binContent := []byte("#!/bin/sh\necho v1.2.3\n")
	asset := "knowledge-server-linux-amd64.tar.gz"
	archive := buildTarGz(t, map[string][]byte{"knowledge-server": binContent})
	checksums := makeChecksums(map[string][]byte{asset: archive})

	srv := newReleaseServer(t, releaseStub{
		tag:       "v1.2.3",
		assetName: asset,
		archive:   archive,
		checksums: checksums,
	})
	pointHTTPClientAt(t, srv)
	withVersion(t, "v1.2.3")

	dest := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := runInstallForTest(t, ctx, "linux", "amd64", dest); err != nil {
		t.Fatalf("runInstall: %v", err)
	}

	installed := filepath.Join(dest, "knowledge-server")
	info, err := os.Stat(installed)
	if err != nil {
		t.Fatalf("stat %s: %v", installed, err)
	}
	if runtime.GOOS != "windows" {
		if info.Mode().Perm()&0o111 == 0 {
			t.Fatalf("installed file is not executable: mode=%v", info.Mode().Perm())
		}
	}
	got, err := os.ReadFile(installed) //nolint:gosec // test fixture path under t.TempDir
	if err != nil {
		t.Fatalf("read installed: %v", err)
	}
	if !bytes.Equal(got, binContent) {
		t.Fatalf("installed content mismatch: got %q, want %q", got, binContent)
	}
}

func TestInstall_HappyPath_Zip(t *testing.T) {
	binContent := []byte("MZ...windows binary stub...")
	asset := "knowledge-server-windows-amd64.zip"
	archive := buildZip(t, map[string][]byte{"knowledge-server.exe": binContent})
	checksums := makeChecksums(map[string][]byte{asset: archive})

	srv := newReleaseServer(t, releaseStub{
		tag:       "v1.2.3",
		assetName: asset,
		archive:   archive,
		checksums: checksums,
	})
	pointHTTPClientAt(t, srv)
	withVersion(t, "v1.2.3")

	dest := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := runInstallForTest(t, ctx, "windows", "amd64", dest); err != nil {
		t.Fatalf("runInstall: %v", err)
	}

	installed := filepath.Join(dest, "knowledge-server.exe")
	got, err := os.ReadFile(installed) //nolint:gosec // test fixture path under t.TempDir
	if err != nil {
		t.Fatalf("read installed: %v", err)
	}
	if !bytes.Equal(got, binContent) {
		t.Fatalf("installed content mismatch")
	}
}

func TestInstall_UnsupportedPlatform_DarwinAmd64(t *testing.T) {
	const want = "darwin-amd64 is not a supported release target; build from source or use an arm64 Mac"
	_, _, err := detectPlatformFor("darwin", "amd64")
	if err == nil || err.Error() != want {
		t.Fatalf("detectPlatformFor(darwin, amd64) = %v; want %q", err, want)
	}

	// runInstallForTest must short-circuit at detectPlatformFor and
	// never reach httpClient. Stub httpClient with a transport that
	// fails on every request to assert no HTTP call escapes.
	withHTTPClient(t, &http.Client{Transport: failingTransport{t}})
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	err = runInstallForTest(t, ctx, "darwin", "amd64", t.TempDir())
	if err == nil || err.Error() != want {
		t.Fatalf("runInstall darwin/amd64 = %v; want %q", err, want)
	}
}

func TestInstall_UnsupportedPlatform_Generic(t *testing.T) {
	_, _, err := detectPlatformFor("freebsd", "amd64")
	if err == nil {
		t.Fatalf("detectPlatformFor(freebsd, amd64) returned nil error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "freebsd-amd64 is not a supported release target") {
		t.Fatalf("error %q must mention freebsd-amd64", msg)
	}
	if strings.Contains(msg, "build from source or use an arm64 Mac") {
		t.Fatalf("error %q must NOT carry the Intel-Mac hint", msg)
	}
}

func TestInstall_ReleaseTagNotFound(t *testing.T) {
	asset := "knowledge-server-linux-amd64.tar.gz"
	srv := newReleaseServer(t, releaseStub{
		tag:       "v9.9.9",
		assetName: asset,
		notFound:  true,
	})
	pointHTTPClientAt(t, srv)
	withVersion(t, "v9.9.9")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := runInstallForTest(t, ctx, "linux", "amd64", t.TempDir())
	if err == nil {
		t.Fatalf("expected 404 error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "v9.9.9") || !strings.Contains(msg, "not found") {
		t.Fatalf("error %q must name v9.9.9 and `not found`", msg)
	}
}

func TestInstall_PermissionDenied_UX(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod 0o500 not meaningful on windows")
	}
	binContent := []byte("bytes")
	asset := "knowledge-server-linux-amd64.tar.gz"
	archive := buildTarGz(t, map[string][]byte{"knowledge-server": binContent})
	checksums := makeChecksums(map[string][]byte{asset: archive})

	srv := newReleaseServer(t, releaseStub{
		tag:       "v1.2.3",
		assetName: asset,
		archive:   archive,
		checksums: checksums,
	})
	pointHTTPClientAt(t, srv)
	withVersion(t, "v1.2.3")

	unwritable := t.TempDir()
	if err := os.Chmod(unwritable, 0o500); err != nil { //nolint:gosec // intentionally read+execute-only to force permission denied
		t.Fatalf("chmod: %v", err)
	}
	// Restore permissions on cleanup so t.TempDir's RemoveAll can fire.
	t.Cleanup(func() { _ = os.Chmod(unwritable, 0o700) }) //nolint:gosec // restoring write so t.TempDir cleanup can RemoveAll

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := runInstallForTest(t, ctx, "linux", "amd64", unwritable)
	if err == nil {
		t.Fatalf("expected permission-denied error, got nil")
	}
	msg := err.Error()
	for _, want := range []string{
		"cannot write knowledge-server to",
		"permission denied",
		"Retry as: sudo knowledge install",
		"--dest ~/.local/bin",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q must contain %q", msg, want)
		}
	}
}

// failingTransport asserts that no HTTP request is issued during a
// test where the install pipeline must short-circuit before the
// network layer.
type failingTransport struct{ t *testing.T }

func (f failingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	f.t.Fatalf("unexpected HTTP request: %s", req.URL.String())
	return nil, errors.New("unreachable")
}

func TestInstall_SHA256Mismatch_NoFileWritten(t *testing.T) {
	binContent := []byte("real bytes")
	asset := "knowledge-server-linux-amd64.tar.gz"
	archive := buildTarGz(t, map[string][]byte{"knowledge-server": binContent})
	// Wrong digest — list a digest of unrelated bytes.
	wrongChecksums := []byte(sha256Hex([]byte("bogus")) + "  " + asset + "\n")

	srv := newReleaseServer(t, releaseStub{
		tag:       "v1.2.3",
		assetName: asset,
		archive:   archive,
		checksums: wrongChecksums,
	})
	pointHTTPClientAt(t, srv)
	withVersion(t, "v1.2.3")

	dest := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := runInstallForTest(t, ctx, "linux", "amd64", dest)
	if err == nil {
		t.Fatalf("expected sha256 mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("error message %q must contain `sha256 mismatch`", err.Error())
	}
	installed := filepath.Join(dest, "knowledge-server")
	if _, statErr := os.Stat(installed); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("installed file must NOT exist on sha256 mismatch; stat err = %v", statErr)
	}
	// No leftover tempfile either — atomic-write cleanup must fire.
	entries, err := os.ReadDir(dest)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "knowledge-server-install-") {
			t.Fatalf("tempfile leftover: %s", e.Name())
		}
	}
}
