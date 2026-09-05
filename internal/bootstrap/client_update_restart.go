// SPDX-License-Identifier: Apache-2.0

// client_update_restart.go — the CHILD half of the update restart handoff: the
// `restart-daemon` verb the newly-installed binary runs.
//
// The PARENT half — which binary is spawned, in what process group, and through
// which cgroup-escaping launcher — lives beside the loop that drives it, in
// client_update_check.go, because the survival hazards it addresses are
// properties of the spawning daemon rather than of the verb.

package bootstrap

import (
	"flag"
	"fmt"
	"log/slog"
)

// restartDaemonVerb is the subcommand the handoff child runs.
const restartDaemonVerb = "restart-daemon"

// restartTargetVersionFlag is the flag carrying the version the restarted
// daemon must report.
const restartTargetVersionFlag = "target-version"

// installServiceUnitsAndRestartFn is the seam the restart-daemon verb reaches
// its work through, so a test can observe the verb being dispatched with the
// exact tag without restarting anything.
//
//nolint:gochecknoglobals // overridable restart seam for testability.
var installServiceUnitsAndRestartFn = installServiceUnitsAndRestart

// runRestartDaemon implements `knowledge restart-daemon --target-version <tag>`.
//
// It is deliberately NOT `knowledge setup --no-self-update`. Setup also resolves
// the config path, may write config, and installs the Claude/Codex assets, none
// of which an unattended restart should perform. This verb does exactly one
// thing: install the persistence units and restart the daemons to the target
// version, identity assertion included.
//
// AN EMPTY --target-version IS REFUSED rather than passed through. The restart's
// identity check compares the restarted daemon's reported version against this
// value, so an empty target turns a guarantee into a comparison no live daemon
// can satisfy — the assertion inverts into a guaranteed failure.
//
// graphStorage IS A PARAMETER, NOT SOMETHING THIS FUNCTION RESOLVES, and that is
// a containment decision rather than a style one. This verb installs THREE
// pieces of process-global state — the default slog handler, the crash-output
// destination and the SIGPIPE disposition — all keyed off a log path under that
// directory. It is also a plain function the package's own tests call directly.
// When it resolved the directory from the ambient $HOME, one run of the client
// bootstrap suite appended 505 lines to the OPERATOR'S live daemon log,
// including records of upgrades that never happened, because slog.SetDefault
// then routed the whole suite there. Taking the directory from the caller means
// a test cannot reach the live path BY OMISSION: an unnamed directory is an
// error, never a fallback to $HOME.
func runRestartDaemon(args []string, graphStorage string) error {
	fs := flag.NewFlagSet("knowledge "+restartDaemonVerb, flag.ContinueOnError)
	target := fs.String(restartTargetVersionFlag, "", "Version the restarted daemon must report. Required — the restart asserts the daemon that comes back reports exactly this version, and an empty value makes that assertion unsatisfiable.")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *target == "" {
		return fmt.Errorf("knowledge %s: --%s is required", restartDaemonVerb, restartTargetVersionFlag)
	}
	if graphStorage == "" {
		return fmt.Errorf("knowledge %s: no graph storage directory was given, so this process has nowhere to record the upgrade's outcome; the caller resolves it", restartDaemonVerb)
	}

	// THIS PROCESS IS A DETACHED CHILD OF A DAEMON IT IS ABOUT TO STOP, so it
	// inherits that daemon's stderr and must outlive it. Two consequences, both
	// handled by the daemon's own logging setup: SIGPIPE is ignored, so the
	// stream's reader going away mid-restart costs the stream instead of the
	// process and the upgrade completes; and the daemon log file becomes this
	// process's durable sink. Without it a restart handoff dies exactly where the
	// upgrade has already swapped the binaries and not yet restarted anything.
	//
	// THE DURABLE SINK IS A PRECONDITION HERE, not a nicety, and this is the one
	// caller that treats it that way. The whole reason this process configures
	// logging at all is that its outcome must survive a stderr that may already
	// be dead; continuing without the file would leave an unattended upgrade able
	// to succeed or fail with the record reaching nobody.
	var lvl slog.LevelVar // defaults to Info
	logPath := daemonLogPath(graphStorage)
	if setupLogging(&Config{LogFile: logPath}, &lvl) == nil {
		return fmt.Errorf("knowledge %s: could not open the durable log %s, so this upgrade's outcome would be recorded nowhere", restartDaemonVerb, logPath)
	}

	// THE OUTCOME IS RECORDED DURABLY, not only returned. This process's caller
	// prints a returned error to stderr — the stream that may be dead — and an
	// unattended upgrade whose failure reached nobody is the sink-nobody-reads
	// defect in its most expensive form.
	if err := installServiceUnitsAndRestartFn(*target); err != nil {
		slog.Error("restart handoff failed", "target_version", *target, "err", err)
		return err
	}
	slog.Info("restart handoff completed", "target_version", *target)
	return nil
}
