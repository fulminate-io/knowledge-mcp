// SPDX-License-Identifier: Apache-2.0

// account_restart.go — the automatic daemon restart that makes an account
// switch take effect with no manual step from the user.
//
// The long-lived daemon builds its segment L2 cache under a root partitioned
// by the selected account and refuses to serve after the selection moves
// (fail-closed, the backstop). Restarting it here is what keeps that design
// invisible: the user runs `knowledge account use` and the switch is live.
//
// It lives in bootstrap, not cli, for two reasons: bootstrap imports cli, so
// cli cannot reach the lifecycle primitives; and process lifecycle is
// bootstrap's job, which keeps the CLI commands pure config-and-network.

package bootstrap

import (
	"fmt"
	"os"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// storedSelection reads the account selection from the default config path.
// Answers "" for every failure — an unreadable config is indistinguishable
// from no selection for the purpose of "did it move?", and this must never
// fail a command.
func storedSelection() string {
	path, err := config.DefaultPath()
	if err != nil {
		return ""
	}
	id, err := config.ReadSelectedAccountID(path)
	if err != nil {
		return ""
	}
	return id
}

// restartDaemonIfSelectionChanged restarts a RUNNING daemon when the stored
// account selection has moved off before.
//
// Every outcome is non-fatal: the selection is already persisted by the time
// this runs, so a restart failure prints an actionable line but never turns a
// successful `account use` (or `login`) into a failed command.
//
// It never STARTS a daemon that was not already running — a config write must
// not launch a process — and it never touches the 15022 graph server, which is
// single-tenant, holds no account state, and would interrupt unrelated work.
func restartDaemonIfSelectionChanged(before string) {
	after := storedSelection()
	if before == after {
		return // a no-op `account use <same account>` must not cycle the daemon
	}

	kind, pid := daemonOwner()
	if kind == daemonOwnerNone {
		return
	}

	// Brew owns its daemon's lifecycle; fighting the service manager with a
	// hand-rolled stop/spawn is the one case this cannot do safely, so it says
	// so instead of faking it. Same position restartSequence takes.
	if isBrewOwned() {
		fmt.Fprintln(os.Stdout, "knowledge: brew services owns the daemon; run `brew services restart knowledge` to apply the account switch")
		return
	}

	switch kind {
	case daemonOwnerUnit:
		stopDaemonUnit()
		if err := kickstartUnit(launchdDaemonLabel, systemdDaemonUnit); err != nil {
			fmt.Fprintf(os.Stdout, "knowledge: the account is saved, but the daemon did not restart (%v) — restart it yourself to apply the switch\n", err)
			return
		}
	case daemonOwnerBare:
		if err := signalDaemonStop(pid); err != nil {
			fmt.Fprintf(os.Stdout, "knowledge: the account is saved, but the daemon did not stop (%v) — restart it yourself to apply the switch\n", err)
			return
		}
		// Let the kernel release the port before the spawn tries to bind it,
		// the same wait stopPreExistingListeners uses.
		waitForReady(func() bool { return daemonPIDOnPort(graphclient.DefaultMCPHTTPPort) == 0 }, 5*time.Second)

		graphStorage, err := serviceGraphStorage()
		if err != nil {
			fmt.Fprintf(os.Stdout, "knowledge: the account is saved, but the daemon could not be restarted (%v) — restart it yourself to apply the switch\n", err)
			return
		}
		if err := spawnDaemonProcess(graphStorage); err != nil {
			fmt.Fprintf(os.Stdout, "knowledge: the account is saved, but the daemon did not restart (%v) — restart it yourself to apply the switch\n", err)
			return
		}
	case daemonOwnerNone:
		return // unreachable: handled above, listed so the switch is exhaustive
	}

	// Confirm it came back. No version-identity check: no binary changed here.
	if _, ok := waitForDaemonReady(probeDaemon15023, graphclient.DefaultMCPHTTPPort, restartReadinessTimeout); !ok {
		fmt.Fprintf(os.Stdout, "knowledge: the account is saved, but the daemon did not come back within %s — restart it yourself to apply the switch\n", restartReadinessTimeout)
		return
	}
	fmt.Fprintln(os.Stdout, "knowledge: daemon restarted — the account switch is live")
}
