// SPDX-License-Identifier: Apache-2.0

// install_stage.go — the two halves of the atomic binary write, split apart so a
// multi-binary install can stage EVERY binary before committing ANY of them.
//
// The single-binary write was already a stage phase followed by a commit phase;
// this file splits it at the seam that was always there. The reason the seam has
// to be reachable from outside is the dual-binary install: fetching, verifying
// and extracting one binary at a time and committing each as it lands leaves the
// destination half-swapped whenever a later asset fails — a new server beside an
// old client. The shell installer has not had that hole since its own two-phase
// rewrite, which extracts both into a staging directory and only then moves the
// two binaries in via adjacent renames; this brings the in-process path to the
// same guarantee.

package bootstrap

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// stagedBinary is a binary written to a temp file beside its final destination,
// verified and made executable, and awaiting only the rename that commits it.
type stagedBinary struct {
	// tmpPath is the staged file. It sits in the SAME directory as finalPath
	// because a rename across filesystems fails, and the client and the server
	// can legitimately live in different directories.
	tmpPath string
	// finalPath is where commitStaged will rename it.
	finalPath string
	// name is the on-disk basename, for operator-facing messages.
	name string
	// size is the staged payload's length, for the wrote-N-bytes line.
	size int
}

// stageBinary writes binBytes to a temp file in destDir, fsyncs and closes it,
// and makes it executable — everything the atomic write does EXCEPT the rename.
//
// The Sync MUST stay ahead of the Close, and the Close ahead of any commit: that
// ordering is the durability half of the atomic write, and a commit of bytes
// that never reached the disk is an atomic swap to garbage.
//
// A permission failure surfaces as fs.ErrPermission so the caller can translate
// it into the multi-line UX hint without parsing error text.
//
// finalBase is the binary basename to install; the +".exe" suffix is applied on
// Windows.
func stageBinary(destDir string, binBytes []byte, goos, finalBase string) (stagedBinary, error) {
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
		return stagedBinary{}, fmt.Errorf("create tempfile in %s: %w", destDir, err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }

	if _, err := tmp.Write(binBytes); err != nil {
		_ = tmp.Close()
		cleanup()
		return stagedBinary{}, fmt.Errorf("write tempfile %s: %w", tmpPath, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return stagedBinary{}, fmt.Errorf("fsync tempfile %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return stagedBinary{}, fmt.Errorf("close tempfile %s: %w", tmpPath, err)
	}
	if goos != "windows" {
		if err := os.Chmod(tmpPath, 0o755); err != nil { //nolint:gosec // executable bit required so the installed binary runs
			cleanup()
			return stagedBinary{}, fmt.Errorf("chmod tempfile %s: %w", tmpPath, err)
		}
	}
	return stagedBinary{tmpPath: tmpPath, finalPath: finalPath, name: finalName, size: len(binBytes)}, nil
}

// commitStaged renames a staged binary into its final place and returns that
// path.
//
// On Windows, move the old executable aside before publishing the staged one.
// Deleting or replacing an image still mapped during shutdown can be denied.
// Unix keeps its single rename and open-descriptor behavior unchanged.
func commitStaged(s stagedBinary, goos string) (string, error) {
	previous := ""
	if goos == "windows" {
		var err error
		previous, err = movePreviousExecutable(s.finalPath)
		if err != nil {
			return "", err
		}
	}
	if err := renameStagedExecutable(s.tmpPath, s.finalPath); err != nil {
		if previous != "" {
			if restoreErr := renameStagedExecutable(previous, s.finalPath); restoreErr != nil {
				return "", fmt.Errorf("publish %s; previous executable retained at %s: %w", s.finalPath, previous, errors.Join(err, fmt.Errorf("restore previous executable: %w", restoreErr)))
			}
			return "", fmt.Errorf("publish %s; restored previous executable: %w", s.finalPath, err)
		}
		return "", err
	}
	if previous != "" {
		if err := removePreviousExecutable(previous); err != nil && !errors.Is(err, fs.ErrNotExist) {
			// The new executable is published. Keep a still-locked recovery image
			// and name it so cleanup is explicit, rather than hiding the failure.
			slog.Warn("previous executable retained; remove after it is no longer in use", "path", previous, "error", err)
		}
	}
	return s.finalPath, nil
}

func movePreviousExecutable(path string) (string, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("existing executable %s is not a regular file", path)
	}
	previous := path + fmt.Sprintf(".previous-%d", time.Now().UnixNano())
	if err := os.Rename(path, previous); err != nil {
		return "", fmt.Errorf("move previous executable %s: %w", path, err)
	}
	return previous, nil
}

var removePreviousExecutable = os.Remove
var renameStagedExecutable = os.Rename

// discardStaged removes every staged temp file, best effort. Every abort path
// calls it, so no staged temp survives a failed install to be misattributed to
// the wrong binary later.
func discardStaged(staged []stagedBinary) {
	for _, s := range staged {
		_ = os.Remove(s.tmpPath)
	}
}
