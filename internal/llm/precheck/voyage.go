// SPDX-License-Identifier: Apache-2.0

package precheck

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// voyageEndpoint is the Voyage AI embeddings endpoint. Hardcoded
// because there's no per-deployment override surface — Voyage doesn't
// publish a self-hosted variant. If a future need surfaces (proxy,
// air-gap), parameterize via a precheck option.
const voyageEndpoint = "https://api.voyageai.com/v1/embeddings"

// voyagePingTimeout caps the HTTP round-trip. Voyage typically
// responds in 200-500ms; 10s absorbs a slow-network startup without
// holding up server boot.
const voyagePingTimeout = 10 * time.Second

// voyagePingModel is the cheapest Voyage model that always exists.
// We don't care about embedding quality — only that auth succeeds —
// so the smallest, fastest model is right.
const voyagePingModel = "voyage-3-lite"

// CheckVoyage verifies the Voyage API key with a single embed request
// against the cheapest model. Returns nil when apiKey is empty (BM25-
// only mode is the documented opt-out — the embedder pipeline already
// handles missing-key by falling back to BM25 search). Returns nil
// on 200 OK from Voyage. Returns a wrapped error naming the HTTP
// status when Voyage rejects the request.
//
// Failure classes the check catches:
//   - VOYAGE_API_KEY env var set to an invalid / revoked key (401/403).
//   - Network unreachable to api.voyageai.com (firewall, captive portal).
//   - Voyage outage (5xx).
//
// Cost: a single 1-token embed against voyage-3-lite — Voyage charges
// per million tokens; per-startup cost is rounding-error fractions of
// a cent.
func CheckVoyage(ctx context.Context, apiKey string) error {
	if apiKey == "" {
		slog.Info("precheck: VOYAGE_API_KEY unset — vector search disabled (BM25 only)")
		return nil
	}
	// Minimum viable Voyage embed request — input + model only. Don't
	// pass output_dim or output_dtype: those exist on the prod embedder
	// path because we want binary 256d vectors for storage, but for an
	// auth-only ping we just want the API to accept-or-reject the key.
	// Default-shaped response is fine; we don't read the embedding.
	body, err := json.Marshal(map[string]any{
		"input": []string{"ping"},
		"model": voyagePingModel,
	})
	if err != nil {
		return fmt.Errorf("marshal voyage request: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, voyagePingTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(pingCtx, http.MethodPost, voyageEndpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build voyage request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	slog.Info("precheck: pinging Voyage", "endpoint", voyageEndpoint, "model", voyagePingModel)
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	elapsed := time.Since(start)
	if err != nil {
		return fmt.Errorf("voyage ping (elapsed=%s): %w", elapsed.Round(time.Millisecond), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		slog.Info("precheck: Voyage ok", "elapsed", elapsed.Round(time.Millisecond))
		return nil
	}

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("voyage rejected VOYAGE_API_KEY (HTTP %d): the key may be invalid, revoked, or out of credits — body: %s", resp.StatusCode, string(respBody))
	case http.StatusTooManyRequests:
		return fmt.Errorf("voyage rate-limited (HTTP 429): retry shortly or check usage at voyageai.com — body: %s", string(respBody))
	default:
		return fmt.Errorf("voyage returned HTTP %d (elapsed=%s): %s", resp.StatusCode, elapsed.Round(time.Millisecond), string(respBody))
	}
}
