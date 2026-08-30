// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// withHTTPClient swaps install_http.go's package-level httpClient
// for the duration of the test, restoring on Cleanup. Tests point
// it at an httptest.Server so install runs are hermetic — no live
// api.github.com calls.
func withHTTPClient(t *testing.T, c *http.Client) {
	t.Helper()
	prev := httpClient
	httpClient = c
	t.Cleanup(func() { httpClient = prev })
}

// withVersion overrides bootstrap.Version (the running knowledge
// binary's version) and restores on Cleanup. Tests use this to
// drive resolveReleaseTag — a dev build ("dev", or any "dev-"-prefixed
// local stamp) → /releases/latest, anything else →
// /releases/tags/<version>.
func withVersion(t *testing.T, v string) {
	t.Helper()
	prev := Version
	Version = v
	t.Cleanup(func() { Version = prev })
}

// buildTarGz constructs a tar.gz containing the supplied
// name→contents map. Mode is 0o755 on every entry so the
// extractor's permission expectations match the real release
// pipeline.
func buildTarGz(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		hdr := &tar.Header{
			Name:     name,
			Mode:     0o755,
			Size:     int64(len(content)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar header: %v", err)
		}
		if _, err := tw.Write(content); err != nil {
			t.Fatalf("tar body: %v", err)
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

// buildZip constructs a zip containing the supplied name→contents
// map. Mirrors the release pipeline's 7z output for windows.
func buildZip(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write(content); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

// releaseStub captures the inputs needed to spin up an httptest
// release-API mux that newReleaseServer exposes.
type releaseStub struct {
	tag       string
	assetName string
	archive   []byte
	checksums []byte
	// clientAssetName / clientArchive optionally add a SECOND release
	// asset (the knowledge client) so dual-binary install tests can
	// serve both binaries from one release. Left zero for the
	// server-only tests. checksums must cover both assets when set.
	clientAssetName string
	clientArchive   []byte
	// reportedTag, when set, is the tag_name the release JSON reports —
	// decoupled from the request path `tag` so downgrade-guard tests can
	// simulate a resolved release LOWER than the requested/installed one.
	// Defaults to tag.
	reportedTag string
	notFound    bool // when true /releases/tags/<tag> returns 404
	respondErr  bool // when true return 500 on any request (mismatch UX)
}

// newReleaseServer spins up an httptest.Server that serves the
// GitHub releases JSON shape for the given stub. /releases/latest
// resolves the same tag. Asset URLs point back at this same server
// under /dl/<name> so install_http.go's downloadAsset path is
// exercised end-to-end without leaving the test process.
func newReleaseServer(t *testing.T, s releaseStub) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var srv *httptest.Server
	releaseJSON := func() []byte {
		assets := []releaseAsset{
			{Name: s.assetName, BrowserDownloadURL: srv.URL + "/dl/" + s.assetName},
			{Name: "checksums.txt", BrowserDownloadURL: srv.URL + "/dl/checksums.txt"},
		}
		if s.clientAssetName != "" {
			assets = append(assets, releaseAsset{Name: s.clientAssetName, BrowserDownloadURL: srv.URL + "/dl/" + s.clientAssetName})
		}
		reportedTag := s.tag
		if s.reportedTag != "" {
			reportedTag = s.reportedTag
		}
		body, err := json.Marshal(releaseResponse{TagName: reportedTag, Assets: assets})
		if err != nil {
			t.Fatalf("marshal release: %v", err)
		}
		return body
	}
	tagsPath := "/repos/fulminate-io/knowledge-mcp/releases/tags/" + s.tag
	latestPath := "/repos/fulminate-io/knowledge-mcp/releases/latest"
	mux.HandleFunc(tagsPath, func(w http.ResponseWriter, r *http.Request) {
		if s.notFound {
			http.NotFound(w, r)
			return
		}
		if s.respondErr {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(releaseJSON())
	})
	mux.HandleFunc(latestPath, func(w http.ResponseWriter, r *http.Request) {
		if s.notFound {
			http.NotFound(w, r)
			return
		}
		if s.respondErr {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(releaseJSON())
	})
	// Assets are served at <tag>/<asset> to match resolveReleaseURLs'
	// build-time construction (releaseBaseURL + "/" + tag + "/" + asset);
	// pointHTTPClientAt sets releaseBaseURL = srv.URL.
	mux.HandleFunc("/"+s.tag+"/"+s.assetName, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(s.archive)
	})
	if s.clientAssetName != "" {
		mux.HandleFunc("/"+s.tag+"/"+s.clientAssetName, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(s.clientArchive)
		})
	}
	mux.HandleFunc("/"+s.tag+"/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(s.checksums)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })
	return srv
}

// withGithubBaseURL is a no-op shim used to keep test files
// self-documenting. The base URL is a parameter on fetchRelease;
// tests pass the httptest.Server URL directly when calling
// fetchRelease/downloadAsset, but runInstall hardcodes
// githubAPIBaseURL. Tests that exercise runInstall point httpClient
// at a transport that rewrites the api.github.com URL onto the
// httptest server.
type rewritingTransport struct {
	target string
	base   http.RoundTripper
}

func (r *rewritingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if rest, ok := strings.CutPrefix(req.URL.String(), githubAPIBaseURL); ok {
		newURL := r.target + rest
		req2 := req.Clone(req.Context())
		var err error
		req2.URL, err = req2.URL.Parse(newURL)
		if err != nil {
			return nil, err
		}
		req2.Host = ""
		return r.base.RoundTrip(req2)
	}
	return r.base.RoundTrip(req)
}

// pointHTTPClientAt installs a transport that rewrites the
// production github base URL onto the supplied httptest server.
// Combined with newReleaseServer this lets the full runInstall
// pipeline (which uses the hardcoded githubAPIBaseURL) hit a local
// hermetic server.
func pointHTTPClientAt(t *testing.T, srv *httptest.Server) {
	t.Helper()
	withHTTPClient(t, &http.Client{
		Timeout:   10 * time.Second,
		Transport: &rewritingTransport{target: srv.URL, base: http.DefaultTransport},
	})
	// The API base is reached via the rewriting transport (fetchRelease
	// builds githubAPIBaseURL URLs); asset downloads are constructed from
	// releaseBaseURL, so point it straight at the test server.
	prev := releaseBaseURL
	releaseBaseURL = srv.URL
	t.Cleanup(func() { releaseBaseURL = prev })
}

// sha256Hex returns the hex-encoded sha256 digest of data.
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// makeChecksums returns sha256sum text-mode formatted bytes for
// the supplied name→bytes map.
func makeChecksums(files map[string][]byte) []byte {
	var buf bytes.Buffer
	for name, content := range files {
		fmt.Fprintf(&buf, "%s  %s\n", sha256Hex(content), name)
	}
	return buf.Bytes()
}

// withStubInstalledServer writes a fake knowledge-server shell
// script at tmpdir/knowledge-server (or .exe on windows) that
// echoes the supplied version line, chmods it executable, and
// stubs getExecutable to point at a sibling running-binary path so
// findServerBinary's sibling lookup resolves to our stub.
func withStubInstalledServer(t *testing.T, dir, version string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stub not portable to windows; --check exec.Command path covered manually")
	}
	stubName := "knowledge-server"
	stubPath := filepath.Join(dir, stubName)
	script := "#!/bin/sh\necho " + version + "\n"
	if err := os.WriteFile(stubPath, []byte(script), 0o755); err != nil { //nolint:gosec // executable shell-script test fixture
		t.Fatalf("write stub server: %v", err)
	}
	stdioStub := filepath.Join(dir, "stdio_stub")
	if err := os.WriteFile(stdioStub, []byte("ignored"), 0o600); err != nil {
		t.Fatalf("write stdio stub: %v", err)
	}
	withStubExecutable(t, stdioStub)
	withPATH(t, "")
}

// captureStdout swaps os.Stdout for an os.Pipe for the duration
// of fn, returning whatever was written. Used by --check tests
// asserting human-readable output.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	prev := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	done := make(chan string)
	go func() {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r)
		done <- buf.String()
	}()
	fn()
	_ = w.Close()
	os.Stdout = prev
	return <-done
}
