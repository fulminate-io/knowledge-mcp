// SPDX-License-Identifier: Apache-2.0

// setup_service.go — persistence-unit generation + load for
// `knowledge setup`. installServiceUnits dispatches on an injectable
// GOOS to the launchd (macOS) or systemd --user (Linux) arm, or the
// no-op Windows arm, and returns a serviceOutcome that steers the
// restart tail (setup_restart.go): a loaded unit, a bare-process
// fallback (degrade / Windows), or a brew defer. All availability /
// load failures degrade-with-note and return the bare-process sentinel
// — `knowledge setup` never hard-fails on a service-load hiccup.

package bootstrap

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// serviceOutcome tells the restart tail how the units were handled.
type serviceOutcome int

const (
	// outcomeUnit: a persistence unit was written AND loaded; the restart
	// tail kickstarts the units (validated by the real-box manual smoke).
	outcomeUnit serviceOutcome = iota
	// outcomeBare: no unit was loaded (degrade-with-note or Windows); the
	// restart tail forks bare processes (spawnServer + spawnDaemonProcess).
	outcomeBare
	// outcomeBrew: brew services owns the lifecycle; the restart tail
	// defers entirely.
	outcomeBrew
)

// Unit labels/names. The io.fulminate.* launchd prefix MUST stay
// distinct from brew's homebrew.mxcl.knowledge so a script-managed
// install never collides with a brew-managed one.
const (
	launchdDaemonLabel = "io.fulminate.knowledge"
	launchdServerLabel = "io.fulminate.knowledge-server"
	systemdDaemonUnit  = "knowledge.service"
	systemdServerUnit  = "knowledge-server.service"
)

// serviceGOOS is the injectable GOOS dispatch seam (mirrors the goos
// threading in install_e2e_test.go) so tests can drive the darwin /
// linux / windows arms deterministically on any CI host.
var serviceGOOS = runtime.GOOS

// serverOwnerFn resolves the owner label of the running 15022 server so
// the service + restart legs can defer to brew. Seam for tests.
var serverOwnerFn = func() string {
	pid := launchctlPID()
	if pid <= 0 {
		return ""
	}
	owner, _ := identifyServerOwner(pid)
	return owner
}

// launchctl / systemctl seams — stubbable so setup_service_test.go can
// force a bootstrap failure or a bus-unavailable state deterministically.
var (
	runLaunchctl = func(args ...string) error { return exec.Command("launchctl", args...).Run() }

	runSystemctlUser = func(args ...string) error {
		return exec.Command("systemctl", append([]string{"--user"}, args...)...).Run()
	}

	enableLinger = func() error {
		return exec.Command("loginctl", "enable-linger", os.Getenv("USER")).Run()
	}

	// userSystemdAvailable reports whether the systemd --user manager is
	// actually running (so daemon-reload + enable --now will work).
	userSystemdAvailable = func() bool {
		out, _ := exec.Command("systemctl", "--user", "is-system-running").CombinedOutput()
		return systemdManagerLive(string(out))
	}
)

// systemdManagerLive classifies `systemctl --user is-system-running`
// output: ONLY the states where a user manager is genuinely running
// count as available. A headless box with no user manager reports
// "offline" (NOT "Failed to connect to bus"), a box with no session bus
// reports "Failed to connect to bus", and systemctl-absent yields empty
// output — all three mean "no usable manager", so setup must degrade to
// the bare-process fallback rather than attempt (and hard-fail on)
// daemon-reload. Matching only "not a bus failure" was too loose: it
// mis-classified "offline" as available and hard-failed on daemon-reload.
func systemdManagerLive(out string) bool {
	switch strings.TrimSpace(out) {
	case "running", "degraded", "starting", "maintenance", "stopping":
		return true
	default:
		// offline / unknown / "Failed to connect to bus" / empty.
		return false
	}
}

// isBrewOwned reports whether the running server is brew-managed.
func isBrewOwned() bool { return strings.Contains(serverOwnerFn(), "brew") }

