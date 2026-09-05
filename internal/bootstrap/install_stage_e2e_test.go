// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stagedInstallFixture builds a two-asset release whose checksums manifest
// covers both, and returns the archives plus the manifest. corruptClient flips a
// byte of the CLIENT archive AFTER the manifest is computed, so the served
// client asset fails verifyChecksum while the server asset verifies cleanly —
// exercising the sha256 mismatch branch, which is the failure a real truncated
// or tampered download produces.
func stagedInstallFixture(t *testing.T, corruptClient bool) (serverAsset, clientAsset string, serverArchive, clientArchive, checksums, serverContent []byte) {
	t.Helper()
	serverContent = []byte("#!/bin/sh\necho NEW server\n")
	clientContent := []byte("#!/bin/sh\necho NEW client\n")
	serverAsset = "knowledge-server-linux-amd64.tar.gz"
	clientAsset = "knowledge-linux-amd64.tar.gz"
	serverArchive = buildTarGz(t, map[string][]byte{"knowledge-server": serverContent})
	clientArchive = buildTarGz(t, map[string][]byte{"knowledge": clientContent})
	checksums = makeChecksums(map[string][]byte{serverAsset: serverArchive, clientAsset: clientArchive})
	if corruptClient {
		clientArchive = append([]byte(nil), clientArchive...)
		clientArchive[len(clientArchive)/2] ^= 0xFF
	}
	return serverAsset, clientAsset, serverArchive, clientArchive, checksums, serverContent
}

// TestInstall_StageBoth_MidFailureLeavesBinUntouched is the reproduction AND the
// regression for the mixed-pair defect: a dual-binary install whose SECOND asset
// fails verification must leave the destination byte-untouched, not half-swapped.
//
// RED DIRECTION, stated honestly. Against the unfixed tree only ASSERTION 3
// below is genuinely red: the server leg has already been renamed into place by
// the time the client leg's checksum fails, so the seeded knowledge-server holds
// the NEW bytes. Assertions 1 and 2 pass on the unfixed tree too (the run does
// error, and the per-write cleanup does remove its own temp file) and are
// labeled here as characterization guards rather than claimed as the
// reproduction.
//
// NAME THE CATCHER: if the commit phase were left inside the per-binary loop,
// assertion 3 is what goes red. No other test in this package would — every
// other install test serves assets that all verify.
func TestInstall_StageBoth_MidFailureLeavesBinUntouched(t *testing.T) {
	serverAsset, clientAsset, serverArchive, clientArchive, checksums, _ := stagedInstallFixture(t, true)

	srv := newReleaseServer(t, releaseStub{
		tag:             "v1.2.3",
		assetName:       serverAsset,
		archive:         serverArchive,
		clientAssetName: clientAsset,
		clientArchive:   clientArchive,
		checksums:       checksums,
	})
	pointHTTPClientAt(t, srv)
	withVersion(t, "v1.2.3")

	dest := t.TempDir()
	oldServer := []byte("OLD server bytes — must survive")
	oldClient := []byte("OLD client bytes — must survive")
	serverPath := filepath.Join(dest, "knowledge-server")
	clientPath := filepath.Join(dest, "knowledge")
	if err := os.WriteFile(serverPath, oldServer, 0o755); err != nil { //nolint:gosec // fixture binary needs the executable bit
		t.Fatalf("seed server: %v", err)
	}
	if err := os.WriteFile(clientPath, oldClient, 0o755); err != nil { //nolint:gosec // fixture binary needs the executable bit
		t.Fatalf("seed client: %v", err)
	}
	withStubExecutable(t, clientPath)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	err := runInstallForTest(t, ctx, "linux", "amd64", dest)

	// ASSERTION 1 (characterization guard): the run fails.
	if err == nil {
		t.Fatalf("a corrupt client asset must fail the install")
	}
	if !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("expected the client asset's checksum failure, got: %v", err)
	}

	// ASSERTION 3 (THE REPRODUCTION): both seeded files still hold their
	// original bytes. On the unfixed tree the server has already been swapped.
	for _, tc := range []struct {
		path string
		want []byte
		name string
	}{
		{serverPath, oldServer, "knowledge-server"},
		{clientPath, oldClient, "knowledge"},
	} {
		got, readErr := os.ReadFile(tc.path) //nolint:gosec // test fixture path under t.TempDir
		if readErr != nil {
			t.Fatalf("read %s: %v", tc.name, readErr)
		}
		if !bytes.Equal(got, tc.want) {
			t.Errorf("%s was modified by an install that FAILED: got %q, want the original %q — a failure before the swap must leave the destination byte-untouched, and a half-swapped pair is a broken installation",
				tc.name, got, tc.want)
		}
	}

	// ASSERTION 2 (characterization guard): no staged temp file survives.
	// Separates "aborted correctly" from "aborted and littered".
	entries, readErr := os.ReadDir(dest)
	if readErr != nil {
		t.Fatalf("readdir: %v", readErr)
	}
	if len(entries) != 2 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("the destination holds %d entries after a failed install (%v); only the two seeded binaries should remain", len(entries), names)
	}
}

