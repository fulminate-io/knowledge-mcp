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
// runaway responses. 200 MiB is well above the largest release
// archive (server binaries are ~50 MiB compressed) and well below
// anything that would exhaust memory on a developer machine.
const maxAssetBytes = 200 << 20

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

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAssetBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", url, err)
	}
	if int64(len(data)) > maxAssetBytes {
		return nil, fmt.Errorf("asset %s exceeds %d-byte cap", url, maxAssetBytes)
	}
	return data, nil
}