// installServiceUnitsAndRestart is the runSetup service/restart tail:
// install persistence units, then restart the daemons to targetVersion.
// Gated behind --no-service in runSetup.
func installServiceUnitsAndRestart(targetVersion string) error {
	// Free 15022/15023 of any PRE-EXISTING listeners (a bare `knowledge
	// serve` dev daemon, a manually-started server, or a stale unit
	// instance) BEFORE loading the units — otherwise a survivor keeps the
	// port, the freshly-loaded unit can't bind, and the version-identity
	// check sees the OLD daemon and false-fails the whole setup. Skipped
	// under brew ownership (brew owns the lifecycle — never fight it).
	if !isBrewOwned() {
		stopPreExistingListeners()
	}
	outcome, err := installServiceUnits()
	if err != nil {
		return err
	}
	return restartSequence(targetVersion, outcome)
}

// installServiceUnits writes + loads the persistence units for the
// running platform. Brew-managed installs skip entirely (brew owns the
// lifecycle); every availability / load failure degrades-with-note and
// returns outcomeBare so the restart tail forks bare processes.
func installServiceUnits() (serviceOutcome, error) {
	if isBrewOwned() {
		fmt.Fprintln(os.Stdout, "knowledge setup: brew services manages the daemon; skipping unit install")
		return outcomeBrew, nil
	}
	switch serviceGOOS {
	case "darwin":
		return installLaunchdUnits()
	case "linux":
		return installSystemdUnits()
	default:
		fmt.Fprintln(os.Stdout, "knowledge setup: no boot-persistence service on Windows; the daemon runs for this session")
		return outcomeBare, nil
	}
}

// installLaunchdUnits writes the two per-user plists to
// ~/Library/LaunchAgents and loads them via launchctl bootout (ignore
// not-loaded) + bootstrap. A bootstrap failure (e.g. an SSH-only session
// with no GUI bootstrap domain) degrades-with-note and returns
// outcomeBare — never a hard failure.
func installLaunchdUnits() (serviceOutcome, error) {
	clientBin, serverBin, logDir, err := resolveServiceBins()
	if err != nil {
		return outcomeBare, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return outcomeBare, fmt.Errorf("resolve home directory: %w", err)
	}
	agentsDir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(agentsDir, 0o750); err != nil {
		return outcomeBare, fmt.Errorf("mkdir %s: %w", agentsDir, err)
	}

	units := []struct {
		label string
		body  string
	}{
		{launchdDaemonLabel, renderLaunchdPlist(launchdDaemonLabel, clientBin, []string{"serve", "--http-port", strconv.Itoa(graphclient.DefaultMCPHTTPPort)}, logDir)},
		{launchdServerLabel, renderLaunchdPlist(launchdServerLabel, serverBin, []string{"--port", strconv.Itoa(graphclient.DefaultPort)}, logDir)},
	}
	uid := os.Getuid()
	for _, u := range units {
		plistPath := filepath.Join(agentsDir, u.label+".plist")
		if err := os.WriteFile(plistPath, []byte(u.body), 0o644); err != nil { //nolint:gosec // LaunchAgent plists carry no secrets and are conventionally world-readable
			return outcomeBare, fmt.Errorf("write plist %s: %w", plistPath, err)
		}
		_ = runLaunchctl("bootout", fmt.Sprintf("gui/%d/%s", uid, u.label)) // ignore not-loaded
		if err := runLaunchctl("bootstrap", fmt.Sprintf("gui/%d", uid), plistPath); err != nil {
			// DELIBERATE degrade: a bootstrap failure (e.g. no GUI domain in
			// an SSH session) is NOT a hard failure — print the note and
			// signal bare-process fallback so setup still exits zero.
			fmt.Fprintln(os.Stdout, "knowledge setup: launchd LaunchAgent load failed — log in to a graphical session and re-run `knowledge setup` for boot persistence")
			return outcomeBare, nil //nolint:nilerr // intentional degrade-with-note, not error propagation
		}
	}
	return outcomeUnit, nil
}

