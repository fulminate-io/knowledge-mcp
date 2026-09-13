// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
)

func TestServiceOptionsRejectBadInput(t *testing.T) {
	root := t.TempDir()
	for _, args := range [][]string{{"--state-dir", root, "--port", "0"}, {"--state-dir", root, "--http-port", "15022"}, {"--state-dir", "relative"}, {"--state-dir", root, "unexpected"}, {"--state-dir", root, "--unknown"}} {
		if _, err := parseServiceOptions(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	f, err := parseServiceOptions([]string{"--state-dir", root, "--port", "25122", "--http-port", "25123"})
	if err != nil {
		t.Fatal(err)
	}
	if f.configFile != filepath.Join(root, "config") || f.root != root {
		t.Fatalf("paths %+v", f)
	}
	if err := runService([]string{"execute"}); err == nil {
		t.Fatal("unknown operation accepted")
	}
}

func TestServiceListenerPIDPlatforms(t *testing.T) {
	for _, tc := range []struct {
		os, out string
		want    int
		bad     bool
	}{
		{"darwin", "123\n123\n", 123, false}, {"linux", "321\n", 321, false},
		{"windows", "  TCP    127.0.0.1:25123    0.0.0.0:0    LISTENING    42\n  TCP    127.0.0.1:25124    0.0.0.0:0    LISTENING    43", 42, false},
		{"windows", "TCP 127.0.0.1:25123 127.0.0.1:90 ESTABLISHED 42", 0, true},
		{"linux", "123\n456\n", 0, true}, {"linux", "", 0, true}, {"linux", "oops", 0, true},
	} {
		pid, err := parseServicePID(tc.out, 25123, tc.os)
		if (err != nil) != tc.bad || pid != tc.want {
			t.Fatalf("%+v got %d %v", tc, pid, err)
		}
	}
}

func TestServiceAdoptsExistingUnitsWithoutRewriting(t *testing.T) {
	home := t.TempDir()
	old := bootstrapHomeDir
	bootstrapHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { bootstrapHomeDir = old })
	oldOS := serviceGOOS
	serviceGOOS = "darwin"
	t.Cleanup(func() { serviceGOOS = oldOS })
	f := serviceOptions{stateDir: filepath.Join(home, ".knowledge"), port: 15022, httpPort: 15023}
	if got, err := serviceManager(f); err != nil || got != "Knowledge session" {
		t.Fatalf("%s %v", got, err)
	}
	dir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{launchdDaemonLabel, launchdServerLabel} {
		if err := os.WriteFile(filepath.Join(dir, label+".plist"), []byte("existing user configuration"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := serviceManager(f); err != nil || got != "launchd" {
		t.Fatalf("%s %v", got, err)
	}
	f.port = 25122
	f.httpPort = 25123
	if got, err := serviceManager(f); err != nil || got != "Knowledge session" {
		t.Fatalf("alternate installation consulted host units: %s %v", got, err)
	}
}

func TestServiceUnitsUseExistingManagersAndSurfaceFailure(t *testing.T) {
	old := serviceCommand
	t.Cleanup(func() { serviceCommand = old })
	var calls [][]string
	serviceCommand = func(name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		return nil, nil
	}
	if err := serviceUnitAction("systemd", "stop", "client"); err != nil {
		t.Fatal(err)
	}
	if err := serviceUnitAction("systemd", "start", "server"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, [][]string{{"systemctl", "--user", "stop", "knowledge.service"}, {"systemctl", "--user", "start", "knowledge-server.service"}}) {
		t.Fatal(calls)
	}
	serviceCommand = func(string, ...string) ([]byte, error) { return []byte("permission denied"), errors.New("exit 1") }
	if err := serviceUnitAction("systemd", "start", "client"); err == nil {
		t.Fatal("failed service command accepted")
	}
}

func TestServiceConnectionRefused(t *testing.T) {
	old := serviceGOOS
	t.Cleanup(func() { serviceGOOS = old })
	for _, platform := range []string{"windows", "linux", "darwin"} {
		t.Run(platform, func(t *testing.T) {
			serviceGOOS = platform
			for _, tc := range []struct {
				name string
				err  error
				want bool
			}{
				{"native refusal", syscall.ECONNREFUSED, true},
				{"Winsock refusal", syscall.Errno(10061), platform == "windows"},
				{"Winsock reset", syscall.Errno(10054), false},
				{"Winsock timeout", syscall.Errno(10060), false},
				{"permission denied", syscall.EACCES, false},
				{"refusal text without errno", errors.New("actively refused it"), false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					err := &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connectex", Err: tc.err}}
					if got := serviceConnectionRefused(err); got != tc.want {
						t.Fatalf("refused = %v, want %v: %v", got, tc.want, err)
					}
				})
			}
		})
	}
}
