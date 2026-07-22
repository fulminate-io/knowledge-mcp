// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestInstall_Check_UpToDate(t *testing.T) {
	dir := t.TempDir()
	withStubInstalledServer(t, dir, "v1.2.3")

	asset := assetName(runtime.GOOS, runtime.GOARCH, "knowledge-server")
	srv := newReleaseServer(t, releaseStub{
		tag:       "v1.2.3",
		assetName: asset,
	})
	pointHTTPClientAt(t, srv)
	withVersion(t, "v1.2.3")

	out := captureStdout(t, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := runCheck(ctx); err != nil {
			t.Fatalf("runCheck: %v", err)
		}
	})

	wantLatest := fmt.Sprintf("latest    = v1.2.3 for %s-%s", runtime.GOOS, runtime.GOARCH)
	for _, want := range []string{
		// Client line (from bootstrap.Version).
		"client installed = v1.2.3",
		"client " + wantLatest,
		"client up to date",
		// Server line (from the sibling --version probe).
		"server installed = v1.2.3",
		"server " + wantLatest,
		"server up to date",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("--check output %q must contain %q", out, want)
		}
	}
}

func TestInstall_Check_UpdateAvailable(t *testing.T) {
	dir := t.TempDir()
	withStubInstalledServer(t, dir, "v1.2.2")

	asset := assetName(runtime.GOOS, runtime.GOARCH, "knowledge-server")
	srv := newReleaseServer(t, releaseStub{
		tag:       "v1.2.3",
		assetName: asset,
	})
	pointHTTPClientAt(t, srv)
	withVersion(t, "v1.2.3")

	out := captureStdout(t, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := runCheck(ctx); err != nil {
			t.Fatalf("runCheck: %v", err)
		}
	})

	for _, want := range []string{
		// Client is the running v1.2.3 binary — up to date.
		"client installed = v1.2.3",
		"client up to date",
		// Server is the stale v1.2.2 sibling — update available.
		"server installed = v1.2.2",
		"server update available",
		"installed=v1.2.2",
		"latest=v1.2.3",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("--check output %q must contain %q", out, want)
		}
	}
}

// TestInstall_Check_BoundedByContext addresses the T3 reviewer
// finding: runCheck must NOT wedge when the installed server
// binary hangs. Plant a sleep-forever shell-script as the
// "installed" server, give runCheck a tight 2s ctx, and assert it
// returns within ~3s. The production caller uses a 5s ctx; we
// stay under that to prove the bound holds and tests don't slow
// the suite.
func TestInstall_Check_BoundedByContext(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script sleep stub not portable to windows")
	}
	dir := t.TempDir()
	// Override withStubInstalledServer with a sleep-forever variant —
	// the helper hardcodes `echo`, so write directly.
	stubPath := filepath.Join(dir, "knowledge-server")
	// `sleep 60` exceeds any plausible test deadline; we expect the
	// ctx timeout to cut the child off.
	script := "#!/bin/sh\nsleep 60\n"
	if err := os.WriteFile(stubPath, []byte(script), 0o755); err != nil { //nolint:gosec // executable shell-script test fixture
		t.Fatalf("write stub: %v", err)
	}
	stdioStub := filepath.Join(dir, "stdio_stub")
	if err := os.WriteFile(stdioStub, []byte("ignored"), 0o600); err != nil {
		t.Fatalf("write stdio stub: %v", err)
	}
	withStubExecutable(t, stdioStub)
	withPATH(t, "")

	asset := assetName(runtime.GOOS, runtime.GOARCH, "knowledge-server")
	srv := newReleaseServer(t, releaseStub{
		tag:       "v1.2.3",
		assetName: asset,
	})
	pointHTTPClientAt(t, srv)
	withVersion(t, "v1.2.3")

	start := time.Now()
	out := captureStdout(t, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := runCheck(ctx); err != nil {
			t.Fatalf("runCheck: %v", err)
		}
	})
	elapsed := time.Since(start)
	if elapsed > 4*time.Second {
		t.Fatalf("runCheck did not honor ctx deadline: elapsed=%v", elapsed)
	}
	// runCheck swallows the server exec error and prints the "version
	// unknown" branch so the output still gives the user a useful
	// summary — while the client line still reports its own version.
	for _, want := range []string{
		"client installed = v1.2.3",
		"server installed = installed (version unknown",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("--check output %q must contain %q", out, want)
		}
	}
}
