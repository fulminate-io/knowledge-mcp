// SPDX-License-Identifier: Apache-2.0

package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// withCodeloadBaseURL temporarily overrides codeloadBaseURL for a single
// test. Restored on cleanup so tests can run in any order.
func withCodeloadBaseURL(t *testing.T, base string) {
	t.Helper()
	prev := codeloadBaseURL
	codeloadBaseURL = base
	t.Cleanup(func() { codeloadBaseURL = prev })
}

// TestFetchCodeloadTarball_HappyPath verifies the fetcher unpacks the
// fixture tarball into a temp dir. The materializer is whole-repo only —
// info.Path is ignored by the fetcher; sub-path filtering used to live
// here but moved out when the dispatcher switched to whole-repo.
func TestFetchCodeloadTarball_HappyPath(t *testing.T) {
	tarball := generateFixtureTarball(t)
	srv := fixtureCodeloadServer(t, tarball)
	defer srv.Close()
	withCodeloadBaseURL(t, srv.URL)

	fc := newFetchClient("", 0)
	info := githubURLInfo{Owner: "owner", Repo: "repo", Ref: "main", Kind: kindTree}

	rootDir, cleanup, w, err := fetchCodeloadTarball(context.Background(), fc, info, 50<<20)
	if err != nil {
		t.Fatalf("fetchCodeloadTarball: %v", err)
	}
	if w != nil {
		t.Fatalf("unexpected warning: %+v", w)
	}
	if cleanup == nil {
		t.Fatalf("cleanup is nil")
	}
	defer cleanup()

	// Confirm the tarball contents are present (top-level dir was stripped).
	for _, rel := range []string{"README.md", "pkg/foo.go", "pkg/bar.go", "cmd/main.go"} {
		path := filepath.Join(rootDir, rel)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected file %q missing: %v", path, err)
		}
	}
}

// TestFetchCodeloadTarball_IgnoresInfoPath confirms that info.Path is
// IGNORED by the fetcher (whole-repo materialization). All files in the
// tarball are unpacked regardless of the URL's sub-path.
func TestFetchCodeloadTarball_IgnoresInfoPath(t *testing.T) {
	tarball := generateFixtureTarball(t)
	srv := fixtureCodeloadServer(t, tarball)
	defer srv.Close()
	withCodeloadBaseURL(t, srv.URL)

	fc := newFetchClient("", 0)
	// info.Path is set to "pkg" — fetcher should ignore it and unpack
	// the whole tarball, not just the pkg/ subtree.
	info := githubURLInfo{Owner: "owner", Repo: "repo", Ref: "main", Path: "pkg", Kind: kindTree}

	rootDir, cleanup, w, err := fetchCodeloadTarball(context.Background(), fc, info, 50<<20)
	if err != nil {
		t.Fatalf("fetchCodeloadTarball: %v", err)
	}
	if w != nil {
		t.Fatalf("unexpected warning: %+v", w)
	}
	defer cleanup()

	// Files OUTSIDE info.Path must still be present — proof info.Path
	// did not filter the unpack.
	if _, err := os.Stat(filepath.Join(rootDir, "cmd", "main.go")); err != nil {
		t.Errorf("cmd/main.go missing — info.Path should not filter unpack: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rootDir, "README.md")); err != nil {
		t.Errorf("README.md missing — info.Path should not filter unpack: %v", err)
	}
}

// TestFetchCodeloadTarball_PreReadSizeCap exercises the Content-Length
// pre-check path: when Content-Length declares > maxBytes, the fetcher
// returns a warning and reads zero body bytes.
func TestFetchCodeloadTarball_PreReadSizeCap(t *testing.T) {
	bodyRead := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "999999999")
		w.WriteHeader(http.StatusOK)
		// Don't write a body — if the test fetcher tries to read, it
		// hangs and we'll see it via the timeout, not via this flag.
		bodyRead = true
		_ = bodyRead
	}))
	defer srv.Close()
	withCodeloadBaseURL(t, srv.URL)

	fc := newFetchClient("", 0)
	info := githubURLInfo{Owner: "owner", Repo: "repo", Ref: "main", Kind: kindTree}

	_, _, w, err := fetchCodeloadTarball(context.Background(), fc, info, 1<<20)
	if err != nil {
		t.Fatalf("fetchCodeloadTarball: %v", err)
	}
	if w == nil {
		t.Fatalf("expected size-cap warning, got nil")
	}
	if w.Reason != "size_cap_pre_read" {
		t.Errorf("Reason=%q want=size_cap_pre_read", w.Reason)
	}
	if w.BytesSeen != 0 {
		t.Errorf("BytesSeen=%d want=0", w.BytesSeen)
	}
	if w.Cap != 1<<20 {
		t.Errorf("Cap=%d want=%d", w.Cap, 1<<20)
	}
	if w.Owner != "owner" || w.Repo != "repo" || w.Ref != "main" {
		t.Errorf("warning identifiers wrong: %+v", w)
	}
}

