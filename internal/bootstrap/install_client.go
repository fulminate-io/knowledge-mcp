// SPDX-License-Identifier: Apache-2.0

// install_client.go — dual-binary install glue for `knowledge
// install`. installBothBinaries drives the per-binary pipeline tail
// (installOneBinary) over a single already-fetched release: first the
// sibling knowledge-server, then the knowledge client self-update in
// place. Split from install.go for the 500-line cap.

package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// installBothBinaries installs the server then the client from a
// single already-fetched release. The server lands in the resolved
// install directory (sibling of the running binary, or --dest); the
// client is written over the running binary in place so a
// renamed/symlinked client is overwritten where it actually lives.
//
// Windows cannot atomically replace a running .exe (writeAtomic's
// remove-then-rename fails on the open file), so the client
// self-update is skipped there with a clear note — the server update
// still landed and the user re-downloads the client manually.
func installBothBinaries(ctx context.Context, rel *releaseResponse, goos, goarch, flagDest string) error {
	serverDir, err := resolveInstallDest(flagDest)
	if err != nil {
		return err
	}
	if err := installOneBinary(ctx, rel, goos, goarch, serverDir, "knowledge-server", "knowledge-server"); err != nil {
		return err
	}

	if goos == "windows" {
		fmt.Fprintln(os.Stdout, "knowledge install: client self-update unsupported on Windows; re-download manually")
		return nil
	}

	clientDir, clientName, err := resolveClientTarget()
	if err != nil {
		return err
	}
	return installOneBinary(ctx, rel, goos, goarch, clientDir, "knowledge", clientName)
}

// resolveClientTarget returns the directory and filename of the
// running client binary so `knowledge install` overwrites it in
// place — even when it was renamed or reached via a symlink.
// EvalSymlinks resolves a Homebrew-style symlink to its real Cellar
// location, matching resolveInstallDest / lookupSibling behavior.
func resolveClientTarget() (dir, name string, err error) {
	exe, err := getExecutable()
	if err != nil {
		return "", "", fmt.Errorf("resolve running client path: %w", err)
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}
	return filepath.Dir(exe), filepath.Base(exe), nil
}

// installOneBinary runs the per-binary tail of the install pipeline
// against an already-fetched release: resolve the asset + checksums
// URLs, download, verify SHA256, extract, and atomically write the
// binary into destDir. It aborts on the FIRST verification failure so
// nothing partial reaches disk.
//
// assetBase names the release asset and its inner file
// ("knowledge-server" or "knowledge"); writeName is the on-disk
// filename — equal to assetBase for the server, but the running
// binary's own basename for the client so a renamed client is
// overwritten in place rather than a stray "knowledge" landing beside
// it.
func installOneBinary(ctx context.Context, rel *releaseResponse, goos, goarch, destDir, assetBase, writeName string) error {
	asset := assetName(goos, goarch, assetBase)
	archiveURL, checksumsURL := resolveReleaseURLs(rel.TagName, asset)

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

	binBytes, err := extractArchive(archiveBytes, goos, assetBase)
	if err != nil {
		return err
	}

	absDestDir, _ := filepath.Abs(destDir)
	finalPath, err := writeAtomic(destDir, binBytes, goos, writeName)
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return fmt.Errorf("knowledge install: cannot write %s to %s: permission denied.\n  Retry as: sudo knowledge install\n  Or pick a writable directory on $PATH: knowledge install --dest ~/.local/bin", writeName, absDestDir)
		}
		return err
	}

	fmt.Fprintf(os.Stdout, "knowledge install: wrote %s (%d bytes, %s)\n", finalPath, len(binBytes), rel.TagName)
	return nil
}
