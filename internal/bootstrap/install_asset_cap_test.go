// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// install_asset_cap_test.go gives maxAssetBytes its first enforcement test in
// either direction.
//
// THE CAP WAS RAISED FROM 200 MiB TO 512 MiB WITH NOTHING OBSERVING IT: reverting
// the constant left this package green, because no test read it. It is headroom
// rather than a fix — every published client asset is 65-75 MiB compressed, so
// the download cap was never the constant that refused the release — but a bound
// nothing pins is a bound that can be deleted by accident.

// serveBytes stands up a server that streams n zero bytes without ever holding
// them, and points the package httpClient at it. The SERVER side of an over-cap
// case must not allocate the payload: only the client's read is under test.
func serveBytes(t *testing.T, n int64) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		chunk := make([]byte, 1<<20)
		for remaining := n; remaining > 0; {
			c := min(int64(len(chunk)), remaining)
			if _, err := w.Write(chunk[:c]); err != nil {
				return
			}
			remaining -= c
		}
	}))
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })
	withHTTPClient(t, srv.Client())
	return srv.URL + "/asset.tar.gz"
}

// withAssetCap swaps the package's download cap for the duration of the test,
// restoring on Cleanup. It is the seam withHTTPClient uses, applied to the other
// package value the fetch path reads.
//
// IT EXISTS SO downloadAsset ITSELF CAN BE DRIVEN AGAINST ITS BOUND. Driving the
// bound through readCapped proves the enforcement; it does not prove the fetch
// path still calls it, and replacing that call with an unbounded io.ReadAll left
// this package green. At the production cap the same case costs 1.3 GB; at four
// kilobytes it costs nothing.
func withAssetCap(t *testing.T, n int64) {
	t.Helper()
	prev := maxAssetBytes
	maxAssetBytes = n
	t.Cleanup(func() { maxAssetBytes = prev })
}

// serveChunked stands up a server that streams n bytes with NO Content-Length,
// so the response reaches the client chunked and its ContentLength is -1. Every
// caller asserts that, because it is what makes a case about the read bound
// rather than about the header pre-check in front of it.
func serveChunked(t *testing.T, n int64) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		flusher, _ := w.(http.Flusher)
		chunk := make([]byte, 1<<10)
		for remaining := n; remaining > 0; {
			c := min(int64(len(chunk)), remaining)
			if _, err := w.Write(chunk[:c]); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
			remaining -= c
		}
	}))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })
	withHTTPClient(t, srv.Client())
	return srv.URL + "/asset.tar.gz"
}

// serveDeclaring stands up a server that DECLARES a Content-Length of n and
// sends no body at all.
//
// THAT IS THE POINT, NOT A SHORTCUT, and it mirrors the tar fixture that
// declares a header size with no member behind it. downloadAsset refuses an
// over-cap declared length before reading, so the body is never reached; writing
// one would cost the very gigabyte this shape exists to stop the suite from
// allocating. The client holds the header the moment Do returns, and the header
// is all the refusal consults.
//
// The server's error log is discarded because this handler deliberately writes
// less than it declared, which the server is right to complain about.
func serveDeclaring(t *testing.T, n int64) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.FormatInt(n, 10))
		w.WriteHeader(http.StatusOK)
	}))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })
	withHTTPClient(t, srv.Client())
	return srv.URL + "/asset.tar.gz"
}

