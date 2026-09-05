// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"
)

// versionChallengePath is the bare route segment the control channel joins to
// its own prefix. Every existing caller passes a bare segment the same way, so
// this endpoint needs no transport change of any kind.
const versionChallengePath = "version-challenge"

// The two legs of the exchange, discriminated by the request body's phase.
const (
	challengePhaseRequest = "request"
	challengePhaseAnswer  = "answer"
)

// challengeRequest is the leg-1 body. The field names are transcribed from the
// verifier's frozen contract; they are the approved header tokens with the
// vendor prefix dropped and dashes lowered to underscores.
type challengeRequest struct {
	Phase string `json:"phase"`
}

// challengeIssue is the leg-1 response.
type challengeIssue struct {
	VersionChallenge string `json:"version_challenge"`
	VersionRange     struct {
		Offset int64 `json:"offset"`
		Length int64 `json:"length"`
	} `json:"version_range"`
}

// challengeAnswer is the leg-2 body. VersionChallenge echoes the issued text
// VERBATIM — the client hashes the challenge's DECODED bytes but sends back the
// text it was given, because that text is what the verifier keyed its record on.
type challengeAnswer struct {
	Phase            string `json:"phase"`
	VersionChallenge string `json:"version_challenge"`
	VersionProof     string `json:"version_proof"`
}

// challengeVerdict is the leg-2 success response.
type challengeVerdict struct {
	Verified  bool   `json:"verified"`
	ExpiresAt string `json:"expires_at"`
}

// VersionChallenge runs the two-leg possession exchange against the gateway and
// returns the expiry of the verification record it established.
//
// The ANSWER IS A CALLER-SUPPLIED FUNCTION rather than a call into the self-read
// package: it keeps this package the transport and the self-read package the
// self-read, so auth never opens a file and never imports the version identity.
// The nonce reaches that function as RAW DECODED BYTES, because the proof is
// defined over the decoded nonce and an implementation that hashed the base64url
// TEXT would produce a proof that never matches with no compile-time tell on
// either side of the repo boundary.
//
// The version and platform are NOT parameters. They ride the client-identity
// headers this transport already stamps on every request it issues, and taking
// them as arguments would create a second place they could disagree with the
// values actually sent.
//
// The offset and length reach the answer function unmodified, so this client
// cannot silently answer a different range than it was asked for. A range the
// answer function refuses — one above its own ceiling, for instance — surfaces
// as an error naming the leg; it is never truncated into a well-formed proof of
// the wrong bytes.
//
// A gateway refusal on either leg arrives as a 426 and is latched by the shared
// classifier on the way out of the control channel, so a refusal surfaces as a
// refusal rather than as an exchange failure.
func (t *Transport) VersionChallenge(
	ctx context.Context,
	answer func(nonce []byte, offset, length int64) (string, error),
) (expiresAt time.Time, err error) {
	if answer == nil {
		return time.Time{}, fmt.Errorf("auth: version challenge: no answer function supplied")
	}

	reqBody, err := json.Marshal(challengeRequest{Phase: challengePhaseRequest})
	if err != nil {
		return time.Time{}, fmt.Errorf("auth: version challenge: encode request leg: %w", err)
	}
	rawIssue, err := t.SyncControlJSON(ctx, versionChallengePath, reqBody)
	if err != nil {
		return time.Time{}, fmt.Errorf("auth: version challenge request leg: %w", err)
	}
	var issued challengeIssue
	if err := json.Unmarshal(rawIssue, &issued); err != nil {
		return time.Time{}, fmt.Errorf("auth: version challenge: decode request-leg response: %w", err)
	}
	if issued.VersionChallenge == "" {
		return time.Time{}, fmt.Errorf("auth: version challenge: request leg returned no challenge")
	}

	// The contract sends the nonce base64url-encoded WITHOUT padding and hashes
	// its decoded bytes.
	nonce, err := base64.RawURLEncoding.DecodeString(issued.VersionChallenge)
	if err != nil {
		return time.Time{}, fmt.Errorf("auth: version challenge: decode challenge nonce: %w", err)
	}

	proof, err := answer(nonce, issued.VersionRange.Offset, issued.VersionRange.Length)
	if err != nil {
		return time.Time{}, fmt.Errorf("auth: version challenge: answer range [%d,%d): %w",
			issued.VersionRange.Offset, issued.VersionRange.Offset+issued.VersionRange.Length, err)
	}

	ansBody, err := json.Marshal(challengeAnswer{
		Phase:            challengePhaseAnswer,
		VersionChallenge: issued.VersionChallenge,
		VersionProof:     proof,
	})
	if err != nil {
		return time.Time{}, fmt.Errorf("auth: version challenge: encode answer leg: %w", err)
	}
	rawVerdict, err := t.SyncControlJSON(ctx, versionChallengePath, ansBody)
	if err != nil {
		return time.Time{}, fmt.Errorf("auth: version challenge answer leg: %w", err)
	}
	var verdict challengeVerdict
	if err := json.Unmarshal(rawVerdict, &verdict); err != nil {
		return time.Time{}, fmt.Errorf("auth: version challenge: decode answer-leg response: %w", err)
	}
	if !verdict.Verified {
		return time.Time{}, fmt.Errorf("auth: version challenge: gateway did not verify the proof")
	}
	// expires_at is REQUIRED, not decorative: the proof loop schedules the next
	// proof from it, and a client that quietly dropped it would fall back to a
	// guessed interval that drifts out of the gateway's real TTL unnoticed.
	if verdict.ExpiresAt == "" {
		return time.Time{}, fmt.Errorf("auth: version challenge: gateway verified the proof but returned no expires_at, so the next proof cannot be scheduled")
	}
	parsed, err := time.Parse(time.RFC3339, verdict.ExpiresAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("auth: version challenge: parse expires_at %q: %w", verdict.ExpiresAt, err)
	}
	return parsed, nil
}
