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
// against tar/zip bombs. The cap was 200 MiB, sized for the server
// binary; the CLIENT binary is ~294 MB uncompressed (v0.8.3
// linux-arm64, measured 2026-09-04: tree-sitter grammars and embedded
// assets dominate), so that cap refused the product's own release and
// `knowledge install` plus the self-updater could never succeed. 1 GiB
// keeps the bomb defense bounded while clearing every shipped binary
// with room to grow.
const maxExtractedBytes = 1 << 30

// declaredBufCap turns an archive entry's DECLARED size into a buffer capacity.
//
// It is deliberately not just the declared size. A declaration is attacker-
// controlled, so it is used only as a hint and only within the cap the callers
// have already enforced: anything outside (0, maxExtractedBytes] pre-allocates
// nothing and the buffer grows as it always did. That keeps a bogus-but-small
// declaration from turning into a large allocation on a stream that never
// delivers the bytes, while the honest case — the shipped 294 MB client, whose
// header tells the truth — lands in a single allocation instead of a doubling
// ladder that overshoots to roughly twice the payload.
func declaredBufCap(declared int64) int {
	if declared <= 0 || declared > maxExtractedBytes {
		return 0
	}
	return int(declared)
}

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
		// THE DECLARED SIZE IS CHECKED BEFORE A BYTE IS COPIED, and that
		// ordering is the whole defense rather than a nicety. The check used to
		// run only AFTER io.CopyN had filled a growing buffer with cap+1 bytes,
		// so refusing one hostile archive cost roughly four times the cap in
		// resident memory — measured at 4.03 GiB once the cap reached 1 GiB,
		// against 545 MiB at the old 200 MiB cap. The tar header states the
		// member's size, so an oversize member can be refused for nothing.
		if hdr.Size > maxExtractedBytes {
			return nil, fmt.Errorf("extracted %s exceeds %d-byte cap", hdr.Name, maxExtractedBytes)
		}
		// Pre-sized from the declared size, bounded by the cap: the legitimate
		// 294 MB client lands in one allocation instead of a doubling ladder.
		buf := bytes.NewBuffer(make([]byte, 0, declaredBufCap(hdr.Size)))
		if _, err := io.CopyN(buf, tr, maxExtractedBytes+1); err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("tar copy %s: %w", hdr.Name, err)
		}
		// BELT AND BRACES, and its zip twin below is the same — an earlier revision
		// of this comment claimed the zip side was load-bearing and that claim was
		// false. MEASURED, not remembered, at the go and toolchain directives
		// go.mod carries: a tar member declaring 512
		// bytes with 512 more spliced in behind it delivers exactly 512 with a nil
		// error, so a header that understates its body cannot deliver more than it
		// declared. With the declared-size pre-check above refusing anything over
		// the cap, this length check cannot fire on any archive the stdlib parses.
		//
		// IT IS KEPT ANYWAY, and the reason is honest rather than defensive
		// hand-waving: it is a second leg against a future reader that stops
		// enforcing the declared size, at the cost of one integer comparison. Do
		// not write a test for it — no input the stdlib accepts reaches it.
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
		// Declared size first, for the reason the tar twin states: refusing an
		// oversize entry must not first allocate the cap to find out.
		if f.UncompressedSize64 > uint64(maxExtractedBytes) {
			return nil, fmt.Errorf("extracted %s exceeds %d-byte cap", f.Name, maxExtractedBytes)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("zip open %s: %w", f.Name, err)
		}
		//nolint:gosec // G115: the conversion is guarded by the cap check above.
		buf := bytes.NewBuffer(make([]byte, 0, declaredBufCap(int64(f.UncompressedSize64))))
		if _, err := io.CopyN(buf, rc, maxExtractedBytes+1); err != nil && !errors.Is(err, io.EOF) {
			_ = rc.Close()
			return nil, fmt.Errorf("zip copy %s: %w", f.Name, err)
		}
		_ = rc.Close()
		// BELT AND BRACES, exactly like the tar twin above, and NOT the reachable
		// arm an earlier revision of this comment claimed it was. That revision
		// said a zip entry may understate UncompressedSize64 while its stream
		// yields the real larger bytes, and that this check was the only thing
		// standing between a lying entry and an unbounded read. Both sentences
		// were wrong.
		//
		// MEASURED, not remembered: archive/zip's checksumReader.Read accumulates
		// nread and returns ErrFormat the moment it passes UncompressedSize64
		// (reader.go:302-304, the `if r.nread > r.f.UncompressedSize64` arm). A
		// probe with a same-run known positive shows an entry declaring 4096 reads
		// 4096 clean, while the same entry with a 4097-byte stream returns
		// "zip: not a valid zip file".
		//
		// PRECISELY: the lying entry yields its DECLARED bytes and only then
		// errors — it is not stopped before the first byte. Nothing reaches a
		// caller regardless, because io.CopyN above returns that non-EOF error and
		// this function discards the buffer. Stated exactly because the comment
		// this one replaced was wrong in this same paragraph, and an overstatement
		// here would be the same failure one revision later.
		//
		// THE ARM IS BYTE-IDENTICAL at go1.26.4 and go1.26.5 — what go.mod carries
		// as its go directive and its toolchain directive — so the claim holds
		// whichever of the two builds this.
		//
		// So a lying entry is refused by the STDLIB, before this check, and with
		// the declared-size pre-check above refusing anything over the cap, this
		// length check cannot fire on any archive the stdlib parses.
		//
		// KEPT for the reason the tar twin states — a second leg against a future
		// reader that stops enforcing the declared size — and untested for the
		// reason that follows from the measurement: no input the stdlib accepts
		// reaches it.
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