// TestDownloadAsset_RefusesPastTheCap drives the download cap in BOTH
// directions: an asset the size of a real release lands, and one byte past the
// cap is refused naming the cap.
//
// THE ACCEPT LEG IS WHAT MAKES THE REFUSAL MEAN ANYTHING. Without it a
// downloadAsset that refused every response would satisfy the refusal leg on its
// own, and the raise from 200 MiB to 512 MiB would still be unobserved.
//
// THE REFUSAL LEG COSTS NOTHING, and that is a property of the code rather than
// of the fixture. downloadAsset consults the response's declared Content-Length
// before reading, so an over-cap asset is refused on a header. This case used to
// read maxAssetBytes+1 bytes to discover the overage and allocated about 1.3 GB
// on every run of this package to assert a comparison.
func TestDownloadAsset_RefusesPastTheCap(t *testing.T) {
	t.Run("an asset the size of a shipped release downloads", func(t *testing.T) {
		const releaseAssetBytes = 64_916_085 // measured v0.8.3 linux-arm64 archive
		url := serveBytes(t, releaseAssetBytes)

		data, err := downloadAsset(context.Background(), url)
		if err != nil {
			t.Fatalf("an asset the size of the shipped release must download, got: %v", err)
		}
		if int64(len(data)) != releaseAssetBytes {
			t.Fatalf("downloaded %d bytes, want %d", len(data), releaseAssetBytes)
		}
	})

	t.Run("a declared length past the cap is refused without reading", func(t *testing.T) {
		url := serveDeclaring(t, maxAssetBytes+1)

		_, err := downloadAsset(context.Background(), url)
		if err == nil {
			t.Fatalf("an asset declaring more than maxAssetBytes must be refused, got nil")
		}
		if !strings.Contains(err.Error(), "exceeds") || !strings.Contains(err.Error(), "byte cap") {
			t.Fatalf("the refusal must name the cap so an operator can act on it, got: %v", err)
		}
		// The refusal must be the CAP's and not a short-body read error, or this
		// passes for the wrong reason: the server sent no body at all.
		if strings.Contains(err.Error(), "read ") {
			t.Fatalf("the refusal came from reading the body, so the declared-length leg did not fire: %v", err)
		}
	})

	// A DECLARED LENGTH THE OLD CAP WOULD HAVE REFUSED MUST NOT BE REFUSED NOW,
	// and this is the leg that pins the CONSTANT rather than the enforcement. The
	// two legs above drive the cap relative to whatever it happens to be, so
	// reverting maxAssetBytes from 512 MiB to 200 MiB left them green: the accept
	// leg serves a real 65 MB release, which is under both, and the refuse leg
	// serves cap+1, which tracks the constant. A length BETWEEN the two values is
	// the only shape that can tell them apart, and it costs nothing — a header
	// with no body behind it.
	t.Run("a declared length between the old cap and the new one is accepted", func(t *testing.T) {
		const between = 300 << 20 // > the old 200 MiB cap, < the current 512 MiB one
		url := serveDeclaring(t, between)

		_, err := downloadAsset(context.Background(), url)
		// It falls through to the read, which fails on the absent body — that
		// failure is expected and is not the assertion. What matters is that the
		// CAP did not refuse it.
		if err != nil && strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("a declared length under maxAssetBytes must not be refused on the cap; the constant has been narrowed: %v", err)
		}
	})
}

