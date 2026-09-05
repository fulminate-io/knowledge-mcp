// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"strings"
	"testing"
)

// releaseClientExtractedBytes is the measured uncompressed size of the
// published v0.8.3 linux-arm64 client (2026-09-04). The installer's
// extracted-size cap was 200 MiB, sized for the server binary, and
// refused this archive with "extracted knowledge exceeds ...-byte cap",
// so `knowledge install` and the self-updater could never install the
// product's own release. This test pins that a member of the shipped
// client's size extracts, and that the bomb defense still refuses a
// member past the cap.
const releaseClientExtractedBytes = 293_707_992

func tarGzWithMember(t *testing.T, name string, size int64) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: size, Typeflag: tar.TypeReg}); err != nil {
		t.Fatalf("header: %v", err)
	}
	// Zero-filled body: compresses to almost nothing, so a multi-hundred-MiB
	// member costs the test only the extraction-side allocation.
	chunk := make([]byte, 1<<20)
	for remaining := size; remaining > 0; {
		n := min(int64(len(chunk)), remaining)
		if _, err := tw.Write(chunk[:n]); err != nil {
			t.Fatalf("write: %v", err)
		}
		remaining -= n
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return out.Bytes()
}

func TestExtractTarGz_AcceptsTheShippedClientSize(t *testing.T) {
	archive := tarGzWithMember(t, "knowledge", releaseClientExtractedBytes)
	bin, err := extractTarGz(archive, "knowledge")
	if err != nil {
		t.Fatalf("a member the size of the shipped client must extract, got: %v", err)
	}
	if int64(len(bin)) != releaseClientExtractedBytes {
		t.Fatalf("extracted %d bytes, want %d", len(bin), releaseClientExtractedBytes)
	}
}

// tarGzDeclaring emits a gzipped tar carrying ONE header that declares size and
// NO body for it.
//
// THAT IS THE POINT, NOT A SHORTCUT. The extractor refuses an over-cap member on
// the header's declared size before it reads a byte, so the body is never
// reached; writing one would cost the very gigabytes this shape exists to stop
// the suite from allocating. The tar writer is deliberately not closed — closing
// it demands the declared bytes — and the gzip writer is closed directly, so the
// stream is a valid header followed by nothing.
func tarGzDeclaring(t *testing.T, name string, declared int64) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: declared, Typeflag: tar.TypeReg}); err != nil {
		t.Fatalf("header: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return out.Bytes()
}

// TestExtractTarGz_RefusesADeclaredOversizeMemberWithoutReadingIt is the
// bomb-defense case, and it asserts the COST as well as the refusal.
//
// Before the declared-size leg this case allocated roughly four times the cap to
// say no — measured at 4.03 GiB once the cap reached 1 GiB — and paid it on every
// run of this package on every developer machine and every CI leg. The archive
// here carries a header declaring cap+1 and no body at all, so an extractor that
// still refused only after copying would fail: there are no bytes to copy.
func TestExtractTarGz_RefusesADeclaredOversizeMemberWithoutReadingIt(t *testing.T) {
	archive := tarGzDeclaring(t, "knowledge", maxExtractedBytes+1)
	if len(archive) > 1<<20 {
		t.Fatalf("the fixture must be tiny or it is not testing the cheap refusal; got %d bytes", len(archive))
	}
	_, err := extractTarGz(archive, "knowledge")
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("a member declaring more than maxExtractedBytes must be refused with the cap named, got: %v", err)
	}
	// The refusal must be the CAP's, not a truncated-stream complaint from the
	// missing body — otherwise this passes for the wrong reason.
	if strings.Contains(err.Error(), "tar copy") {
		t.Fatalf("the refusal came from reading the body, so the declared-size leg did not fire: %v", err)
	}
}

// zipWithMember builds a zip carrying one deflated member of the given size.
func zipWithMember(t *testing.T, name string, size int64) []byte {
	t.Helper()
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatalf("zip create: %v", err)
	}
	chunk := make([]byte, 1<<16)
	for remaining := size; remaining > 0; {
		n := min(int64(len(chunk)), remaining)
		if _, werr := w.Write(chunk[:n]); werr != nil {
			t.Fatalf("zip write: %v", werr)
		}
		remaining -= n
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return out.Bytes()
}

// zipDeclaring builds a zip whose entry DECLARES declared uncompressed bytes
// while carrying an empty stream, via CreateRaw — the zip twin of tarGzDeclaring
// and, unlike the tar side, a shape the format genuinely permits: a zip's sizes
// are central-directory metadata rather than a bound on the stream.
func zipDeclaring(t *testing.T, name string, declared uint64) []byte {
	t.Helper()
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	w, err := zw.CreateRaw(&zip.FileHeader{
		Name:               name,
		Method:             zip.Store,
		UncompressedSize64: declared,
		CompressedSize64:   0,
	})
	if err != nil {
		t.Fatalf("zip create raw: %v", err)
	}
	if _, err := w.Write(nil); err != nil {
		t.Fatalf("zip raw write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return out.Bytes()
}

// TestExtractZip_EnforcesTheExtractedCapInBothDirections drives extractZip's own
// copy of the extracted-size check, which no test reached before: a member under
// the cap extracts, and one declaring past it is refused.
//
// THE ACCEPT LEG IS WHAT MAKES THE REFUSAL MEAN ANYTHING. Without it, an
// extractZip that refused everything would satisfy the refusal leg alone.
func TestExtractZip_EnforcesTheExtractedCapInBothDirections(t *testing.T) {
	t.Run("a member under the cap extracts", func(t *testing.T) {
		const size = 1 << 16
		archive := zipWithMember(t, "knowledge.exe", size)
		bin, err := extractZip(archive, "knowledge.exe")
		if err != nil {
			t.Fatalf("a member well under the cap must extract, got: %v", err)
		}
		if len(bin) != size {
			t.Fatalf("extracted %d bytes, want %d", len(bin), size)
		}
	})

	t.Run("a member declaring past the cap is refused", func(t *testing.T) {
		archive := zipDeclaring(t, "knowledge.exe", uint64(maxExtractedBytes)+1)
		if len(archive) > 1<<20 {
			t.Fatalf("the fixture must be tiny or it is not testing the cheap refusal; got %d bytes", len(archive))
		}
		_, err := extractZip(archive, "knowledge.exe")
		if err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("an entry declaring more than maxExtractedBytes must be refused with the cap named, got: %v", err)
		}
		if strings.Contains(err.Error(), "zip copy") {
			t.Fatalf("the refusal came from reading the entry, so the declared-size leg did not fire: %v", err)
		}
	})
}
