// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

var serviceBackendStatus = func(port int) (map[string]any, error) {
	// routing: local by design — service lifecycle observes the addressed process.
	return graphclient.NewGraphClient(port).Status()
}

// Validate the product identity, not just a version-shaped response on a port.
func probeServiceClient(port int) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	client, release := newVersionProbeClient()
	defer release()
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/mcp", port), bytes.NewReader(body))
	if err != nil {
		return "", false
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	var reply struct {
		Result struct {
			ServerInfo struct{ Name, Version string }
		}
	}
	// MCP initialize replies permit unrelated protocol and capability fields.
	// Decode only server identity here without rejecting those additional fields.
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&reply) != nil {
		return "", false
	}
	return reply.Result.ServerInfo.Version, reply.Result.ServerInfo.Name == "knowledge" && reply.Result.ServerInfo.Version != ""
}

func startService(f serviceOptions) error {
	manager, err := serviceManager(f)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(f.stateDir, 0o750); err != nil {
		return err
	}
	if err := os.MkdirAll(f.root, 0o750); err != nil {
		return err
	}
	// An account-free starter is created only when absent; existing configuration
	// and credentials remain byte-for-byte unchanged.
	file, err := os.OpenFile(f.configFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		_, err = file.WriteString("[credentials]\n")
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	} else if !os.IsExist(err) {
		return err
	}
	for _, kind := range []string{"server", "client"} {
		port := f.port
		if kind == "client" {
			port = f.httpPort
		}
		listening, err := serviceListening(port)
		if err != nil {
			return err
		}
		if listening {
			if !serviceHealthy(kind, port) {
				return fmt.Errorf("%s port %d is occupied by an unverified endpoint", kind, port)
			}
			continue
		}
		if err := startServicePart(f, manager, kind); err != nil {
			return err
		}
		if !waitForReady(func() bool { return serviceHealthy(kind, port) }, restartReadinessTimeout) {
			return fmt.Errorf("%s did not become ready; inspect %s", kind, filepath.Join(f.stateDir, kind+".log"))
		}
	}
	return nil
}

func serviceHealthy(kind string, port int) bool {
	if kind == "client" {
		_, ok := probeServiceClient(port)
		return ok
	}
	_, err := serviceBackendStatus(port)
	return err == nil
}

func serviceBinaries(f serviceOptions) (client, server string, err error) {
	client = f.clientBinary
	if client == "" {
		client, err = getExecutable()
		if err != nil {
			return
		}
	}
	server = f.serverBinary
	if server == "" {
		var found bool
		server, found = lookupSibling(client, nil, serviceBinaryName("knowledge-server"))
		if !found {
			server, err = findServerBinary()
		}
	}
	return
}
func serviceBinaryName(name string) string {
	if serviceGOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func startServicePart(f serviceOptions, manager, kind string) error {
	if manager != "Knowledge session" && (manager != "Homebrew" || kind != "server") {
		return serviceUnitAction(manager, "start", kind)
	}
	client, server, err := serviceBinaries(f)
	if err != nil {
		return err
	}
	executable := server
	args := []string{"--port", strconv.Itoa(f.port), "--root", f.root, "--graph-storage", f.stateDir, "--log-file", filepath.Join(f.stateDir, kind+".log")}
	if kind == "client" {
		executable = client
		args = append([]string{"serve"}, args...)
		args = append(args, "--http-port", strconv.Itoa(f.httpPort), "--headless", "--no-auto-update")
	}
	// Older released runtimes remain manageable by a newer CLI. Explicit state
	// flags are required for alternate installations, never silently omitted there.
	helpArgs := []string{"--help"}
	flagName := "-machine-id-cache"
	if kind == "client" {
		helpArgs = []string{"serve", "--help"}
		flagName = "-state-dir"
	}
	output, helpErr := serviceCommand(executable, helpArgs...)
	if helpErr != nil && len(output) == 0 {
		return fmt.Errorf("inspect installed %s capabilities: %w", kind, helpErr)
	}
	explicit := strings.Contains(string(output), flagName)
	if kind == "server" {
		explicit = explicit && strings.Contains(string(output), "-pid-file")
	}
	args, err = serviceStateArgs(f, kind, args, explicit)
	if err != nil {
		return err
	}

	return launchServiceProcess(executable, args, f.root)
}

func launchServiceProcess(executable string, args []string, root string) (err error) {
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, null.Close()) }()
	cmd := exec.Command(executable, args...) //nolint:gosec // Executable resolved locally; no shell, validated lifecycle arguments.
	cmd.Dir = root
	cmd.Stdin = null
	cmd.Stdout = null
	cmd.Stderr = null
	configureServiceProcess(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start Knowledge: %w", err)
	}
	return cmd.Process.Release()
}

func stopService(f serviceOptions) error {
	manager, err := serviceManager(f)
	if err != nil {
		return err
	}
	for _, kind := range []string{"client", "server"} {
		port := f.port
		if kind == "client" {
			port = f.httpPort
		}
		listening, err := serviceListening(port)
		if err != nil {
			return err
		}
		if !listening {
			continue
		}
		if !serviceHealthy(kind, port) {
			return fmt.Errorf("refusing to stop unverified %s endpoint on port %d", kind, port)
		}
		if manager != "Knowledge session" {
			err = serviceUnitAction(manager, "stop", kind)
		} else if kind == "server" {
			err = runStop([]string{"--port", strconv.Itoa(f.port), "--graph-storage", f.stateDir, "--root", f.root})
		} else {
			var pid int
			pid, err = serviceListenerPID(port)
			if err == nil {
				err = stopServiceProcess(pid)
			}
		}
		if err != nil {
			return fmt.Errorf("stop %s: %w", kind, err)
		}
		var probeErr error
		if !waitForReady(func() bool { listening, err := serviceListening(port); probeErr = err; return err == nil && !listening }, restartReadinessTimeout) {
			if probeErr != nil {
				return probeErr
			}
			return fmt.Errorf("%s still listening on port %d after stop", kind, port)
		}
	}
	return nil
}

func serviceStateArgs(f serviceOptions, kind string, args []string, explicit bool) ([]string, error) {
	if explicit {
		if kind == "client" {
			return append(args, "--state-dir", f.stateDir, "--config-file", f.configFile), nil
		}
		return append(args, "--machine-id-cache", filepath.Join(f.stateDir, "machine-id"), "--pid-file", filepath.Join(f.stateDir, "knowledge.pid")), nil
	}
	home, err := bootstrapHomeDir()
	if err != nil {
		return nil, err
	}
	if f.stateDir != filepath.Join(home, ".knowledge") || f.configFile != filepath.Join(f.stateDir, "config") {
		return nil, fmt.Errorf("installed %s lacks explicit state paths; upgrade Knowledge before starting this alternate installation", kind)
	}
	return args, nil
}
