// SPDX-License-Identifier: Apache-2.0

// install_client.go — dual-binary install glue for `knowledge install`.
// installBothBinaries drives a single already-fetched release through three
// explicit phases over both binaries — fetch all, stage all, commit all — so a
// failure anywhere before the first commit leaves the destination byte-untouched
// rather than half-swapped. Split from install.go for the 500-line cap.

package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// binaryTarget names one binary to install: which release asset carries it,
// which directory it lands in, and what it is called on disk.
//
// writeName is the on-disk filename — equal to assetBase for the server, but the
// running binary's own basename for the client, so a renamed client is
// overwritten in place rather than a stray "knowledge" landing beside it.
type binaryTarget struct {
	assetBase string
	destDir   string
	writeName string
}

// installBothBinaries installs the server then the client from a single
// already-fetched release, in three phases over the whole set.
//
// PHASE FETCH downloads, verifies and extracts EVERY binary. PHASE STAGE writes
// each payload to a temp file beside its destination. PHASE COMMIT renames them
// in turn. Nothing is committed until everything has been fetched and staged, so
// a network failure, a checksum mismatch or a malformed archive — the failures
// that take seconds to minutes and are overwhelmingly the ones that happen —
// cannot land between two commits and leave a new server beside an old client.
//
// CRASH WINDOW, disclosed rather than hidden: the commit phase performs two
// renames with no cross-file atomicity, so a power loss precisely between them
// still leaves a mismatched pair. That residue is inherent to committing two
// files; the renames are adjacent with no I/O between them.
//
// A COMMIT FAILURE IS REPORTED, NEVER REPAIRED. Once a rename has succeeded the
// old bytes are gone and there is nothing to roll back to, so the honest
// behavior is a loud error naming which binary committed and which did not,
// plus disposal of everything still staged.
//
// Windows cannot atomically replace a running .exe, so the client self-update is
// skipped there with a clear note — the server update still lands and the user
// re-downloads the client manually.
func installBothBinaries(ctx context.Context, rel *releaseResponse, goos, goarch, flagDest string) error {
	serverDir, err := resolveInstallDest(flagDest)
	if err != nil {
		return err
	}
	targets := []binaryTarget{
		{assetBase: "knowledge-server", destDir: serverDir, writeName: "knowledge-server"},
	}
	if goos == "windows" {
		fmt.Fprintln(os.Stdout, "knowledge install: client self-update unsupported on Windows; re-download manually")
	} else {
		clientDir, clientName, cerr := resolveClientTarget()
		if cerr != nil {
			return cerr
		}
		targets = append(targets, binaryTarget{assetBase: "knowledge", destDir: clientDir, writeName: clientName})
	}

	// PHASE FETCH — nothing has touched the destination yet.
	payloads := make([][]byte, len(targets))
	for i, tgt := range targets {
		binBytes, ferr := fetchAndExtractOne(ctx, rel, goos, goarch, tgt.assetBase)
		if ferr != nil {
			return ferr
		}
		payloads[i] = binBytes
	}

	// PHASE STAGE — temp files only; the destination is still byte-untouched.
	staged := make([]stagedBinary, 0, len(targets))
	for i, tgt := range targets {
		s, serr := stageBinary(tgt.destDir, payloads[i], goos, tgt.writeName)
		if serr != nil {
			discardStaged(staged)
			if errors.Is(serr, fs.ErrPermission) {
				absDestDir, _ := filepath.Abs(tgt.destDir)
				return fmt.Errorf("knowledge install: cannot write %s to %s: permission denied.\n  Retry as: sudo knowledge install\n  Or pick a writable directory on $PATH: knowledge install --dest ~/.local/bin", tgt.writeName, absDestDir)
			}
			return serr
		}
		staged = append(staged, s)
	}

	// PHASE COMMIT — from here a failure is reported, not repaired.
	committed := make([]string, 0, len(staged))
	for i, s := range staged {
		finalPath, cerr := commitStaged(s, goos)
		if cerr != nil {
			discardStaged(staged[i:])
			return fmt.Errorf("knowledge install: committed %v but could not commit %s: %w — the installation is now a mixed pair; re-run `knowledge install` once the cause is resolved",
				committed, s.name, cerr)
		}
		committed = append(committed, s.name)
		fmt.Fprintf(os.Stdout, "knowledge install: wrote %s (%d bytes, %s)\n", finalPath, s.size, rel.TagName)
	}
	return nil
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

// fetchAndExtractOne runs the FETCH half of the per-binary pipeline against an
// already-fetched release: resolve the asset + checksums URLs, download, verify
// SHA256, and extract. It touches no destination directory at all — that is what
// makes it safe to run for every binary before any of them is staged, and it
// aborts on the FIRST verification failure so nothing unverified proceeds.
//
// assetBase names the release asset and its inner file ("knowledge-server" or
// "knowledge").
func fetchAndExtractOne(ctx context.Context, rel *releaseResponse, goos, goarch, assetBase string) ([]byte, error) {
	asset := assetName(goos, goarch, assetBase)
	archiveURL, checksumsURL := resolveReleaseURLs(rel.TagName, asset)

	archiveBytes, err := downloadAsset(ctx, archiveURL)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", asset, err)
	}
	checksumsBytes, err := downloadAsset(ctx, checksumsURL)
	if err != nil {
		return nil, fmt.Errorf("download checksums.txt: %w", err)
	}

	checksums := parseChecksums(checksumsBytes)
	expected, ok := checksums[asset]
	if !ok {
		return nil, fmt.Errorf("checksums.txt missing entry for %s", asset)
	}
	if err := verifyChecksum(archiveBytes, expected, asset); err != nil {
		return nil, err
	}

	return extractArchive(archiveBytes, goos, assetBase)
}
