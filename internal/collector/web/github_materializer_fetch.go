// SPDX-License-Identifier: Apache-2.0

package web

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// codeloadBaseURL is the base URL for codeload tarball fetches. It is a
// package-level var so tests can point it at an httptest.Server. Tarball
// fetches are the ONLY remote read path the materializer uses — single-file
// raw.githubusercontent fetches were removed when the design moved to
// always-whole-repo materialization (one fetch per (owner, repo, ref)).
var codeloadBaseURL = "https://codeload.github.com"

// errSizeExceeded is returned by limitedReader when cumulative read bytes
// have surpassed the configured cap. The fetcher converts this into a
// materializerWarning before returning to the caller.
var errSizeExceeded = errors.New("github_materializer: download exceeded size cap")

// materializerWarning describes why a github URL was NOT materialized
// (size cap, transport error). Emitted as a NodeDocument warning node by
// the dispatcher when present, with the metadata keys recorded here.
type materializerWarning struct {
	Reason string // "size_cap_pre_read" | "size_cap_mid_stream" | "transport"
	// URL is the CODELOAD TARBALL URL that was being fetched when the
	// warning was raised — codeloadBaseURL/<owner>/<repo>/tar.gz/<ref>, built
	// at fetchCodeloadTarball and passed to every site that sets this field.
	// It is NOT the original github URL the crawl was given; that address
	// reaches the emitted warning node separately, as its uri.
	URL       string
	BytesSeen int64
	Cap       int64
	Owner     string
	Repo      string
	Ref       string
	Detail    string // optional extra detail (transport error message)
}

// limitedReader is an io.Reader that returns errSizeExceeded once cumulative
// read bytes pass N. Differs from io.LimitedReader by signaling overflow
// instead of EOF, so the caller can distinguish "stream ended" from "stream
// hit the cap".
type limitedReader struct {
	R       io.Reader
	N       int64 // cap; remaining capacity decremented on each read
	BytesIn int64 // cumulative bytes successfully returned to the caller
}

// Read implements io.Reader. Once cumulative reads exceed the cap, returns
// errSizeExceeded. Otherwise delegates to the wrapped reader.
func (lr *limitedReader) Read(p []byte) (int, error) {
	if lr.N <= 0 {
		return 0, errSizeExceeded
	}
	if int64(len(p)) > lr.N {
		p = p[:lr.N]
	}
	n, err := lr.R.Read(p)
	lr.N -= int64(n)
	lr.BytesIn += int64(n)
	if err == io.EOF {
		return n, io.EOF
	}
	if lr.N <= 0 && err == nil {
		// Read more to confirm we're at EOF or overflowing.
		var probe [1]byte
		extra, perr := lr.R.Read(probe[:])
		if extra > 0 {
			return n, errSizeExceeded
		}
		if perr == io.EOF {
			return n, io.EOF
		}
	}
	return n, err
}

// fetchRaw performs a single GET against url with the same User-Agent /
// timeout / redirect policy the rest of the web collector uses. Unlike
// fetchClient.fetch, which reads a page body to EOF into memory, fetchRaw
// returns the response so the caller can STREAM the body through
// limitedReader against the materializer's own MaxDownloadBytes budget —
// a budget that REFUSES loudly (errSizeExceeded plus a warning node, never
// a partial repo) rather than truncating.
//
// On Content-Length > maxBytes, returns a non-nil warning + nil
// io.ReadCloser (the response is consumed/closed before returning so no
// body bytes are ever read). Otherwise returns the wrapped body and nil
// warning; caller is responsible for closing the reader.
//
// maxBytes <= 0 is treated as "no cap" (the materializer interprets the
// crawl-options -1 sentinel by passing math.MaxInt64 here).
func fetchRaw(ctx context.Context, fc *fetchClient, rawURL string, maxBytes int64) (
	body io.ReadCloser, warning *materializerWarning, err error,
) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("User-Agent", fc.userAgent)
	req.Header.Set("Accept", "*/*")

	resp, err := fc.httpClient.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, nil, err
		}
		return nil, nil, fmt.Errorf("http get %q: %w", rawURL, err)
	}
	if resp.StatusCode/100 != 2 {
		_ = resp.Body.Close()
		return nil, nil, fmt.Errorf("http get %q: status %d", rawURL, resp.StatusCode)
	}

	// Content-Length pre-check. If declared and over the cap, abort
	// before reading any body bytes.
	if maxBytes > 0 {
		if cl := resp.Header.Get("Content-Length"); cl != "" {
			if n, perr := strconv.ParseInt(cl, 10, 64); perr == nil && n > maxBytes {
				_ = resp.Body.Close()
				return nil, &materializerWarning{
					Reason:    "size_cap_pre_read",
					URL:       rawURL,
					BytesSeen: 0,
					Cap:       maxBytes,
				}, nil
			}
		}
	}

	if maxBytes <= 0 {
		return resp.Body, nil, nil
	}
	return &readCloserLimited{lr: &limitedReader{R: resp.Body, N: maxBytes}, c: resp.Body}, nil, nil
}

