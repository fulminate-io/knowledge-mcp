// SPDX-License-Identifier: Apache-2.0

// sync_transport.go — the single sync-transport builder shared by graph sync
// (push/pull), the segment control plane (segmentdist manifest/read), and the
// standalone transcript-upload subcommand. It constructs the *auth.Transport the
// control-plane POSTs go over. A present KNOWLEDGE_AUTH_TOKEN machine bearer is
// honored FIRST (headless/automated path); otherwise it wires the interactive OAuth
// TokenSource off the local keychain store. The Fulminate cloud host is hardcoded
// per build tag (CloudEndpoint); there is no runtime override.

package cli

import (
	"fmt"
	"os"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/clientver"
)

// BuildSyncTransport constructs the sync Transport used by every control-plane POST
// (graph sync AND the segmentdist manifest/read path).
//
// A present KNOWLEDGE_AUTH_TOKEN machine bearer is honored FIRST: a headless daemon
// (e.g. the executor sandbox) has NO keychain/dbus, so the OAuth-refresh path cannot
// serve a token even though the machine token is set — which is exactly what stranded
// the sandbox knowledge search at segmentdist manifest/read. A missing keychain now
// resolves to the file-backed credential store rather than erroring outright, but a
// sandbox has never run an interactive login, so that store is empty and the path
// still fails. This mirrors bootstrap.selectAuthSources: a present machine token
// bypasses the credential store entirely and presents the opaque bearer directly. It
// reads the SAME env var as the --auth-token flag default (config.go).
//
// With no machine token it falls back to the OAuth store for the interactive-login
// path, returning an actionable error when the credential store cannot be opened so
// the caller can surface the "run knowledge login" guidance — a nil Transport with a
// nil error is never returned.
// PROVE-ON-REFUSAL IS WIRED HERE, AT THE CONSTRUCTOR, and on BOTH return paths.
// This is the single builder every CLI consumer goes through, so wiring it here
// covers `knowledge accounts`, `knowledge account use`, `knowledge
// transcript-upload` and any future consumer by construction; wiring it
// per-command would have to be repeated at each one and would silently miss the
// next. Enabling it on both returns is the part a partial edit gets wrong — the
// machine-bearer path is exactly the population LEAST likely to have a daemon
// running, and therefore the one most in need of proving for itself.
func BuildSyncTransport() (*auth.Transport, error) {
	// Open the executable handle the possession proof reads from. It is
	// idempotent, so a process that also runs the daemon wiring opens nothing
	// twice, and taking it at construction keeps ONE discipline across the
	// daemon and the CLI: the descriptor comes from the binary the process was
	// launched from, which is what makes the proof describe the version the
	// process claims.
	//
	// A FAILURE HERE DOES NOT FAIL CONSTRUCTION. A command that never touches
	// the cloud, or one whose account already has a live verification record,
	// must keep working on a machine where the executable cannot be reopened.
	// The failure is recorded so the status renderers show it, and the eventual
	// refusal — if one comes — carries the user-facing message. Failing here
	// would convert a proof-capability problem into a total CLI outage.
	if err := clientver.OpenSelf(); err != nil {
		clientver.RecordProof(clientver.ProofState{
			OK:      false,
			Version: clientver.Version, Platform: clientver.Platform(),
			Err: err.Error(),
		})
	}
	prove := auth.WithProveOnRefusal(clientver.AnswerChallenge)

	if tok := os.Getenv("KNOWLEDGE_AUTH_TOKEN"); tok != "" {
		return auth.NewSyncTransport(CloudEndpoint, auth.StaticTokenSource{AccessToken: tok}, prove), nil
	}
	store, err := auth.OpenStore()
	if err != nil {
		return nil, fmt.Errorf("sync requires login — credential store unavailable: %w", err)
	}
	ts := auth.NewOAuthTokenSource(store, CloudEndpoint, AllowedAuthHosts())
	return auth.NewSyncTransport(CloudEndpoint, ts, prove), nil
}
