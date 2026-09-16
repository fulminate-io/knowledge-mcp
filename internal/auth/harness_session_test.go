// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHarnessSession_ContextCarrierIsTheOnlyResolver pins what this package's
// consumption seam answers with.
//
// THE PRODUCER HAS LANDED: harnessSessionResolver forwards to
// session.HarnessSessionFromContext. The first sub-test now pins the honest
// default — a request carrying NO stamped session resolves nothing and reports
// the carrier-naming reason, which is what makes manage(account_for_session)
// refuse when the hook is not firing. The production forwarding itself is
// observed by the status rows in package tools, which drive a stamped context
// and read the session back out of the rendered body.
func TestHarnessSession_ContextCarrierIsTheOnlyResolver(t *testing.T) {
	t.Run("a context with no stamped session resolves nothing", func(t *testing.T) {
		got := ResolveHarnessSession(context.Background())
		assert.False(t, got.Resolved(), "an unstamped context carries no session, so nothing can resolve")
		assert.Equal(t, HarnessSourceNone, got.Source)
		assert.Equal(t, HarnessReasonNotResolved, got.Reason)
		assert.Contains(t, got.RefusalText(), UnresolvedHarnessSessionReason,
			"what a user is shown always carries the carrier-naming sentence")
		assert.Contains(t, got.RefusalText(), HarnessReasonNotResolved,
			"and the producer's own machine-readable reason beside it")
	})

	t.Run("the unresolved reason names every carrier a user can install", func(t *testing.T) {
		// Read off the contract's own text rather than re-asserting a copy of it:
		// the requirement is that the reason names each carrier, so a producer
		// that adds a fourth carrier must extend this sentence.
		for _, carrier := range []string{"PreToolUse hook", "Codex", "Knowledge-Session-Id"} {
			assert.Contains(t, UnresolvedHarnessSessionReason, carrier)
		}
		assert.True(t, strings.HasPrefix(UnresolvedHarnessSessionReason, "no harness session on this call"))
	})

	t.Run("a resolved session reports its id and carrier unchanged", func(t *testing.T) {
		defer SetHarnessSessionResolverForTest(func(context.Context) HarnessSession {
			return HarnessSession{ID: "harness-123", Source: HarnessSourceCodexMeta}
		})()
		got := ResolveHarnessSession(context.Background())
		require.True(t, got.Resolved())
		assert.Equal(t, "harness-123", got.ID)
		assert.Equal(t, HarnessSourceCodexMeta, got.Source)
	})

	t.Run("an empty id is never resolved whatever the carrier says", func(t *testing.T) {
		defer SetHarnessSessionResolverForTest(func(context.Context) HarnessSession {
			return HarnessSession{Source: HarnessSourceHeader}
		})()
		assert.False(t, ResolveHarnessSession(context.Background()).Resolved(),
			"Resolved() is the id being present, never the carrier claiming one")
	})
}
