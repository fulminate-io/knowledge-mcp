// SPDX-License-Identifier: Apache-2.0

package graphclient

import "fmt"

// VersionSkewLine reports whether a KNOWN client version and a KNOWN daemon
// version disagree, and returns the single operator-facing skew line to surface
// when they do. It is the one shared source of the skew wording so the two
// surfaces that render it — manage(status) and `knowledge version` — cannot
// drift apart.
//
// skewed is true ONLY when both stamps are non-empty AND differ:
//   - either side empty  -> unknown, no skew (a failed daemon probe returns "",
//     so a missing daemon version never reads as a mismatch).
//   - equal              -> in sync, no skew (dev-vs-dev is quiet; the "dev"
//     sentinel compares equal to itself so a pair of dev builds stays silent).
//   - differ             -> skew (dev-vs-release fires, as does release-vs-release
//     across an upgrade).
//
// Home is graphclient because both bootstrap (the version subcommand) and tools
// (manage status) import it, and graphclient imports neither — the cycle-safe
// shared home that already owns the daemon-probe port constant. The message is
// backend-neutral: the only instruction is the brew restart a user actually runs.
func VersionSkewLine(clientVer, daemonVer string) (line string, skewed bool) {
	if clientVer == "" || daemonVer == "" {
		return "", false
	}
	if clientVer == daemonVer {
		return "", false
	}
	return fmt.Sprintf(
		"version skew: running daemon %s, installed client %s — restart the daemon (brew services restart knowledge)",
		daemonVer, clientVer), true
}

// ServerBinarySkewLine reports whether a KNOWN client version and a KNOWN
// knowledge-server BINARY version disagree.
//
// It is a SEPARATE function from VersionSkewLine rather than a widening of it,
// because the two answer different questions and their remedies differ. The
// daemon stamp is a RUNNING PROCESS's compiled-in value, so a difference there
// is fixed by restarting that process. The server-binary stamp is a FILE ON
// DISK, so a difference there means the two binaries on this machine came from
// different releases and no restart repairs it — the remedy is re-running the
// install, which is what this line says.
//
// The unknown and equal cases behave exactly as the sibling's: either side
// empty means unknown and never skews (a missing or unreadable server binary
// returns "", so it never reads as a mismatch), and equal is quiet.
//
// Both stamps come from the same release pipeline, injected into each binary's
// own version var from the same tag — which is precisely why a difference
// between them is meaningful and worth surfacing.
func ServerBinarySkewLine(clientVer, serverBinVer string) (line string, skewed bool) {
	if clientVer == "" || serverBinVer == "" {
		return "", false
	}
	if clientVer == serverBinVer {
		return "", false
	}
	return fmt.Sprintf(
		"binary skew: installed knowledge-server %s, installed client %s — the two binaries are from different releases; re-run `knowledge install`",
		serverBinVer, clientVer), true
}