// installSystemdUnits writes the two --user units and enables them.
// When the user-manager D-Bus is unavailable (a headless SSH box), it
// attempts loginctl enable-linger and retries the probe ONCE; if still
// unavailable it degrades-with-note and returns outcomeBare — never a
// hard failure.
func installSystemdUnits() (serviceOutcome, error) {
	if !userSystemdAvailable() {
		_ = enableLinger()
		if !userSystemdAvailable() {
			fmt.Fprintln(os.Stdout, "knowledge setup: systemd user manager unavailable — run `loginctl enable-linger <user>` then re-run `knowledge setup` for boot persistence")
			return outcomeBare, nil
		}
	}

	clientBin, serverBin, _, err := resolveServiceBins()
	if err != nil {
		return outcomeBare, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return outcomeBare, fmt.Errorf("resolve home directory: %w", err)
	}
	unitDir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(unitDir, 0o750); err != nil {
		return outcomeBare, fmt.Errorf("mkdir %s: %w", unitDir, err)
	}

	units := []struct {
		name string
		body string
	}{
		{systemdDaemonUnit, renderSystemdUnit("knowledge daemon (MCP HTTP)", clientBin, []string{"serve", "--http-port", strconv.Itoa(graphclient.DefaultMCPHTTPPort)})},
		{systemdServerUnit, renderSystemdUnit("knowledge graph server", serverBin, []string{"--port", strconv.Itoa(graphclient.DefaultPort)})},
	}
	for _, u := range units {
		if err := os.WriteFile(filepath.Join(unitDir, u.name), []byte(u.body), 0o644); err != nil { //nolint:gosec // systemd unit files carry no secrets and are conventionally world-readable
			return outcomeBare, fmt.Errorf("write unit %s: %w", u.name, err)
		}
	}
	if err := runSystemctlUser("daemon-reload"); err != nil {
		return outcomeBare, fmt.Errorf("systemctl --user daemon-reload: %w", err)
	}
	for _, u := range units {
		if err := runSystemctlUser("enable", "--now", u.name); err != nil {
			return outcomeBare, fmt.Errorf("systemctl --user enable --now %s: %w", u.name, err)
		}
	}
	return outcomeUnit, nil
}

// renderLaunchdPlist renders a per-user LaunchAgent plist with RunAtLoad
// + KeepAlive and the given ProgramArguments (absolute binary path
// first). StandardOut/Error land under logDir.
func renderLaunchdPlist(label, bin string, args []string, logDir string) string {
	var pa strings.Builder
	pa.WriteString("\t\t<string>" + bin + "</string>\n")
	for _, a := range args {
		pa.WriteString("\t\t<string>" + a + "</string>\n")
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + label + `</string>
	<key>ProgramArguments</key>
	<array>
` + pa.String() + `	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>StandardOutPath</key>
	<string>` + filepath.Join(logDir, label+".out.log") + `</string>
	<key>StandardErrorPath</key>
	<string>` + filepath.Join(logDir, label+".err.log") + `</string>
</dict>
</plist>
`
}

// renderSystemdUnit renders a --user service unit. ExecStart is the
// absolute binary path followed by args; Restart=always;
// WantedBy=default.target so `systemctl --user enable` wires boot start.
func renderSystemdUnit(desc, bin string, args []string) string {
	execLine := strings.Join(append([]string{bin}, args...), " ")
	return "[Unit]\nDescription=" + desc + "\nAfter=network.target\n\n" +
		"[Service]\nType=simple\nExecStart=" + execLine + "\nRestart=always\nRestartSec=2\n\n" +
		"[Install]\nWantedBy=default.target\n"
}

// resolveServiceBins returns the absolute client + server binary paths
// and the log directory for the unit writers. The client is the running
// binary (symlinks resolved so a Homebrew-style symlink points at the
// real Cellar location); the server is findServerBinary's result.
func resolveServiceBins() (clientBin, serverBin, logDir string, err error) {
	exe, err := getExecutable()
	if err != nil {
		return "", "", "", fmt.Errorf("resolve client binary: %w", err)
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}
	server, err := findServerBinary()
	if err != nil {
		return "", "", "", fmt.Errorf("locate knowledge-server: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", "", fmt.Errorf("resolve home directory: %w", err)
	}
	return exe, server, filepath.Join(home, ".knowledge"), nil
}
