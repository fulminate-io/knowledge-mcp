// SPDX-License-Identifier: Apache-2.0

package syncgcs

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// gcsTransferTimeout bounds a single GCS PUT or GET. Generous for a large graph
// over a slow-but-honest link, while preventing a stuck transfer from pinning a
// goroutine indefinitely. NEVER use http.DefaultClient (no timeout).
const gcsTransferTimeout = 15 * time.Minute

// gcsHTTPClient is the no-auth HTTP client for direct GCS transfers. It is a
// SEPARATE client from the sync control-plane auth.Transport BY DESIGN: a GCS V4
// signed URL carries its signature in the query string, and ANY Authorization:
// Bearer header breaks that signature. auth.Transport.issueBytes unconditionally
// sets a Bearer header, so it cannot be reused here — these requests carry NO
// Authorization header at all.
var gcsHTTPClient = &http.Client{Timeout: gcsTransferTimeout}

// PutObject uploads body to a GCS V4 presigned PUT URL with a plain net/http
// request and NO Authorization header. contentType MUST match the content-type
// the presign signed the URL with (octet-stream) or GCS rejects the signature.
func PutObject(ctx context.Context, signedURL string, body []byte, contentType string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, signedURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("syncgcs: build PUT request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)
	// Deliberately NO Authorization header — see gcsHTTPClient.

	resp, err := gcsHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("syncgcs: PUT to GCS: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("syncgcs: GCS PUT status %d: %s", resp.StatusCode, string(snippet))
	}
	return nil
}

// GetObject downloads the object at a GCS V4 presigned GET URL with a plain
// net/http request and NO Authorization header.
func GetObject(ctx context.Context, signedURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, signedURL, nil)
	if err != nil {
		return nil, fmt.Errorf("syncgcs: build GET request: %w", err)
	}
	// Deliberately NO Authorization header — see gcsHTTPClient.

	resp, err := gcsHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("syncgcs: GET from GCS: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("syncgcs: GCS GET status %d: %s", resp.StatusCode, string(snippet))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("syncgcs: read GCS GET body: %w", err)
	}
	return data, nil
}