// TestFetchCodeloadTarball_MidStreamSizeCap exercises the cumulative-byte
// counter path: when Content-Length is missing, the limitedReader trips
// once cumulative reads exceed the cap during gunzip / tar streaming.
func TestFetchCodeloadTarball_MidStreamSizeCap(t *testing.T) {
	tarball := generateOversizeFixtureTarball(t)
	// Serve without Content-Length. We use httptest with a manual
	// Hijack-like flush so net/http omits Content-Length.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-gzip")
		w.Header().Set("Transfer-Encoding", "chunked")
		_, _ = w.Write(tarball)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer srv.Close()
	withCodeloadBaseURL(t, srv.URL)

	fc := newFetchClient("", 0)
	info := githubURLInfo{Owner: "owner", Repo: "repo", Ref: "main", Kind: kindTree}

	// Cap at 1 MiB — well below the 200 MiB padding file.
	_, _, w, err := fetchCodeloadTarball(context.Background(), fc, info, 1<<20)
	if err != nil {
		t.Fatalf("fetchCodeloadTarball: %v", err)
	}
	if w == nil {
		t.Fatalf("expected size-cap warning, got nil")
	}
	if w.Reason != "size_cap_mid_stream" {
		t.Errorf("Reason=%q want=size_cap_mid_stream", w.Reason)
	}
	if w.Owner != "owner" || w.Repo != "repo" || w.Ref != "main" {
		t.Errorf("warning identifiers wrong: %+v", w)
	}
}

// TestUnpackTar_PathTraversal verifies that a tar entry with ".." or an
// absolute path is rejected.
func TestUnpackTar_PathTraversal(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		{"normal", "pkg/foo.go", false},
		{"dot_dot", "../etc/passwd", true},
		{"absolute", "/etc/passwd", true},
		{"embedded_dot_dot", "pkg/../../etc/passwd", true},
		{"trailing_slash", "pkg/", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isUnsafeTarPath(tt.path)
			if got != tt.want {
				t.Errorf("isUnsafeTarPath(%q)=%v want=%v", tt.path, got, tt.want)
			}
		})
	}
}

// TestStripCodeloadTopDir confirms the leading "<repo>-<sha>/" prefix is
// dropped consistently.
func TestStripCodeloadTopDir(t *testing.T) {
	tests := map[string]string{
		"repo-abc123/README.md":   "README.md",
		"repo-abc123/pkg/foo.go":  "pkg/foo.go",
		"repo-abc123/":            "",
		"repo-abc123":             "",
		"./repo-abc123/README.md": "README.md",
	}
	for in, want := range tests {
		got := stripCodeloadTopDir(in)
		if got != want {
			t.Errorf("stripCodeloadTopDir(%q)=%q want=%q", in, got, want)
		}
	}
}

// TestUnpackTar_SkipsSymlinks builds a tarball with a symlink entry and
// confirms it is silently skipped while regular files are extracted.
func TestUnpackTar_SkipsSymlinks(t *testing.T) {
	// Build a tarball with a symlink. We can't use generateFixtureTarball
	// (regular-files-only) — construct inline.
	tarball := buildTarballWithSymlink(t)

	tmpDir := t.TempDir()
	w := unpackTar(byteReader(tarball), tmpDir)
	if w != nil {
		t.Fatalf("unexpected warning: %+v", w)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "real.txt")); err != nil {
		t.Errorf("regular file missing: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(tmpDir, "symlink.txt")); err == nil {
		t.Errorf("symlink unexpectedly present (should be silently skipped)")
	}
}
