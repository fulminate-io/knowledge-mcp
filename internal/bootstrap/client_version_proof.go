// SPDX-License-Identifier: Apache-2.0

// client_version_proof.go — the daemon's background possession-proof loop.
//
// The agent gateway fronts all client cloud traffic and refuses a client whose
// version it cannot verify. Verification is established by a challenge exchange
// in which the gateway names a byte range of the client's own release artifact
// and the client answers with a digest over that range; the gateway then writes
// a verification record with a TTL. This loop is what keeps that record alive:
// it proves at daemon start, re-proves before the record lapses, and re-proves
// immediately when a refusal is latched.
//
// It follows the transcript-upload loop's shape — an overridable seam so tests
// need no gateway, a cloud-auth gate re-checked each tick, and a failure that is
// logged rather than fatal — with the three differences its own job requires:
// no boot delay, a schedule derived from the gateway's own expiry rather than a
// guessed constant, and bounds on both the success and the refusal paths.

package bootstrap

import (
	"context"
	"log/slog"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/clientver"
)

// proofSafetyMargin is how far BEFORE the gateway's stated expiry the next proof
// is scheduled. It absorbs clock skew between this client and the gateway plus
// the round trip of the exchange itself, so the replacement record is written
// while the old one is still live and no cloud request falls into an
// unverified window.
const proofSafetyMargin = 15 * time.Minute

// minReproveInterval floors the delay computed from the gateway's expiry, on the
// SUCCESS path.
//
// Without a floor a well-formed expiry that is near, already past, or skewed
// against this client's clock yields a non-positive delay, the timer fires
// immediately, and the loop re-proves without bound — and neither of the loop's
// other two bounds covers it, because the refusal spacing below is scoped to the
// REFUSAL trigger and the short failure cadence to a missing or unparseable
// value. This path parses fine and the proof SUCCEEDED, so it takes neither.
//
// The floor exists to bound what a value the SERVER supplies can cost the
// gateway, which is the whole reason a client-side floor belongs on a
// server-scheduled loop. Reachability is honest: under the contract as it stands
// the delay sits near 45 minutes and this bound never binds. It becomes
// reachable on a gateway-side TTL reduction, a large client clock skew, or a
// gateway bug returning a past expiry — none of which this client controls.
const minReproveInterval = 5 * time.Minute

// proofFailureInterval is the retry cadence after a FAILED proof, including the
// contract-violating case of a success response carrying no usable expiry.
// Shorter than the healthy cadence because an unverified client is being refused
// on every cloud request meanwhile, and longer than a busy-wait because a
// persistent failure must not become an outbound storm.
const proofFailureInterval = 2 * time.Minute

// minRefusalReproveSpacing bounds the REFUSAL-driven trigger. A refusal that
// cannot be cleared — a genuinely too-old client — would otherwise re-prove on
// every observation forever. The honest end state there is a client that stays
// refused and says so on both status surfaces, not one that hammers the
// endpoint; this spacing is what makes that the actual behavior.
const minRefusalReproveSpacing = 5 * time.Minute

// refusalPollInterval is how often the loop looks for a newly latched refusal
// while waiting for its next scheduled proof. It is a poll rather than a signal
// because the latch has two writers in different packages and neither owns a
// channel this loop could select on.
const refusalPollInterval = 30 * time.Second

// versionProofOnce is the proof function the loop invokes. It is a package var
// so a test can substitute a counting fake for the real network exchange,
// mirroring the transcriptUploadOnce seam. Production leaves the default, which
// runs the gateway exchange and answers it from the executable handle opened at
// daemon construction.
//
//nolint:gochecknoglobals // overridable proof seam for testability; mirrors transcriptUploadOnce.
var versionProofOnce = func(ctx context.Context, transportFn func() (*auth.Transport, error)) (time.Time, error) {
	tr, err := transportFn()
	if err != nil {
		return time.Time{}, err
	}
	return tr.VersionChallenge(ctx, clientver.AnswerChallenge)
}

// nowFn is the clock the loop reads, injectable so a test can drive the
// schedule without sleeping real time.
//
//nolint:gochecknoglobals // injectable clock for testability.
var nowFn = time.Now