// readCloserLimited adapts a limitedReader + the underlying io.Closer into
// a single io.ReadCloser so callers can defer Close() without juggling
// both halves.
type readCloserLimited struct {
	lr *limitedReader
	c  io.Closer
}

func (r *readCloserLimited) Read(p []byte) (int, error) { return r.lr.Read(p) }
func (r *readCloserLimited) Close() error               { return r.c.Close() }
func (r *readCloserLimited) bytesIn() int64             { return r.lr.BytesIn }

// fetchCodeloadTarball downloads the codeload tarball for the (owner,
// repo, ref) triple, streams it through gzip + tar, and unpacks regular
// files into a freshly-created temp dir. info.Path is ignored — the
// materializer is whole-repo only; per-URL link targets are computed
// elsewhere from info.Path against the namespaced node IDs.
//
// Returns:
//   - rootDir: the temp dir to pass to parser.PopulateForExternalGraph.
//   - cleanup: os.RemoveAll-the-temp-dir func; the caller defers it AFTER
//     parser.PopulateForExternalGraph has returned (rootDir must outlive
//     the parser call).
//   - warning: non-nil only on size-cap (pre-read or mid-stream). The
//     dispatcher emits a warning node and skips materialization when this
//     is set.
//   - err: transport, gzip, or tar failures. The dispatcher logs and
//     skips. err is mutually exclusive with warning.
func fetchCodeloadTarball(ctx context.Context, fc *fetchClient, info githubURLInfo, maxBytes int64) (
	rootDir string, cleanup func(), warning *materializerWarning, err error,
) {
	ref := info.Ref
	if ref == "" {
		// kindRoot: codeload returns a 302 to the default-branch tarball
		// when /tar.gz is hit without a ref. The Go http client follows
		// the redirect transparently.
		ref = "HEAD"
	}
	tarURL := fmt.Sprintf("%s/%s/%s/tar.gz/%s", codeloadBaseURL, info.Owner, info.Repo, ref)

	body, w, err := fetchRaw(ctx, fc, tarURL, maxBytes)
	if err != nil {
		return "", nil, nil, fmt.Errorf("fetch tarball %q: %w", tarURL, err)
	}
	if w != nil {
		w.Owner = info.Owner
		w.Repo = info.Repo
		w.Ref = info.Ref
		return "", nil, w, nil
	}
	defer body.Close()

	gz, err := gzip.NewReader(body)
	if err != nil {
		if errors.Is(err, errSizeExceeded) {
			return "", nil, midStreamWarning(tarURL, info, body, maxBytes), nil
		}
		return "", nil, nil, fmt.Errorf("gzip reader %q: %w", tarURL, err)
	}
	defer gz.Close()

	// Apply the cap to uncompressed bytes too — gzip can expand by 1000x
	// (a tarball padded with 200 MiB of zeros compresses to a few KB),
	// so a Content-Length pre-check on compressed bytes is insufficient.
	// The cap is the total uncompressed footprint we're willing to write
	// to disk for this materialization.
	var uncompressedReader io.Reader = gz
	var uncompressed *limitedReader
	if maxBytes > 0 {
		uncompressed = &limitedReader{R: gz, N: maxBytes}
		uncompressedReader = uncompressed
	}

	keyStr := info.Owner + "/" + info.Repo + "@" + ref
	sum := sha256.Sum256([]byte(keyStr))
	tmpDir, err := os.MkdirTemp("", "knowledge-gh-mat-"+hex.EncodeToString(sum[:8])+"-")
	if err != nil {
		return "", nil, nil, fmt.Errorf("mkdir temp: %w", err)
	}
	cleanup = func() { _ = os.RemoveAll(tmpDir) }

	if w := unpackTar(uncompressedReader, tmpDir); w != nil {
		stampUnpackWarning(w, tarURL, info, body, uncompressed)
		cleanup()
		return "", nil, w, nil
	}

	if _, err := os.Stat(tmpDir); err != nil {
		cleanup()
		return "", nil, nil, fmt.Errorf("tarball unpack produced no rootDir: %w", err)
	}
	return tmpDir, cleanup, nil, nil
}

