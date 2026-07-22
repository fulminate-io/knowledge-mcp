// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"runtime"
	"strings"
	"testing"
)

// TestCompareReleaseVersions locks the semver-ish comparison the
// downgrade guard depends on — including the -dev pre-release handling
// (v0.4.11-dev's 0.4.11 core beats v0.4.10) and the unparseable "dev"
// sentinel that makes the guard skip.
func TestCompareReleaseVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
		ok   bool
	}{
		{"v0.4.10", "v0.4.11-dev", -1, true}, // resolved older than installed pre-release
		{"v0.4.11-dev", "v0.4.10", 1, true},
		{"v1.2.3", "v1.2.3", 0, true},         // equal
		{"v1.2.4", "v1.2.3", 1, true},         // higher patch
		{"v1.2.2", "v1.2.3", -1, true},        // lower patch
		{"v2.0.0", "v1.9.9", 1, true},         // major dominates
		{"v1.2.3-rc1", "v1.2.3", -1, true},    // pre-release < final at same core
		{"v1.2.3", "v1.2.3-rc1", 1, true},     // final > pre-release
		{"v1.2.3-dev", "v1.2.3-rc1", 0, true}, // two pre-releases tie at the core
		{"dev", "v1.2.3", 0, false},           // unparseable → ok=false (guard skips)
		{"v1.2.3", "latest", 0, false},
		{"v1.2", "v1.2.3", 0, false}, // wrong shape
		{"", "v1.2.3", 0, false},
	}
	for _, c := range cases {
		got, ok := compareReleaseVersions(c.a, c.b)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("compareReleaseVersions(%q, %q) = (%d, %v); want (%d, %v)", c.a, c.b, got, ok, c.want, c.ok)
		}
	}
}

// TestInstall_DowngradeGuard locks the four guard cases. The release
// server returns a tag but no usable assets, so a run that PASSES the
// guard fails LATER at download/verify — letting us distinguish
// "refused by the guard" (error names the refusal) from "proceeded past
// the guard" (a different, later error).
func TestInstall_DowngradeGuard(t *testing.T) {
	asset := assetName(runtime.GOOS, runtime.GOARCH, "knowledge-server")
	const refusal = "refusing to downgrade"

	// run drives runInstallFull with an installed Version and a server
	// that RESOLVES to `resolved`. A versioned client pins to its own tag
	// (resolveReleaseTag), so the server answers the /tags/<installed>
	// path but REPORTS tag_name=resolved; a "dev" client resolves /latest.
	run := func(t *testing.T, installed, resolved string, allow bool) error {
		t.Helper()
		withVersion(t, installed)
		stub := releaseStub{tag: installed, reportedTag: resolved, assetName: asset}
		if installed == "dev" { // dev resolves /latest, which returns `resolved`
			stub = releaseStub{tag: resolved, assetName: asset}
		}
		pointHTTPClientAt(t, newReleaseServer(t, stub))
		var err error
		_ = captureStdout(t, func() { _, err = runInstallFull(t.TempDir(), allow) })
		return err
	}

	t.Run("lower target refused", func(t *testing.T) {
		err := run(t, "v0.4.11-dev", "v0.4.10", false)
		if err == nil || !strings.Contains(err.Error(), refusal) {
			t.Fatalf("lower target must be refused; got %v", err)
		}
	})
	t.Run("equal target not refused", func(t *testing.T) {
		err := run(t, "v1.2.3", "v1.2.3", false)
		if err != nil && strings.Contains(err.Error(), refusal) {
			t.Fatalf("equal target must NOT be refused; got %v", err)
		}
	})
	t.Run("higher target proceeds", func(t *testing.T) {
		err := run(t, "v1.2.2", "v1.2.3", false)
		if err != nil && strings.Contains(err.Error(), refusal) {
			t.Fatalf("higher target must proceed past the guard; got %v", err)
		}
	})
	t.Run("--allow-downgrade overrides", func(t *testing.T) {
		err := run(t, "v0.4.11-dev", "v0.4.10", true)
		if err != nil && strings.Contains(err.Error(), refusal) {
			t.Fatalf("--allow-downgrade must bypass the guard; got %v", err)
		}
	})
	t.Run("unparseable installed (dev) skips the guard", func(t *testing.T) {
		err := run(t, "dev", "v0.4.10", false)
		if err != nil && strings.Contains(err.Error(), refusal) {
			t.Fatalf("un-ldflagged dev build must not be guarded; got %v", err)
		}
	})
}