// runVersionProofOnce performs one proof attempt, records its outcome so both
// status surfaces can show it, and returns the delay before the next attempt.
//
// ERROR POSTURE, stated because it resembles a swallowed error and is not. A
// failed proof is logged and the loop continues, exactly as the transcript loop
// continues past a failed batch — but the LOUDNESS lives on the request path:
// with no verification record the gateway refuses every cloud call with an
// instructive refusal the transports surface and latch. The recorded ProofState
// is what makes a persistently failing proof visible rather than merely retried.
func runVersionProofOnce(ctx context.Context, transportFn func() (*auth.Transport, error)) time.Duration {
	expiresAt, err := versionProofOnce(ctx, transportFn)
	now := nowFn()
	if err != nil {
		clientver.RecordProof(clientver.ProofState{
			At: now, OK: false,
			Version: clientver.Version, Platform: clientver.Platform(),
			Err: err.Error(),
		})
		slog.Warn("version proof: failed (will retry)", "error", err, "retry_in", proofFailureInterval)
		return proofFailureInterval
	}

	clientver.RecordProof(clientver.ProofState{
		At: now, OK: true,
		Version: clientver.Version, Platform: clientver.Platform(),
	})
	// The latch is cleared only after a proof SUCCEEDS, never on an attempt, so
	// a standing refusal keeps showing on both status surfaces until this client
	// has actually re-established a record.
	clientver.ClearRefusal()

	// A response with no usable expiry violates the contract, so it is treated
	// as a failed schedule rather than assumed to be an hour. runVersionProofOnce
	// receives a zero time only if the exchange returned one without erroring.
	if expiresAt.IsZero() {
		slog.Warn("version proof: succeeded but carried no expiry; retrying on the failure cadence rather than guessing a schedule",
			"retry_in", proofFailureInterval)
		return proofFailureInterval
	}

	delay := max(expiresAt.Sub(now)-proofSafetyMargin,
		// See minReproveInterval: this is the success-path floor, and it is the
		// only thing standing between a near/past/skewed expiry and an
		// unbounded outbound loop.
		minReproveInterval)
	slog.Debug("version proof: verified", "expires_at", expiresAt, "next_proof_in", delay)
	return delay
}

// runVersionProofLoop proves at start with NO boot delay, then re-proves on the
// schedule the gateway's expiry implies, and immediately when a refusal is
// latched.
//
// NO BOOT DELAY is the difference from the transcript template a copy-paste
// would silently lose: until a verification record exists every cloud request is
// refused, so a delay would guarantee a window of refusals on every restart. The
// loop still runs off the bind critical path because wireRuntimesBackground
// spawns it with `go`.
//
// THE CLOUD-AUTH GATE short-circuits at the TOP of each attempt, before a
// transport is built, and loggedIn is re-read every time so a mid-session login
// starts proving with no restart.
func runVersionProofLoop(
	ctx context.Context,
	transportFn func() (*auth.Transport, error),
	loggedIn func(context.Context) bool,
) {
	runVersionProofLoopEvery(ctx, refusalPollInterval, transportFn, loggedIn)
}

// runVersionProofLoopEvery is runVersionProofLoop with the refusal-poll cadence
// supplied by the caller. The cadence is a parameter ONLY so tests can drive the
// schedule in milliseconds instead of half-minutes, mirroring
// probeDaemonVersionWithin in this same package; every shipped caller goes
// through runVersionProofLoop and gets refusalPollInterval.
func runVersionProofLoopEvery(
	ctx context.Context,
	poll time.Duration,
	transportFn func() (*auth.Transport, error),
	loggedIn func(context.Context) bool,
) {
	var lastRefusalReprove time.Time

	attempt := func() time.Duration {
		if !loggedIn(ctx) {
			slog.Debug("version proof: skipped — not authenticated")
			return proofFailureInterval
		}
		return runVersionProofOnce(ctx, transportFn)
	}

	next := nowFn().Add(attempt())

	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := nowFn()
			if !now.Before(next) {
				next = now.Add(attempt())
				continue
			}
			// REFUSAL-DRIVEN RE-PROOF: a latched refusal means the record is
			// gone or was never made, so waiting for the scheduled proof would
			// leave every cloud request refused meanwhile. Bounded by
			// minRefusalReproveSpacing so a client that can never be cleared
			// stays refused and says so instead of hammering the endpoint.
			if _, refused := clientver.CurrentRefusal(); !refused {
				continue
			}
			if !lastRefusalReprove.IsZero() && now.Sub(lastRefusalReprove) < minRefusalReproveSpacing {
				continue
			}
			lastRefusalReprove = now
			next = now.Add(attempt())
		}
	}
}

// maybeStartVersionProof opens the executable handle the possession proof reads
// from and spawns the proof loop.
//
// WIRING ORDER IS LOAD-BEARING: OpenSelf runs FIRST. Starting the loop before
// the handle exists would make the first proof fail for a reason that has
// nothing to do with the gateway.
//
// ERROR POSTURE WHEN OpenSelf FAILS — a missing, replaced or unreadable
// executable: log at Error naming the condition, do NOT abort daemon start, and
// record a failed proof state so the condition is visible on both render
// surfaces. Aborting startup would take a working local daemon offline over a
// cloud-only capability; staying silent would leave every later proof failing
// with an unopened-handle error and nothing to explain it.
func (c *client) maybeStartVersionProof(ctx context.Context) {
	if err := clientver.OpenSelf(); err != nil {
		slog.Error("version proof: could not open the running executable; possession proofs will fail until this daemon is restarted from a readable binary",
			"error", err)
		clientver.RecordProof(clientver.ProofState{
			At: nowFn(), OK: false,
			Version: clientver.Version, Platform: clientver.Platform(),
			Err: err.Error(),
		})
	}
	go runVersionProofLoop(ctx, c.buildCloudSyncTransport, c.router.LoggedIn)
}
