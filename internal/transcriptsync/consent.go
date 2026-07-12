// SPDX-License-Identifier: Apache-2.0

package transcriptsync

import (
	"context"
	"encoding/json"
	"fmt"
)

// consentControlPath is the consent read endpoint, pinned by AG-Consent: the
// agent serves it as POST /v1/sync/transcript-consent. SyncControlJSON prepends
// the /v1/sync/ prefix to this bare path component. The account is resolved
// agent-side from the bearer token, so the request carries no account id (no
// IDOR surface).
const consentControlPath = "transcript-consent"

// consentResponse is the agent's consent reply. transcript_collection_enabled is
// the per-account flag the user toggles; when false, the whole batch is skipped.
type consentResponse struct {
	TranscriptCollectionEnabled bool `json:"transcript_collection_enabled"`
}

// consentEnabled fetches the per-account transcript_collection_enabled flag once
// per batch via the consent control endpoint. It reuses ControlTransport.
// SyncControlJSON verbatim — no new client transport method.
//
// The caller (Run) maps the two failure modes distinctly:
//   - (false, nil) — consent disabled: skip the ENTIRE batch, ship nothing.
//   - (_, err)     — fetch failed: skip-and-retry — abort with NO ships and NO
//     watermark writes, returning the error so a scheduled re-run retries.
//
// Either way nothing is uploaded unless consent is affirmatively true; the gate
// is kept strictly distinct from per-chunk upload-failure handling.
func consentEnabled(ctx context.Context, t ControlTransport) (bool, error) {
	// A minimal non-nil body: the account comes from the bearer token, so there
	// is nothing to send, but issueBytes only sets the octet-stream content type
	// when the body is non-nil and the agent reads JSON regardless.
	raw, err := t.SyncControlJSON(ctx, consentControlPath, []byte(`{}`))
	if err != nil {
		return false, fmt.Errorf("transcriptsync: fetch transcript consent: %w", err)
	}
	var resp consentResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return false, fmt.Errorf("transcriptsync: decode transcript consent response: %w", err)
	}
	return resp.TranscriptCollectionEnabled, nil
}
