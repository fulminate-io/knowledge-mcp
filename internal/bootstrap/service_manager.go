// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// Existing per-user service files remain authoritative, including while stopped.
// Explicit alternate installations never inspect or control the user's units.
func serviceManager(f serviceOptions) (string, error) {
	home, err := bootstrapHomeDir()
	if err != nil {
		return "", err
	}
	if f.stateDir != filepath.Join(home, ".knowledge") || f.port != graphclient.DefaultPort || f.httpPort != graphclient.DefaultMCPHTTPPort {
		return "Knowledge session", nil
	}
	var paths []string
	var manager string
	switch serviceGOOS {
	case "darwin":
		brew := filepath.Join(home, "Library", "LaunchAgents", "homebrew.mxcl.knowledge.plist")
		if _, err := os.Stat(brew); err == nil {
			return "Homebrew", nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		manager = "launchd"
		for _, label := range []string{launchdDaemonLabel, launchdServerLabel} {
			paths = append(paths, filepath.Join(home, "Library", "LaunchAgents", label+".plist"))
		}
	case "linux":
		manager = "systemd"
		for _, unit := range []string{systemdDaemonUnit, systemdServerUnit} {
			paths = append(paths, filepath.Join(home, ".config", "systemd", "user", unit))
		}
	default:
		return "Knowledge session", nil
	}
	found := 0
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			found++
		} else if !os.IsNotExist(err) {
			return "", err
		}
	}
	if found == 0 {
		return "Knowledge session", nil
	}
	if found != len(paths) {
		return "", fmt.Errorf("incomplete %s service installation: restore the existing Knowledge service files before managing it", manager)
	}
	return manager, nil
}

var serviceCommand = func(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	name = resolveServiceCommand(name)
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

func serviceUnitAction(manager, action, kind string) error {
	var name string
	var args []string
	label, unit := launchdServerLabel, systemdServerUnit
	if kind == "client" {
		label, unit = launchdDaemonLabel, systemdDaemonUnit
	}
	switch manager {
	case "launchd":
		name = "launchctl"
		domain := fmt.Sprintf("gui/%d", os.Getuid())
		target := domain + "/" + label
		var err error
		args, err = launchdServiceArgs(action, label, domain, target)
		if err != nil {
			return err
		}

	case "systemd":
		name = "systemctl"
		args = []string{"--user", action, unit}
	case "Homebrew":
		if kind == "server" {
			if action == "start" {
				return runStart(nil)
			}
			return runStop(nil)
		}
		name = "brew"
		args = []string{"services", action, "knowledge"}
	default:
		return fmt.Errorf("unsupported service manager %q", manager)
	}
	output, err := serviceCommand(name, args...)
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func serviceListenerPID(port int) (int, error) {
	var out []byte
	var err error
	if serviceGOOS == "windows" {
		out, err = serviceCommand("netstat", "-ano", "-p", "tcp")
	} else {
		out, err = serviceCommand("lsof", "-nP", "-ti", fmt.Sprintf("TCP:%d", port), "-sTCP:LISTEN")
	}
	if err != nil {
		return 0, fmt.Errorf("resolve Knowledge listener: %w", err)
	}
	return parseServicePID(string(out), port, serviceGOOS)
}

func parseServicePID(output string, port int, goos string) (int, error) {
	pids := map[int]bool{}
	for line := range strings.SplitSeq(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		text := fields[0]
		if goos == "windows" {
			if len(fields) != 5 || fields[0] != "TCP" || fields[3] != "LISTENING" || !strings.HasSuffix(fields[1], ":"+strconv.Itoa(port)) {
				continue
			}
			text = fields[4]
		}
		pid, err := strconv.Atoi(text)
		if err != nil || pid <= 0 {
			return 0, errors.New("invalid Knowledge listener PID")
		}
		pids[pid] = true
	}
	if len(pids) != 1 {
		return 0, fmt.Errorf("expected one Knowledge listener on %d; found %d", port, len(pids))
	}
	for pid := range pids {
		return pid, nil
	}
	return 0, errors.New("missing listener")
}

func launchdServiceArgs(action, label, domain, target string) ([]string, error) {
	if action == "stop" {
		return []string{"bootout", target}, nil
	}
	if _, err := serviceCommand("launchctl", "print", target); err == nil {
		return []string{"kickstart", target}, nil
	}
	home, err := bootstrapHomeDir()
	if err != nil {
		return nil, err
	}
	return []string{"bootstrap", domain, filepath.Join(home, "Library", "LaunchAgents", label+".plist")}, nil
}

func resolveServiceCommand(name string) string {
	if name != "brew" {
		return name
	}
	if _, err := exec.LookPath(name); err == nil {
		return name
	}
	for _, candidate := range []string{"/opt/homebrew/bin/brew", "/usr/local/bin/brew"} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return name
}
