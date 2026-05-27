// SPDX-License-Identifier: Apache-2.0

package web

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fixtureFile describes a single entry in a synthetic tarball.
type fixtureFile struct {
	Path    string // path inside the tar (relative; the helper prepends a top-level dir)
	Content []byte
	IsDir   bool
}

// generateFixtureTarball builds a small synthetic tar.gz in memory with the
// shape:
//
//	repo-abc123/
//	  README.md
//	  pkg/
//	    foo.go    (10 LOC, 1 exported function)
//	    bar.go    (10 LOC, 1 exported function)
//	  cmd/
//	    main.go   (5 LOC)
//
// The top-level directory is "repo-abc123" — codeload.github.com tarballs
// always have a single top-level directory of the form
// "<repo>-<sha-or-ref>", and the unpacker is expected to strip one path
// component to find the repo contents.
func generateFixtureTarball(t *testing.T) []byte {
	t.Helper()
	files := []fixtureFile{
		{Path: "repo-abc123/README.md", Content: []byte("# fixture repo\n")},
		{Path: "repo-abc123/pkg/foo.go", Content: []byte(`package pkg

// Foo returns a small fixture string for testing.
func Foo() string {
	return "foo"
}
`)},
		{Path: "repo-abc123/pkg/bar.go", Content: []byte(`package pkg

// Bar returns a small fixture string for testing.
func Bar() string {
	return "bar"
}
`)},
		{Path: "repo-abc123/cmd/main.go", Content: []byte(`package main

func main() {
}
`)},
	}
	return buildTarGz(t, files)
}

// generateOversizeFixtureTarball builds the same shape as
// generateFixtureTarball plus one synthetic 200 MiB padding file. Used for
// the size-cap mid-stream test. The padding is all-zero bytes (compresses
// efficiently in gzip but expands during tar unpack — exactly what triggers
// the mid-stream cumulative-byte cap).
func generateOversizeFixtureTarball(t *testing.T) []byte {
	t.Helper()
	const padBytes = 200 << 20
	pad := make([]byte, padBytes)
	files := []fixtureFile{
		{Path: "repo-abc123/README.md", Content: []byte("# oversize fixture\n")},
		{Path: "repo-abc123/big.bin", Content: pad},
	}
	return buildTarGz(t, files)
}

// buildTarGz writes the provided fixture files into an in-memory gzipped
// tarball. Regular files only — no symlinks, devices, or fifos.
func buildTarGz(t *testing.T, files []fixtureFile) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	now := time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC)
	// Emit directories implicitly via parent paths; codeload tarballs
	// usually include them but the unpacker should be tolerant either way.
	dirs := map[string]bool{}
	for _, f := range files {
		if !f.IsDir {
			parts := strings.Split(f.Path, "/")
			for i := 1; i < len(parts); i++ {
				dir := strings.Join(parts[:i], "/") + "/"
				if dirs[dir] {
					continue
				}
				dirs[dir] = true
				if err := tw.WriteHeader(&tar.Header{
					Name:     dir,
					Typeflag: tar.TypeDir,
					Mode:     0o755,
					ModTime:  now,
				}); err != nil {
					t.Fatalf("tar dir %q: %v", dir, err)
				}
			}
		}
		hdr := &tar.Header{
			Name:    f.Path,
			Mode:    0o644,
			Size:    int64(len(f.Content)),
			ModTime: now,
		}
		if f.IsDir {
			hdr.Typeflag = tar.TypeDir
			hdr.Mode = 0o755
			hdr.Size = 0
		} else {
			hdr.Typeflag = tar.TypeReg
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar header %q: %v", f.Path, err)
		}
		if !f.IsDir {
			if _, err := tw.Write(f.Content); err != nil {
				t.Fatalf("tar write %q: %v", f.Path, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// fixtureCodeloadServer returns an httptest.Server that serves the provided
// tarball bytes at any path matching `/<owner>/<repo>/tar.gz/<ref>`.
//
// Callers point the materializer at this server's URL by overriding
// codeloadBaseURL via the package-level var (see github_materializer.go).
func fixtureCodeloadServer(t *testing.T, tarball []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Emulate codeload.github.com: any GET serves the tarball.
		if strings.Contains(r.URL.Path, "/tar.gz/") {
			w.Header().Set("Content-Type", "application/x-gzip")
			w.Header().Set("Content-Length", itoa(len(tarball)))
			_, _ = w.Write(tarball)
			return
		}
		http.NotFound(w, r)
	}))
	return srv
}

// itoa is a tiny helper to avoid pulling strconv into header values.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	negative := n < 0
	if negative {
		n = -n
	}
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if negative {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// buildTarballWithSymlink constructs an UNGZIPPED tar (no codeload
// top-level dir wrapper) containing one regular file plus one symlink
// entry. Used by the symlink-skip test which calls unpackTar directly.
func buildTarballWithSymlink(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	// We need to fake the codeload top-dir prefix because unpackTar
	// strips one path component before checking entries.
	if err := tw.WriteHeader(&tar.Header{
		Name:     "repo-abc/",
		Typeflag: tar.TypeDir,
		Mode:     0o755,
	}); err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{
		Name:     "repo-abc/real.txt",
		Typeflag: tar.TypeReg,
		Mode:     0o644,
		Size:     5,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{
		Name:     "repo-abc/symlink.txt",
		Typeflag: tar.TypeSymlink,
		Linkname: "real.txt",
		Mode:     0o644,
	}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// byteReader wraps a byte slice as an io.Reader. Used by unit tests that
// call unpackTar directly without going through gzip+http.
func byteReader(b []byte) io.Reader { return bytes.NewReader(b) }