// unpackTar reads tar entries from r and writes regular files to dstDir,
// stripping the codeload top-level "<repo>-<ref>/" prefix.
//
// Path-traversal defense: any entry whose stripped path contains ".." or
// starts with "/" is rejected.
//
// Type-flag policy:
//   - TypeReg → write the file (the stdlib decodes legacy TypeRegA entries
//     into TypeReg automatically, so no explicit case is needed)
//   - TypeDir → mkdir
//   - everything else (symlinks, devices, fifos) → skipped silently
func unpackTar(r io.Reader, dstDir string) *materializerWarning {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			if errors.Is(err, errSizeExceeded) {
				return &materializerWarning{Reason: "size_cap_mid_stream"}
			}
			slog.Debug("github_materializer: tar.Next failed", "err", err)
			return nil
		}
		stripped := stripCodeloadTopDir(hdr.Name)
		if stripped == "" {
			continue // top-level dir entry itself
		}
		if isUnsafeTarPath(stripped) {
			slog.Debug("github_materializer: rejected unsafe tar path", "path", hdr.Name)
			continue
		}
		dst := filepath.Join(dstDir, filepath.FromSlash(stripped))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dst, 0o750); err != nil {
				slog.Debug("github_materializer: mkdir failed", "path", dst, "err", err)
			}
		case tar.TypeReg:
			if err := writeTarFile(tr, dst); err != nil {
				if errors.Is(err, errSizeExceeded) {
					return &materializerWarning{Reason: "size_cap_mid_stream"}
				}
				slog.Debug("github_materializer: write failed", "path", dst, "err", err)
			}
		default:
			// symlinks, devices, fifos, char-special, hardlinks → skip silently
		}
	}
}

// writeTarFile copies one tar entry's body into dst, creating parent
// directories as needed. errSizeExceeded propagates so the caller can
// emit a mid-stream warning.
func writeTarFile(r io.Reader, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, r); err != nil {
		return err
	}
	return nil
}

// stripCodeloadTopDir drops the leading "<repo>-<ref-sha>/" component from
// a codeload tar entry. Returns the empty string when the input is just
// the top-level dir itself.
func stripCodeloadTopDir(p string) string {
	p = strings.TrimPrefix(p, "./")
	_, rest, ok := strings.Cut(p, "/")
	if !ok {
		return ""
	}
	return rest
}

// isUnsafeTarPath reports whether p is a path-traversal attempt (contains
// ".." segments) or absolute. Paths are POSIX-form (slash-separated) at
// this point.
func isUnsafeTarPath(p string) bool {
	if path.IsAbs(p) {
		return true
	}
	return slices.Contains(strings.Split(p, "/"), "..")
}

// stampUnpackWarning fills the URL / owner / repo / ref / bytes_seen fields
// on a warning produced by unpackTar. Extracted so fetchCodeloadTarball's
// happy-path stays a flat sequence (gofunlen-friendly) and to keep the
// nested `if w != nil { ... }` block from tripping the nestif linter.
func stampUnpackWarning(w *materializerWarning, tarURL string, info githubURLInfo, body io.Reader, uncompressed *limitedReader) {
	w.URL = tarURL
	w.Owner = info.Owner
	w.Repo = info.Repo
	w.Ref = info.Ref
	if w.BytesSeen != 0 {
		return
	}
	switch {
	case uncompressed != nil:
		w.BytesSeen = uncompressed.BytesIn
	default:
		if rcl, ok := body.(*readCloserLimited); ok {
			w.BytesSeen = rcl.bytesIn()
		}
	}
}

// midStreamWarning constructs a size_cap_mid_stream warning preserving any
// bytes-read counter from a wrapped reader.
func midStreamWarning(url string, info githubURLInfo, body io.Reader, cap int64) *materializerWarning {
	w := &materializerWarning{
		Reason: "size_cap_mid_stream",
		URL:    url,
		Cap:    cap,
		Owner:  info.Owner,
		Repo:   info.Repo,
		Ref:    info.Ref,
	}
	if rcl, ok := body.(*readCloserLimited); ok {
		w.BytesSeen = rcl.bytesIn()
	}
	return w
}
