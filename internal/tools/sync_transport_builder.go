// SPDX-License-Identifier: Apache-2.0

// sync_transport_builder.go — the graph-sync entry point into the shared OAuth
// sync-transport builder. The wiring now lives in cli.BuildSyncTransport (the
// single keychain transport builder graph sync push/pull AND the standalone
// transcript-upload subcommand both delegate to); this thin forwarder preserves
// the package-tools call site and the syncTransportBuilder test-swap var.

package tools

import (
	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/cli"
)

// buildSyncTransport delegates to cli.BuildSyncTransport — the single OAuth
// sync-transport builder. It returns the same actionable "keychain unavailable —
// run knowledge login" error when no credential store is present.
func buildSyncTransport() (*auth.Transport, error) {
	return cli.BuildSyncTransport()
}

// SetSyncTransportBuilder installs the production sync-transport builder that
// the push/pull intercepts resolve their Transport through, replacing the
// default per-call cli.BuildSyncTransport with a factory over the daemon's
// SINGLE shared cloud token source (bootstrap.buildCloudSyncTransport).
//
// CONCURRENCY: syncTransportBuilder is a plain package func-value read by
// handler goroutines at InterceptSync push/pull. This setter MUST be called
// SYNCHRONOUSLY before the MCP listener binds (from bootstrap.buildClient,
// pre-ListenAndServe) — never from the background wiring goroutine — so the
// single write happens-before every handler read and no atomic/mutex is
// required. A nil fn is ignored so the default fallback is never cleared.
func SetSyncTransportBuilder(fn func() (*auth.Transport, error)) {
	if fn != nil {
		syncTransportBuilder = fn
	}
}
