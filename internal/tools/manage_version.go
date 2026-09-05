// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// versionInfo is the optional view of ClientDeps that manage(status) uses to
// surface the running client + daemon versions and flag a skew between them.
// Declared here (not on ClientDeps) with the SAME structural-typing discipline
// as cloudStatusInfo (manage.go) / transcriptUploadHealther: production *client
// satisfies it structurally, the 18+ existing ClientDeps test fakes do NOT, and
// the render helpers degrade to no version block when the type-assert misses.
//
//   - ClientVersion is the in-process client binary version (bootstrap.Version).
//     Always known — no probe, never empty in production.
//   - DaemonVersion best-effort probes the running local daemon; ok=false on any
//     failure (no daemon, timeout, malformed reply), which degrades to a
//     client-version-only render with NO error.
//   - ServerBinaryVersion best-effort reads the INSTALLED knowledge-server
//     binary's own version from disk; ok=false on any failure (not installed,
//     unreadable, exec error), which degrades to no server-binary line and no
//     binary-skew line. It answers a DIFFERENT question from DaemonVersion —
//     what is ON DISK versus what is RUNNING — and the two have different
//     remedies, which is why they carry separate lines.
type versionInfo interface {
	ClientVersion() string
	DaemonVersion() (string, bool)
	ServerBinaryVersion() (string, bool)
}

// versionSection reads the optional versionInfo, probing the daemon version.
// Returns ("","",false) when the type-assert misses (test fakes) — the additive
// degrade contract mirroring transcriptUploadHealth: no version block renders.
func versionSection(deps ClientDeps) (clientVer, daemonVer string, daemonKnown bool) {
	vi, ok := deps.(versionInfo)
	if !ok {
		return "", "", false
	}
	clientVer = vi.ClientVersion()
	daemonVer, daemonKnown = vi.DaemonVersion()
	return clientVer, daemonVer, daemonKnown
}

// serverBinarySection reads the installed knowledge-server binary's version.
// Returns ("", false) when the type-assert misses or the read failed — the
// same additive degrade contract versionSection carries, so no server-binary
// line and no binary-skew line render.
func serverBinarySection(deps ClientDeps) (string, bool) {
	vi, ok := deps.(versionInfo)
	if !ok {
		return "", false
	}
	return vi.ServerBinaryVersion()
}

// clientVersionOnly reads ONLY the in-process client version, without probing
// the daemon. Used by the "daemon NOT RUNNING" branch, where there is no daemon
// to compare against: the client version is always known in-process, so the
// branch still renders a client-version line (and never a skew line). Returns
// "" when the type-assert misses, so no block renders.
func clientVersionOnly(deps ClientDeps) string {
	if vi, ok := deps.(versionInfo); ok {
		return vi.ClientVersion()
	}
	return ""
}

// renderVersionLines renders the operator-facing version block: a Client version
// line, a Daemon version line (only when the daemon probe succeeded), and the
// shared graphclient.VersionSkewLine when the two known stamps differ. Returns
// "" when clientVer is empty (versionInfo assert miss) so the block is fully
// additive — nothing renders and nothing breaks.
func renderVersionLines(clientVer, daemonVer string, daemonKnown bool, serverBinVer string, serverBinKnown bool) string {
	if clientVer == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nVersion:\n")
	fmt.Fprintf(&b, "  Client version: %s", clientVer)
	if daemonKnown {
		fmt.Fprintf(&b, "\n  Daemon version: %s", daemonVer)
	}
	// The INSTALLED server binary, distinct from the RUNNING daemon above.
	if serverBinKnown {
		fmt.Fprintf(&b, "\n  Server binary version: %s", serverBinVer)
	}
	if line, skewed := graphclient.VersionSkewLine(clientVer, daemonVer); skewed {
		fmt.Fprintf(&b, "\n  %s", line)
	}
	if line, skewed := graphclient.ServerBinarySkewLine(clientVer, serverBinVer); skewed {
		fmt.Fprintf(&b, "\n  %s", line)
	}
	return b.String()
}

// addVersionJSON merges the version fields into the status map so format:json
// carries them too: client_version (when known), daemon_version (only when the
// probe succeeded), and version_skew (the bool flag from the shared skew helper).
// No-op when clientVer is empty (assert miss) so the json body stays unchanged.
func addVersionJSON(m map[string]any, clientVer, daemonVer string, daemonKnown bool, serverBinVer string, serverBinKnown bool) {
	if clientVer == "" {
		return
	}
	m["client_version"] = clientVer
	if daemonKnown {
		m["daemon_version"] = daemonVer
	}
	if serverBinKnown {
		m["server_binary_version"] = serverBinVer
	}
	_, skewed := graphclient.VersionSkewLine(clientVer, daemonVer)
	m["version_skew"] = skewed
	_, binSkewed := graphclient.ServerBinarySkewLine(clientVer, serverBinVer)
	m["server_binary_skew"] = binSkewed
}
