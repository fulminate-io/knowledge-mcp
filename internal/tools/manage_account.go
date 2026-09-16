// SPDX-License-Identifier: Apache-2.0

// manage_account.go — the two account operations the daemon serves:
// account_for_session (bind THIS call's harness session to an account) and
// account_use (move the machine-wide selection).
//
// NEITHER TAKES A SESSION ARGUMENT. account_for_session binds the session the
// call arrived on and no other: a session id on the wire would let one agent
// retarget another agent's session, and the published schema declares no such
// property, so a call carrying one is refused before dispatch by
// rejectUndeclaredParams. When no harness session resolves, the operation FAILS
// and binds nothing — the owner's rule is that it must fail if the hook is not
// working, because binding "the session that happens to be asking" when nobody
// knows which session that is routes a user's writes into an account they did
// not choose.
//
// THE SUBSCRIPTION ASYMMETRY BETWEEN THEM IS DELIBERATE. account_use is the CLI
// flow reused whole, and that flow has always refused an account with no active
// subscription; account_for_session is new and adds no subscription check of its
// own, because the gateway refuses every cloud call for such an account
// server-side and is the authority for both.

package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/cli"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// lookupAccountFn and useAccountFn are the two cli entry points these arms call,
// held as package seams so an arm test can drive the dispatch, the refusals and
// the binding without a live gateway on the machine. Production holds the cli
// functions themselves; the seam test that runs the REAL membership decoder and
// the REAL write flow against an httptest gateway lives in package cli, where the
// endpoint and the config path are the swappable things.
//
//nolint:gochecknoglobals // test seams, mirroring syncTransportBuilder in this package.
var (
	lookupAccountFn = cli.LookupAccount
	useAccountFn    = cli.UseAccount
)

// accountArmAudit is the ordered log of OUTBOUND actions the account arms take.
// Production leaves it nil and records nothing; a test installs a sink and
// asserts the arm's WHOLE action list, which is what turns "nothing is restarted
// and nothing is spawned" into an observation rather than a claim: the assertion
// is over the entire list, so an action that is not in it fails whatever it is.
//
// IT COVERS THE SANCTIONED PATH ONLY, and says so. An author who adds a restart
// through a seam records it here and is caught by the list; an author who calls
// exec.Command directly records nothing, and is caught by the two gates that
// watch the source instead — TestAccountArms_SpawnNoProcess below, which parses
// this file in the suite, and the corpus check that covers the same shape
// durably. Three gates because the defect they guard against is a daemon
// signaling its own pid on every account switch.
//
//nolint:gochecknoglobals // test seam, mirroring syncTransportBuilder in this package.
var accountArmAudit func(action string)

// auditAccountArm records one outbound action. The names are the contract the
// test asserts against, so they change only with the action they name.
func auditAccountArm(action string) {
	if accountArmAudit != nil {
		accountArmAudit(action)
	}
}

// sessionAccountBinder is the local view of ClientDeps that the account arms use
// to reach the daemon's per-session binding store. Declared here rather than on
// ClientDeps for the SAME reason as pipelineMetricser (manage.go:20-28): the
// existing ClientDeps test fakes must not each grow a stub. Production *client
// satisfies it structurally; the handlers error rather than degrade when the
// type-assert misses, because an unbindable session is not a session bound to
// the machine-wide account.
type sessionAccountBinder interface {
	SessionAccountBindings() *SessionAccountStore
}

// sessionAccountStore resolves the binding store from deps.
func sessionAccountStore(deps ClientDeps) *SessionAccountStore {
	binder, ok := deps.(sessionAccountBinder)
	if !ok {
		return nil
	}
	return binder.SessionAccountBindings()
}

// handleAccountManage serves the two account operations, which take the same one
// argument and differ only in what they do with it.
//
// THEY ARE ONE ARM RATHER THAN TWO CASES for the reason stated on
// handlePipelineLifecycleManage: InterceptManage's dispatch is bounded by a
// statement count, and "the account operations" is the distinction a reader of
// that switch needs.
func handleAccountManage(ctx context.Context, deps ClientDeps, a manageArgs) kgtools.ToolResult {
	if a.Operation == "account_use" {
		return handleAccountUse(ctx, deps, a)
	}
	return handleAccountForSession(ctx, deps, a)
}

