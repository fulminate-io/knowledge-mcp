// SPDX-License-Identifier: Apache-2.0

// install_archive.go — stdlib-only tar.gz + zip extraction for
// `knowledge install`. Split from install.go for the 500-line cap.
//
// Invariant: each archive must contain exactly one regular file
// named knowledge-server (or knowledge-server.exe on Windows) with
// no leading directory — matches the release pipeline's archive
// shape verbatim. Anything else is rejected before bytes ever reach
// disk.

package bootstrap

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
)

// maxExtractedBytes bounds the decompressed binary size to defend
// against tar/zip bombs. 200 MiB is well above any plausible server
// binary and well below memory-exhausting territory.
const maxExtractedBytes = 200 << 20

// extractArchive decompresses archiveBytes and returns the bytes of
// the single expected entry: binBase (or binBase+".exe" on Windows),
// where binBase is "knowledge-server" or "knowledge".
// Returns an error when the archive has zero, multiple, or
// misnamed regular files — there is no fallback / heuristic
// matching, the release pipeline produces exactly-one-file archives
// and anything else means the asset is wrong.
func extractArchive(archiveBytes []byte, goos, binBase string) ([]byte, error) {
	want := binBase
	if goos == "windows" {
		want = binBase + ".exe"
	}
	if goos == "windows" {
		return extractZip(archiveBytes, want)
	}
	return extractTarGz(archiveBytes, want)
}

func extractTarGz(archiveBytes []byte, want string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archiveBytes))
	if err != nil {
		return nil, fmt.Errorf("gzip open: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	var bin []byte
	count := 0
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("tar read: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		count++
		if count > 1 {
			return nil, fmt.Errorf("archive contains %d regular files; expected exactly 1 (%s)", count, want)
		}
		if hdr.Name != want {
			return nil, fmt.Errorf("archive entry %q does not match expected %q", hdr.Name, want)
		}
		var buf bytes.Buffer
		if _, err := io.CopyN(&buf, tr, maxExtractedBytes+1); err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("tar copy %s: %w", hdr.Name, err)
		}
		if int64(buf.Len()) > maxExtractedBytes {
			return nil, fmt.Errorf("extracted %s exceeds %d-byte cap", hdr.Name, maxExtractedBytes)
		}
		bin = buf.Bytes()
	}
	if count == 0 {
		return nil, fmt.Errorf("archive contains 0 regular files; expected exactly 1 (%s)", want)
	}
	return bin, nil
}

func extractZip(archiveBytes []byte, want string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(archiveBytes), int64(len(archiveBytes)))
	if err != nil {
		return nil, fmt.Errorf("zip open: %w", err)
	}
	var bin []byte
	count := 0
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		count++
		if count > 1 {
			return nil, fmt.Errorf("archive contains %d regular files; expected exactly 1 (%s)", count, want)
		}
		if f.Name != want {
			return nil, fmt.Errorf("archive entry %q does not match expected %q", f.Name, want)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("zip open %s: %w", f.Name, err)
		}
		var buf bytes.Buffer
		if _, err := io.CopyN(&buf, rc, maxExtractedBytes+1); err != nil && !errors.Is(err, io.EOF) {
			_ = rc.Close()
			return nil, fmt.Errorf("zip copy %s: %w", f.Name, err)
		}
		_ = rc.Close()
		if int64(buf.Len()) > maxExtractedBytes {
			return nil, fmt.Errorf("extracted %s exceeds %d-byte cap", f.Name, maxExtractedBytes)
		}
		bin = buf.Bytes()
	}
	if count == 0 {
		return nil, fmt.Errorf("archive contains 0 regular files; expected exactly 1 (%s)", want)
	}
	return bin, nil
}
