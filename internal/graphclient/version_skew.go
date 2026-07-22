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
