// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"fmt"
	"io"
	"net/http"
)

// mePathPrefix is the caller-scoped route prefix. Unlike /v1/sync/* and
// /v1/segments/*, routes under it are bearer-gated but NOT tier-gated.
const mePathPrefix = "/v1/me/"

// jsonAccept is the Accept value for routes returning JSON. The bytes routes
// keep advertising application/octet-stream.
const jsonAccept = "application/json"

// ListAccounts issues GET /v1/me/accounts with the caller's bearer and returns
// the raw 2xx JSON body verbatim.
//
// Raw bytes rather than a decoded struct is this package's established
// convention — SyncControlJSON and SegmentControlJSON do the same, and their
// DTOs live in the consuming packages.
//
// THIS ROUTE BYPASSES THE KNOWN-INVALID REFUSAL, deliberately, and that
// asymmetry must not be "fixed": a user whose selection has been rejected by
// the gateway would otherwise be locked out of the very command that tells
// them which accounts they may pick. The header is still STAMPED when a valid
// selection exists (the endpoint is bearer-scoped to the caller's own
// memberships, so it is inert there); only the refusal is bypassed.
//
// The bypass is a per-call parameter rather than transport state: the daemon
// shares one Transport across concurrent callers, so a set-then-reset flag
// could let a concurrent sync push skip its own refusal.
//
// The 401 force-refresh-and-retry-once path applies unchanged and is desirable
// here: an expired session on `knowledge accounts` should refresh, not fail.
func (t *Transport) ListAccounts(ctx context.Context) ([]byte, error) {
	resp, err := t.sendWithAuthBytes(ctx, http.MethodGet, mePathPrefix, "accounts", jsonAccept, nil, true)
	if err != nil {
		return nil, fmt.Errorf("auth: list accounts: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, t.readHTTPError(ctx, resp, mePathPrefix+"accounts")
	}
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("auth: read accounts response: %w", err)
	}
	return out, nil
}
