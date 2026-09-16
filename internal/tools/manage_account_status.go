// SPDX-License-Identifier: Apache-2.0

// manage_account_status.go — the ACCOUNT line manage(status) renders for the
// human arm, for whichever request is asking.
//
// STATUS IS PER-CALL, NOT PER-PROCESS. Two agents talking to one daemon can be
// routed to two different accounts, so "which account am I using?" has no
// process-wide answer: it is resolved on the calling request's own context,
// through the same ladder every read resolves through. A status render that
// reported the machine-wide selection would tell a bound session the opposite
// of where its writes are going.
//
// THE JSON ARMS ARE addRequestAccountJSON's (manage_local_daemon.go); this is
// the text twin, and the two read the same ladder so they cannot drift. The
// SESSION half of the line is rendered by renderHarnessSessionText, which owns
// that fact for every arm — this function says nothing about the session.

package tools

import (
	"context"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// renderAccountStatusText renders the calling request's account and the rung
// that decided it, plus anything that needs explaining: a header that did not
// take, a ladder refusal, or a binding store that cannot be read.
func renderAccountStatusText(ctx context.Context, deps ClientDeps) string {
	account, source, err := auth.RequestAccount(ctx)
	reason := graphclient.RequestAccountReason(ctx)
	if err != nil {
		account, source, reason = "", auth.AccountSourceNone, err.Error()
	}
	line := "\n  Account: " + account + " (" + string(source) + ")"
	if account == "" {
		line = "\n  Account: none — the gateway resolves your primary account (" + string(source) + ")"
	}
	if reason != "" {
		line += " — " + reason
	}
	// The machine-wide selection beside it, always: see addRequestAccountJSON's
	// account_global for why the effective account alone does not answer the
	// question a user asks here.
	global := auth.SelectedAccount().ID(ctx)
	if global == "" {
		global = "none"
	}
	line += "\n  Global selection: " + global
	if store := sessionAccountStore(deps); store != nil {
		if storeErr := store.Check(); storeErr != nil {
			line += "\n  Session bindings are NOT in effect: " + storeErr.Error()
		}
	}
	return line
}
