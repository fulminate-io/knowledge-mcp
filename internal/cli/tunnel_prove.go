// SPDX-License-Identifier: Apache-2.0

// tunnel_prove.go — the tunnel connect leg's one-shot recovery from a refusal
// that says "this client has no live verification record".
//
// It is split from tunnel.go so the recovery policy sits beside its own doc
// rather than inside the connect flow, mirroring how the sync transport keeps
// version_prove_on_refusal.go separate from sync_transport.go.

package cli

import (
	"context"
	"errors"
	"net/http"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/clientver"
)

// proveVersionOnce runs the SHIPPED possession exchange against the gateway. It is
// a package var so a test can drive the refusal paths without reaching the network;
// production wires the same primitives the other cloud transports use.
//
// There is deliberately no second challenge implementation here: this calls
// Transport.VersionChallenge with clientver.AnswerChallenge, exactly what
// BuildSyncTransport wires for every other CLI command.
var proveVersionOnce = func(ctx context.Context) error {
	t, err := BuildSyncTransport()
	if err != nil {
		return err
	}
	_, err = t.VersionChallenge(ctx, clientver.AnswerChallenge)
	return err
}

// fetchCertProving is fetchCert plus the one-shot prove-and-retry recovery, so a
// tunnel refused for want of a live verification record can fix itself the same way
// every other cloud call path does.
//
// IT TRIGGERS ONLY ON version_unverified, the one refusal a proof repairs. A
// below-minimum client is genuinely too old, an unprovable one has no artifact to
// prove against, and a reason this build has never heard of is by definition one it
// cannot know how to fix — proving on any of them burns a round trip and still
// fails, so they pass straight through with their remedy intact.
//
// ONE ATTEMPT, and a FAILED PROOF SURFACES THE GATEWAY'S REFUSAL rather than the
// proof error: the refusal is what names the minimum and the upgrade command, which
// is what the user can act on. This mirrors the sync transport's recovery
// (auth/version_prove_on_refusal.go) rather than reimplementing its policy.
//
// The CLI is exactly the population that needs this: the daemon proves on a
// background loop, but a short-lived `knowledge tunnel` on a machine that never runs
// one would otherwise be refused forever with a remedy it could not apply.
func fetchCertProving(ctx context.Context, client *http.Client, apiURL, token, publicKey, env string) (cert, relayToken, hostCAPubKey string, err error) {
	cert, relayToken, hostCAPubKey, err = fetchCert(ctx, client, apiURL, token, publicKey, env)

	var refusal *auth.VersionRefusalError
	if !errors.As(err, &refusal) || refusal.Refusal.Reason != clientver.ReasonUnverified {
		return cert, relayToken, hostCAPubKey, err
	}

	if proveErr := proveVersionOnce(ctx); proveErr != nil {
		return "", "", "", err
	}
	return fetchCert(ctx, client, apiURL, token, publicKey, env)
}