// TestReadCapped_BoundsAnUndeclaredBody drives the read bound directly, which is
// the ONLY thing that caps a chunked response.
//
// WHY IT IS DRIVEN THROUGH readCapped RATHER THAN downloadAsset. The bound is
// reached only when the response declares no length, and reaching it through the
// 512 MiB production constant means streaming 512 MiB into the client: measured
// at 1.32 GB peak, which is why this arm shipped untested. The cap is a parameter
// precisely so the identical code can be driven at four kilobytes. The
// end-to-end leg below still proves the wiring — that a real chunked response
// really does arrive with no declared length — so nothing rests on the unit
// call alone.
func TestReadCapped_BoundsAnUndeclaredBody(t *testing.T) {
	const cap = 4 << 10

	t.Run("one byte past the cap is refused", func(t *testing.T) {
		_, err := readCapped(bytes.NewReader(make([]byte, cap+1)), cap, "http://example.invalid/a")
		if err == nil {
			t.Fatalf("a body past the cap must be refused, got nil")
		}
		if !strings.Contains(err.Error(), "exceeds") || !strings.Contains(err.Error(), "byte cap") {
			t.Fatalf("the refusal must name the cap, got: %v", err)
		}
	})

	// THE ACCEPT LEG AT THE BOUNDARY. Without it a readCapped that refused
	// everything would satisfy the leg above, and an off-by-one that refused at
	// exactly the cap would go unnoticed.
	t.Run("exactly the cap is accepted", func(t *testing.T) {
		data, err := readCapped(bytes.NewReader(make([]byte, cap)), cap, "http://example.invalid/a")
		if err != nil {
			t.Fatalf("a body of exactly the cap must be accepted, got: %v", err)
		}
		if len(data) != cap {
			t.Fatalf("read %d bytes, want %d", len(data), cap)
		}
	})

	// THE WIRING, both sides real: a chunked httptest response really does reach
	// the client with ContentLength == -1, so the Content-Length pre-check cannot
	// see it and the read bound is what refuses. The ContentLength assertion is
	// the control — without it this case would pass even if the server had
	// declared a length and the header arm had done the refusing.
	t.Run("a chunked response bypasses the header check and is caught by the read", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			flusher, _ := w.(http.Flusher)
			for range 2 {
				if _, err := w.Write(make([]byte, cap)); err != nil {
					return
				}
				if flusher != nil {
					flusher.Flush()
				}
			}
		}))
		srv.Config.ErrorLog = log.New(io.Discard, "", 0)
		t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })

		resp, err := srv.Client().Get(srv.URL + "/asset.tar.gz")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		defer resp.Body.Close()
		if resp.ContentLength != -1 {
			t.Fatalf("the control failed: the response declared a length of %d, so this case would exercise the header arm rather than the read bound", resp.ContentLength)
		}

		if _, err := readCapped(resp.Body, cap, srv.URL); err == nil {
			t.Fatalf("an undeclared body past the cap must be refused, got nil")
		} else if !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("the read bound must name the cap, got: %v", err)
		}
	})

	// THE SAME WIRING IN THE ACCEPT DIRECTION. Without it the case above is
	// equally consistent with a bound that refuses every chunked response, which
	// would break the ordinary download of an asset served without a declared
	// length — the common case behind a proxy that re-chunks.
	t.Run("a chunked response under the cap succeeds", func(t *testing.T) {
		const body = cap / 2
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			flusher, _ := w.(http.Flusher)
			if _, err := w.Write(make([]byte, body)); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}))
		srv.Config.ErrorLog = log.New(io.Discard, "", 0)
		t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })

		resp, err := srv.Client().Get(srv.URL + "/asset.tar.gz")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		defer resp.Body.Close()
		if resp.ContentLength != -1 {
			t.Fatalf("the control failed: the response declared a length of %d, so this is not the chunked case", resp.ContentLength)
		}

		data, err := readCapped(resp.Body, cap, srv.URL)
		if err != nil {
			t.Fatalf("a chunked body under the cap must be read, got: %v", err)
		}
		if len(data) != body {
			t.Fatalf("read %d bytes, want %d", len(data), body)
		}
	})
}

// TestDownloadAsset_BoundsAnUndeclaredBody drives the FETCH PATH against its own
// bound, which is the one thing the readCapped legs above cannot prove.
//
// WHY IT IS SEPARATE FROM THEM. Those legs establish that the enforcement is
// correct and two-sided; they say nothing about whether downloadAsset still
// calls it. Replacing `return readCapped(resp.Body, maxAssetBytes, url)` with an
// unbounded io.ReadAll — reintroducing exactly the vulnerability the bound exists
// to prevent, on the production constant, for every chunked response — left the
// whole package green over a 55 second run. This case is what reds instead.
//
// The two existing downloadAsset legs cannot reach it: the accept leg serves a
// 65 MB DECLARED body, and the refuse leg is answered by the Content-Length
// pre-check before the read is ever attempted. Only an undeclared body past the
// cap reaches the line, and the cap seam is what makes that affordable.
func TestDownloadAsset_BoundsAnUndeclaredBody(t *testing.T) {
	const assetCap = 4 << 10
	withAssetCap(t, assetCap)

	t.Run("a chunked body past the cap is refused by the fetch path", func(t *testing.T) {
		url := serveChunked(t, assetCap*2)

		_, err := downloadAsset(context.Background(), url)
		if err == nil {
			t.Fatalf("downloadAsset must refuse an undeclared body past the cap; an unbounded read here is the vulnerability the cap exists to prevent")
		}
		if !strings.Contains(err.Error(), "exceeds") || !strings.Contains(err.Error(), "byte cap") {
			t.Fatalf("the refusal must name the cap, got: %v", err)
		}
	})

	// THE ACCEPT LEG, on the same path and the same seam. Without it a
	// downloadAsset that refused every chunked response would satisfy the case
	// above, and an asset served without a declared length — the ordinary shape
	// behind a proxy that re-chunks — would stop downloading.
	t.Run("a chunked body under the cap is downloaded whole", func(t *testing.T) {
		const body = assetCap / 2
		url := serveChunked(t, body)

		data, err := downloadAsset(context.Background(), url)
		if err != nil {
			t.Fatalf("a chunked body under the cap must download, got: %v", err)
		}
		if len(data) != body {
			t.Fatalf("downloaded %d bytes, want %d", len(data), body)
		}
	})
}