// TestInstall_StageBoth_CommitFailureDiscardsRemainingStaged gates the
// commit-phase abort the FIX introduces, which no reproduction against the
// unfixed tree can cover: the fetch-phase guarantee is total (nothing
// committed), while the commit phase can only promise that nothing further is
// committed and nothing is littered.
//
// The second commit is forced to fail deterministically by pre-creating a
// DIRECTORY at the client's final target path. THE ERROR CLASS IS NOT ASSERTED
// BEYOND NON-NIL, deliberately and on measured grounds: on darwin the rename
// returns "file exists" (fs.ErrExist / EEXIST), NOT EISDIR, so asserting a
// directory-specific class or matching on "Is a directory" would false-red on
// this platform. Renaming onto a plain path and onto an existing REGULAR file
// both succeed, so this forcing mechanism is specific to a directory target and
// cannot disturb the ordinary overwrite the happy path performs.
func TestInstall_StageBoth_CommitFailureDiscardsRemainingStaged(t *testing.T) {
	serverAsset, clientAsset, serverArchive, clientArchive, checksums, serverContent := stagedInstallFixture(t, false)

	srv := newReleaseServer(t, releaseStub{
		tag:             "v1.2.3",
		assetName:       serverAsset,
		archive:         serverArchive,
		clientAssetName: clientAsset,
		clientArchive:   clientArchive,
		checksums:       checksums,
	})
	pointHTTPClientAt(t, srv)
	withVersion(t, "v1.2.3")

	serverDir := t.TempDir()
	clientDir := t.TempDir()
	oldServer := []byte("OLD server bytes")
	serverPath := filepath.Join(serverDir, "knowledge-server")
	if err := os.WriteFile(serverPath, oldServer, 0o755); err != nil { //nolint:gosec // fixture binary needs the executable bit
		t.Fatalf("seed server: %v", err)
	}
	// The forcing mechanism: a DIRECTORY where the client binary must land.
	clientPath := filepath.Join(clientDir, "knowledge")
	if err := os.Mkdir(clientPath, 0o750); err != nil {
		t.Fatalf("pre-create the client target as a directory: %v", err)
	}
	withStubExecutable(t, clientPath)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	err := runInstallForTest(t, ctx, "linux", "amd64", serverDir)

	// ASSERTION 3: a non-nil error naming the binaries, so an operator can see
	// which committed and which did not. The error CLASS is deliberately not
	// asserted — see the doc comment.
	if err == nil {
		t.Fatalf("a failed commit must surface as an error")
	}
	msg := err.Error()
	for _, want := range []string{"knowledge-server", "knowledge"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the commit-failure error must name the binaries so a partial commit is reportable; %q omits %q", msg, want)
		}
	}

	// ASSERTION 4 (KNOWN-POSITIVE CONTROL): the SERVER really did commit, which
	// is what proves execution reached the commit phase rather than aborting
	// earlier for an unrelated reason. Without it every other assertion here
	// could pass against a run that never got there.
	gotServer, readErr := os.ReadFile(serverPath) //nolint:gosec // test fixture path under t.TempDir
	if readErr != nil {
		t.Fatalf("read server: %v", readErr)
	}
	if !bytes.Equal(gotServer, serverContent) {
		t.Fatalf("the server binary does not hold the NEW bytes, so this run never reached the commit phase and the assertions below gate nothing: got %q", gotServer)
	}

	// ASSERTION 6: nothing renamed the server back. A rollback would restore
	// the OLD bytes, which assertion 4 already forbids — stated separately
	// because the step forbids inventing a rollback and an implementer who
	// added one would still satisfy assertions 3 and 5.
	if bytes.Equal(gotServer, oldServer) {
		t.Errorf("the committed server binary was rolled back; a partial commit is REPORTED, never repaired — the old bytes are gone once a rename succeeds")
	}

	// ASSERTION 5: no staged temp file survives in EITHER destination. Not
	// vacuous: a failed rename leaves its source in place, so an implementation
	// that omitted the discard really does litter.
	for _, dir := range []string{serverDir, clientDir} {
		entries, dirErr := os.ReadDir(dir)
		if dirErr != nil {
			t.Fatalf("readdir %s: %v", dir, dirErr)
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "knowledge-server-install-") {
				t.Errorf("staged temp file left behind in %s: %s — every abort path must discard what it staged", dir, e.Name())
			}
		}
	}
}
