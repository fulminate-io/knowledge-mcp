// SPDX-License-Identifier: Apache-2.0

// install_http.go — HTTP plumbing for `knowledge install`. Split from
// install.go for the 500-line cap and to keep the install_test.go
// httpClient stub seam in its own file. Stdlib net/http only.

package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// maxAssetBytes caps any single asset download to defend against
// runaway responses. The client archive is ~65–75 MiB compressed
// (v0.8.3, all platforms) and the server ~15 MiB; 512 MiB clears both
// with room to grow while staying far from memory-exhausting territory.
// The decompressed size is bounded separately by maxExtractedBytes.
//
// IT IS A VAR RATHER THAN A CONST SO THE BOUND CAN BE DRIVEN END TO END, on the
// seam this package already uses for httpClient just below: a package value a
// test helper swaps and restores on cleanup. NOTHING IN PRODUCTION ASSIGNS IT —
// the only writer is withAssetCap in install_asset_cap_test.go.
//
// The alternative was leaving it const, and the cost of that was measured rather
// than argued: reaching downloadAsset's undeclared-length bound through 512 MiB
// means streaming 512 MiB into the test, at 1.3 GB of resident memory, so the
// bound shipped with its production callsite unobserved. Replacing that callsite
// with an unbounded io.ReadAll — reintroducing exactly the vulnerability the
// bound exists to prevent — left the whole package green.
var maxAssetBytes int64 = 512 << 20

// httpClient is a package-level seam so install_test.go can swap in
// an httptest.Server's client without re-plumbing every call site.
// The 60s timeout is generous for a large release archive over a
// slow link — well above the typical 1-5s but bounded so a hung
// transfer eventually fails.
var httpClient = &http.Client{Timeout: 60 * time.Second}

// releaseAsset matches the GitHub releases API asset shape. Only the
// fields runInstall consumes are declared — extra fields in the
// response are ignored by encoding/json.
type releaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// releaseResponse matches the GitHub releases API response shape.
type releaseResponse struct {
	TagName string         `json:"tag_name"`
	Assets  []releaseAsset `json:"assets"`
}

// fetchRelease GETs the release JSON for the given tag. When
// isLatest is true the /releases/latest endpoint resolves against
// the most recently published non-draft, non-prerelease release.
// When isLatest is false the /releases/tags/<tag> endpoint pins to
// the exact tag.
//
// 404 is the canonical "release missing" signal; we surface it with
// a clear message naming the tag so users can sanity-check the tag
// they're chasing (often they typed v1.2.3 when the release is
// v1.2.4, or they're on a dev binary asking for a version that
// hasn't shipped yet). Other non-2xx responses are wrapped with the
// status code + a truncated body slice (~1 KiB) for diagnostics.
func fetchRelease(ctx context.Context, baseURL, tag string, isLatest bool) (*releaseResponse, error) {
	var url string
	if isLatest {
		url = baseURL + "/repos/fulminate-io/knowledge-mcp/releases/latest"
	} else {
		url = baseURL + "/repos/fulminate-io/knowledge-mcp/releases/tags/" + tag
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build release request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		label := tag
		if isLatest {
			label = "latest"
		}
		return nil, fmt.Errorf("release %s not found at github.com/fulminate-io/knowledge-mcp", label)
	}
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("get release: HTTP %d: %s", resp.StatusCode, string(body))
	}

	var rel releaseResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxAssetBytes)).Decode(&rel); err != nil {
		return nil, fmt.Errorf("decode release JSON: %w", err)
	}
	return &rel, nil
}

// downloadAsset GETs an asset URL (typically the browser_download_url
// returned by fetchRelease) and reads up to maxAssetBytes into
// memory. Returns the raw bytes — callers verify SHA256 before
// extracting.
func downloadAsset(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build asset request: %w", err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("get %s: HTTP %d: %s", url, resp.StatusCode, string(body))
	}

	// THE DECLARED LENGTH IS CHECKED BEFORE A BYTE IS READ, the same ordering the
	// archive extractor applies to a tar header's Size and a zip entry's
	// UncompressedSize64, and for the same reason: refusing an over-cap asset must
	// not first allocate the cap to find out. Reading cap+1 bytes to discover the
	// overage cost about 1.3 GB, and it cost it on every run of this package's
	// enforcement test as well as on a real hostile response.
	//
	// ContentLength IS -1 WHEN THE LENGTH IS UNKNOWN (a chunked response declares
	// none), and -1 is not greater than the cap, so an undeclared body falls
	// through to the read bound below rather than being refused on a length
	// nobody stated.
	if resp.ContentLength > maxAssetBytes {
		return nil, fmt.Errorf("asset %s exceeds %d-byte cap", url, maxAssetBytes)
	}

	return readCapped(resp.Body, maxAssetBytes, url)
}

// readCapped reads r into memory, refusing at cap bytes.
//
// IT IS THE ONLY THING BOUNDING A RESPONSE THAT DECLARED NO LENGTH AT ALL, which
// is what makes it load-bearing rather than belt and braces: net/http truncates a
// body to a declared Content-Length, so a server that understates one cannot
// deliver more than it declared, but a CHUNKED response declares nothing and can
// stream without limit. The caller's Content-Length pre-check cannot see that
// case (ContentLength is -1), and this is what stops it.
//
// THE CAP IS A PARAMETER SO THE ARM CAN BE DRIVEN AT A SANE SIZE. Enforcing it
// against the 512 MiB constant inline meant the only way to observe this bound
// was to stream 512 MiB into a test, which cost 1.3 GB of resident memory and was
// therefore left untested — a security bound shipped unobserved on a cost that
// this parameter removes. Production passes maxAssetBytes and is unchanged; a
// test passes a few kilobytes and drives the identical refusal.
func readCapped(r io.Reader, cap int64, url string) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, cap+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", url, err)
	}
	if int64(len(data)) > cap {
		return nil, fmt.Errorf("asset %s exceeds %d-byte cap", url, cap)
	}
	return data, nil
}
