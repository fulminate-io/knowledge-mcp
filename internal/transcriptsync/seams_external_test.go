// SPDX-License-Identifier: Apache-2.0

package transcriptsync_test

import (
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/transcriptsync"
)

// Compile-time proof that the production *auth.Transport satisfies the engine's
// ControlTransport seam directly — the seam is exactly the SyncControlJSON method
// subset pushGraph already drives, so no adapter is needed. If auth.Transport's
// SyncControlJSON signature ever drifts, this assignment stops compiling.
var _ transcriptsync.ControlTransport = (*auth.Transport)(nil)

// TestControlTransport_SatisfiedByAuthTransport keeps the compile-time assertion
// above in a named test so its intent is discoverable; the real check is the
// package-level var, which the compiler enforces.
func TestControlTransport_SatisfiedByAuthTransport(t *testing.T) {
	var _ transcriptsync.ControlTransport = (*auth.Transport)(nil)
}