// handleAccountForSession binds the harness session THIS call arrived on to an
// account the caller is a member of.
func handleAccountForSession(ctx context.Context, deps ClientDeps, a manageArgs) kgtools.ToolResult {
	account := strings.TrimSpace(a.Account)
	if account == "" {
		return errorResult("manage(account_for_session): account is required — pass the id or slug of the account this session's calls should use (run `knowledge accounts` to list them)")
	}
	store := sessionAccountStore(deps)
	if store == nil {
		return errorResult("manage(account_for_session): the per-session binding store is unavailable — this client was constructed without a data root, so no binding can be recorded")
	}
	harness := auth.ResolveHarnessSession(ctx)
	if !harness.Resolved() {
		return errorResult("manage(account_for_session): " + harness.RefusalText())
	}
	auditAccountArm("membership-lookup")
	matched, err := lookupAccountFn(ctx, account)
	if err != nil {
		return errorResult("manage(account_for_session): " + err.Error())
	}
	auditAccountArm("binding-write")
	replaced, err := store.Bind(harness.ID, matched.ID)
	if err != nil {
		return errorResult("manage(account_for_session): " + err.Error())
	}
	return renderAccountForSession(a.Format, harness, matched, replaced)
}

// renderAccountForSession reports what was bound, in either format.
//
// IT REPORTS A REPLACED STORE, because that is a fact about OTHER callers. The
// documented recovery from a corrupt binding file is to write over it, and this
// call is what performs it — every other session's binding went with it and
// those sessions now resolve to the machine-wide account until they re-bind.
// Saying so here is the only notice they get: manage(status)'s
// session_binding_store_error, the surface that showed the corruption, is clean
// from this moment on.
func renderAccountForSession(
	format string, harness auth.HarnessSession, matched cli.AccountMembership, replaced bool,
) kgtools.ToolResult {
	if format == "json" {
		return jsonResult(map[string]any{
			"bound":          true,
			"account":        matched.ID,
			"account_slug":   matched.Slug,
			"session":        harness.ID,
			"session_source": string(harness.Source),
			"idle_ttl":       SessionAccountBindingTTL.String(),
			"store_replaced": replaced,
		})
	}
	body := fmt.Sprintf(
		"This session now uses account %s (%s).\n  Session: %s (via %s)\n  The binding lasts until it is overwritten, cleared, or %s idle.\n  Other sessions and the machine-wide default are unchanged.",
		matched.Slug, matched.ID, harness.ID, harness.Source, SessionAccountBindingTTL)
	if replaced {
		body = fmt.Sprintf(
			"This session now uses account %s (%s).\n  Session: %s (via %s)\n  The binding lasts until it is overwritten, cleared, or %s idle.\n"+
				"  The binding store was UNREADABLE and has been replaced: every other session's binding was discarded, and those sessions\n"+
				"  resolve to the machine-wide default until they bind again.",
			matched.Slug, matched.ID, harness.ID, harness.Source, SessionAccountBindingTTL)
	}
	return textResult(body)
}

// handleAccountUse moves the MACHINE-WIDE selection, the same write
// `knowledge account use` performs, and returns.
//
// NOTHING IS RESTARTED. The daemon follows the new selection on its own: the
// account selection re-reads the config file once its cache window elapses
// (auth.DefaultAccountCheckTTL), and the per-account cloud clients and segment
// children are built on demand, so the next request after that window binds the
// new account in this same process.
func handleAccountUse(ctx context.Context, _ ClientDeps, a manageArgs) kgtools.ToolResult {
	account := strings.TrimSpace(a.Account)
	if account == "" {
		return errorResult("manage(account_use): account is required — pass the id or slug of the account this machine should use (run `knowledge accounts` to list them)")
	}
	// The ONLY outbound action this arm takes. There is deliberately no second
	// one: no restart, no respawn, no signal. The daemon follows the new
	// selection through its own cache window, and a restart issued from INSIDE
	// the daemon would resolve the daemon-port owner and signal this very
	// process.
	auditAccountArm("selection-write")
	var confirmation strings.Builder
	selected, err := useAccountFn(ctx, &confirmation, account)
	if err != nil {
		return errorResult("manage(account_use): " + err.Error())
	}
	if a.Format == "json" {
		return jsonResult(map[string]any{
			"account":          selected,
			"restart_required": false,
			"follows_within":   auth.DefaultAccountCheckTTL.String(),
			"message":          strings.TrimSpace(confirmation.String()),
		})
	}
	return textResult(fmt.Sprintf(
		"%s\n  No restart: this daemon picks the new default up within %s. Sessions with their own binding keep it.",
		strings.TrimSpace(confirmation.String()), auth.DefaultAccountCheckTTL))
}
