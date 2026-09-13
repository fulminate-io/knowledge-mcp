// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// serviceOptions addresses an existing installation. No service command changes
// login/boot policy; setup remains the owner of persistence-unit installation.
type serviceOptions struct {
	stateDir, configFile, root string
	clientBinary, serverBinary string
	port, httpPort             int
}

type serviceStatus struct {
	State         string `json:"state"`
	Owner         string `json:"owner"`
	ClientVersion string `json:"clientVersion"`
	ServerVersion string `json:"serverVersion,omitempty"`
	Endpoint      string `json:"endpoint"`
	Reason        string `json:"reason"`
	ClientState   string `json:"clientState"`
	ServerState   string `json:"serverState"`
}

func parseServiceOptions(args []string) (serviceOptions, error) {
	var f serviceOptions
	fs := flag.NewFlagSet("knowledge service", flag.ContinueOnError)
	fs.StringVar(&f.clientBinary, "client-binary", "", "Installed Knowledge executable to manage (default this executable)")
	fs.StringVar(&f.serverBinary, "server-binary", "", "Installed backend executable to manage (default sibling)")
	fs.StringVar(&f.stateDir, "state-dir", "", "Knowledge state and graph directory (default ~/.knowledge)")
	fs.StringVar(&f.configFile, "config-file", "", "Configuration file (default <state-dir>/config)")
	fs.StringVar(&f.root, "root", "", "Project root (default state directory)")
	fs.IntVar(&f.port, "port", graphclient.DefaultPort, "Local graph-server port")
	fs.IntVar(&f.httpPort, "http-port", graphclient.DefaultMCPHTTPPort, "Knowledge MCP HTTP port")
	if err := fs.Parse(args); err != nil {
		return f, err
	}
	if fs.NArg() != 0 {
		return f, fmt.Errorf("unexpected service arguments: %v", fs.Args())
	}
	if f.port < 1024 || f.port > 65535 || f.httpPort < 1024 || f.httpPort > 65535 || f.port == f.httpPort {
		return f, errors.New("service ports must be distinct numbers between 1024 and 65535")
	}
	if f.stateDir == "" {
		var err error
		f.stateDir, err = serviceGraphStorage()
		if err != nil {
			return f, err
		}
	}
	if f.configFile == "" {
		f.configFile = filepath.Join(f.stateDir, "config")
	}
	if f.root == "" {
		f.root = f.stateDir
	}
	for _, p := range []string{f.stateDir, f.configFile, f.root} {
		if !filepath.IsAbs(p) {
			return f, fmt.Errorf("service paths must be absolute: %s", p)
		}
	}
	for _, binary := range []string{f.clientBinary, f.serverBinary} {
		if binary != "" && !filepath.IsAbs(binary) {
			return f, fmt.Errorf("service binary path must be absolute: %s", binary)
		}
	}
	return f, nil
}

func runService(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: knowledge service <status|start|stop|restart|upgrade> [flags]")
	}
	verb := args[0]
	switch verb {
	case "status", "start", "stop", "restart", "upgrade":
	default:
		return fmt.Errorf("unknown service operation %q", verb)
	}
	f, err := parseServiceOptions(args[1:])
	if err != nil {
		return err
	}
	if verb == "status" {
		s, err := inspectService(f)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(s)
	}
	if verb == "upgrade" {
		return upgradeService(f)
	}
	if verb == "stop" || verb == "restart" {
		if err := stopService(f); err != nil {
			return err
		}
	}
	if verb == "start" || verb == "restart" {
		if err := startService(f); err != nil {
			return err
		}
	}
	fmt.Fprintf(os.Stdout, "Knowledge service %s completed\n", verb)
	return nil
}

func serviceListening(port int) (bool, error) {
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 300*time.Millisecond)
	if err != nil {
		if serviceConnectionRefused(err) {
			return false, nil
		}
		return false, fmt.Errorf("inspect listener on port %d: %w", port, err)
	}
	_ = c.Close()
	return true, nil
}

func inspectService(f serviceOptions) (serviceStatus, error) {
	owner, err := serviceManager(f)
	if err != nil {
		return serviceStatus{}, err
	}
	s := serviceStatus{State: "stopped", Owner: owner, Endpoint: fmt.Sprintf("http://127.0.0.1:%d/mcp", f.httpPort), ClientState: "stopped", ServerState: "stopped", Reason: "Start Knowledge to make MCP available."}
	clientListening, err := serviceListening(f.httpPort)
	if err != nil {
		return s, err
	}
	if clientListening {
		s.ClientState = "unreachable"
		if version, ok := probeServiceClient(f.httpPort); ok {
			s.ClientVersion = version
			s.ClientState = "running"
		}
	}
	serverListening, err := serviceListening(f.port)
	if err != nil {
		return s, err
	}
	if serverListening {
		s.ServerState = "unreachable"
		if _, err := serviceBackendStatus(f.port); err == nil {
			s.ServerState = "running"
			// The existing backend wire does not expose its running version.
		}
	}
	if s.ClientState == "running" && s.ServerState == "running" {
		s.State = "running"
		s.Reason = "Knowledge runs independently of Fulminate Desktop."
	} else if s.ClientState != "stopped" || s.ServerState != "stopped" {
		s.State = "error"
		s.Reason = "Knowledge is partially running or an endpoint cannot be verified. Check client.log and server.log, then retry."
	}
	return s, nil
}

func serviceConnectionRefused(err error) bool {
	// Winsock returns WSAECONNREFUSED (10061), not Go's Windows
	// syscall.ECONNREFUSED compatibility value. Keep the check typed and OS-scoped.
	const wsaConnectionRefused syscall.Errno = 10061
	return errors.Is(err, syscall.ECONNREFUSED) ||
		(serviceGOOS == "windows" && errors.Is(err, wsaConnectionRefused))
}
