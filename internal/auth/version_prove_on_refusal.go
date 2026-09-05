// SPDX-License-Identifier: Apache-2.0

// version_prove_on_refusal.go — the CLI's one-shot recovery from a refusal that
// says "this client has no live verification record".
//
// The daemon proves on a background loop; a short-lived CLI invocation has no
// loop and no daemon, so on a machine that never runs one it would be refused
// forever with a remedy it cannot apply. This recovers exactly that case: prove
// once, retry once, and otherwise get out of the way.

package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"

	"github.com/fulminate-io/knowledge-mcp/internal/clientver"
)

// WithProveOnRefusal enables the one-shot prove-and-retry recovery on a
// Transport, using answer to compute the possession proof.
//
// It is OFF by default and enabled by an explicit option because the daemon
// builds its transports through other paths and must keep its existing
// behavior, where the background proof loop rather than the request path owns
// proving. Turning it on there would give the daemon two provers.
func WithProveOnRefusal(answer func(nonce []byte, offset, length int64) (string, error)) TransportOption {
	return func(t *Transport) { t.proveAnswer = answer }
}

// proveOnRefusalState is the per-Transport single-attempt guard.
type proveOnRefusalState struct {
	once sync.Once
}

// maybeProveAndRetry inspects a non-2xx response and, when it is a refusal a
// proof can actually fix, runs the challenge exchange once and reports that the
// caller should retry the original request.
//
// IT TRIGGERS ONLY ON version_unverified. That reason means no live
// verification record — the one refusal a proof repairs. A below-minimum client
// is genuinely too old, an unprovable one has no artifact to prove against, and
// a reason this repo has never heard of is by definition one it cannot know how
// to fix; proving on any of them burns a round trip and still fails. They pass
// straight through with their remedy intact.
//
// THE GUARD IS CONSUMED BEFORE THE EXCHANGE RUNS, which is what makes recursion
// impossible by construction rather than by care: the challenge legs travel over
// this same Transport, so a refusal on one finds the guard already spent and
// passes through instead of re-entering. A failed proof therefore surfaces the
// gateway's refusal rather than starting a retry loop.
//
// THE BODY IS RESTORED WHEN NOT RETRYING. Deciding whether to prove requires
// reading the refusal body, and the caller reads it again afterwards to build
// the user-facing error. Without putting it back, that second read returns
// nothing, the parse fails, and a real below-minimum refusal reaches the user as
// an unparseable one with no minimum and no remedy — which turns this feature's
// most user-facing surface into a shrug.
func (t *Transport) maybeProveAndRetry(ctx context.Context, resp *http.Response) bool {
	if t.proveAnswer == nil || resp.StatusCode != http.StatusUpgradeRequired {
		return false
	}

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, MaxErrorBodyBytes))
	// Drain and close the original before substituting a replayable copy: a
	// LimitReader stops at the cap WITHOUT reaching EOF, so a body larger than
	// the cap would otherwise leave the connection unreleased.
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(raw))

	reason := refusalReason(raw)
	if reason != clientver.ReasonUnverified {
		return false
	}

	claimed := false
	t.proveState.once.Do(func() { claimed = true })
	if !claimed {
		// Already spent on this Transport — one attempt per invocation.
		return false
	}

	expiresAt, err := t.VersionChallenge(ctx, t.proveAnswer)
	if err != nil {
		clientver.RecordProof(clientver.ProofState{
			OK:      false,
			Version: clientver.Version, Platform: clientver.Platform(),
			Err: err.Error(),
		})
		t.logger.Warn("sync: version proof failed; surfacing the gateway's refusal", "error", err)
		return false
	}
	clientver.RecordProof(clientver.ProofState{
		OK:      true,
		Version: clientver.Version, Platform: clientver.Platform(),
	})
	// Cleared only after a proof SUCCEEDS, never on an attempt, so a standing
	// refusal keeps showing on the status surfaces.
	clientver.ClearRefusal()
	t.logger.Debug("sync: proved this client's version; retrying the refused request", "expires_at", expiresAt)
	return true
}

// refusalReason reads just the reason out of a refusal body. An unreadable body
// yields "", which is not the one reason that triggers a proof — an unparseable
// refusal is surfaced, not guessed at.
func refusalReason(body []byte) string {
	var parsed versionRefusalBody
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ""
	}
	return parsed.Reason
}
