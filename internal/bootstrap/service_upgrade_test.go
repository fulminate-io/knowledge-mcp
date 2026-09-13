// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// Uses the existing install fixture with explicitly supplied scratch binaries.
// No implicit executable path or host service is eligible for this live test.
func TestServiceUpgradeLive(t *testing.T) {
	clientSource, serverSource := os.Getenv("DESKTOP_CLIENT_BINARY"), os.Getenv("DESKTOP_SERVER_BINARY")
	if clientSource == "" || serverSource == "" {
		t.Skip("set explicit DESKTOP_CLIENT_BINARY and DESKTOP_SERVER_BINARY for live upgrade fixture")
	}
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	payloads := map[string][]byte{}
	for name, source := range map[string]string{"knowledge": clientSource, "knowledge-server": serverSource} {
		body, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		payloads[name] = body
		if err := os.WriteFile(filepath.Join(bin, serviceBinaryName(name)), body, 0o700); err != nil { //nolint:gosec // Executable fixture must be runnable.
			t.Fatal(err)
		}
	}
	port := func() int {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := listener.Addr().(*net.TCPAddr).Port
		if err := listener.Close(); err != nil {
			t.Fatal(err)
		}
		return port
	}
	f := serviceOptions{stateDir: filepath.Join(root, "data"), root: root, port: port(), httpPort: port(), clientBinary: filepath.Join(bin, serviceBinaryName("knowledge")), serverBinary: filepath.Join(bin, serviceBinaryName("knowledge-server"))}
	f.configFile = filepath.Join(f.stateDir, "config")
	t.Cleanup(func() {
		if err := stopService(f); err != nil {
			t.Error(err)
		}
	})
	if err := startService(f); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(f.configFile)
	if err != nil {
		t.Fatal(err)
	}
	archive := func(name string) []byte {
		if runtime.GOOS == "windows" {
			return buildZip(t, map[string][]byte{serviceBinaryName(name): payloads[name]})
		}
		return buildTarGz(t, map[string][]byte{name: payloads[name]})
	}
	clientAsset, serverAsset := assetName(runtime.GOOS, runtime.GOARCH, "knowledge"), assetName(runtime.GOOS, runtime.GOARCH, "knowledge-server")
	clientArchive, serverArchive := archive("knowledge"), archive("knowledge-server")
	fixture := releaseStub{tag: "v0.1.0-desktop", assetName: serverAsset, archive: serverArchive, clientAssetName: clientAsset, clientArchive: clientArchive, checksums: makeChecksums(map[string][]byte{clientAsset: clientArchive, serverAsset: serverArchive})}
	server := newReleaseServer(t, fixture)
	pointHTTPClientAt(t, server)
	withVersion(t, "v0.1.0-desktop")
	if err := upgradeService(f); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(f.configFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("upgrade changed config")
	}
	status, err := inspectService(f)
	if err != nil || status.State != "running" || status.ClientVersion != "v0.1.0-desktop" {
		t.Fatalf("%+v %v", status, err)
	}
	t.Log("Verified release download, staging, replacement, restart, MCP health, and identical config in", root)
}

func TestServiceExplicitStateArguments(t *testing.T) {
	root := t.TempDir()
	f := serviceOptions{stateDir: root, configFile: filepath.Join(root, "custom-config")}
	for _, kind := range []string{"client", "server"} {
		got, err := serviceStateArgs(f, kind, []string{"existing"}, true)
		want := []string{"existing", "--state-dir", root, "--config-file", f.configFile}
		if kind == "server" {
			want = []string{"existing", "--machine-id-cache", filepath.Join(root, "machine-id"), "--pid-file", filepath.Join(root, "knowledge.pid")}
		}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("%s argv = %v %v", kind, got, err)
		}
		if _, err := serviceStateArgs(f, kind, nil, false); err == nil || !strings.Contains(err.Error(), "lacks explicit state paths") {
			t.Fatalf("%s unsupported paths: %v", kind, err)
		}
	}
}

func TestServiceStatusOmitsUnknownServerVersion(t *testing.T) {
	raw, err := json.Marshal(serviceStatus{ClientVersion: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["serverVersion"]; ok {
		t.Fatal("unimplemented version emitted")
	}
	if fields["clientVersion"] != "v1" {
		t.Fatal("implemented version lost")
	}
}

func TestCommitServiceUpgradeWindowsRetainsLockedRecovery(t *testing.T) {
	root := t.TempDir()
	client := filepath.Join(root, "knowledge.exe")
	server := filepath.Join(root, "knowledge-server.exe")
	for _, path := range []string{client, server} {
		if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	oldRemove := removePreviousExecutable
	removePreviousExecutable = func(string) error { return errors.New("image is still mapped") }
	t.Cleanup(func() { removePreviousExecutable = oldRemove })
	var staged []stagedBinary
	for _, name := range []string{"knowledge-server", "knowledge"} {
		item, err := stageBinary(root, []byte("new-"+name), "windows", name)
		if err != nil {
			t.Fatal(err)
		}
		staged = append(staged, item)
	}
	defer discardStaged(staged)
	if err := commitServiceUpgrade(staged, "windows"); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{client: "new-knowledge", server: "new-knowledge-server"} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("replacement = %q %v", got, err)
		}
	}
	for _, target := range []string{server, client} {
		backups, err := filepath.Glob(target + ".previous-*")
		if err != nil || len(backups) != 1 {
			t.Fatalf("recovery copies for %s = %v %v", target, backups, err)
		}
		if got, err := os.ReadFile(backups[0]); err != nil || string(got) != "old" {
			t.Fatalf("recovery copy = %q %v", got, err)
		}
	}
}

func TestServiceServerRequiresIndependentPIDCapability(t *testing.T) {
	root := t.TempDir()
	f := serviceOptions{stateDir: root, root: root, configFile: filepath.Join(root, "config"), clientBinary: filepath.Join(root, "knowledge"), serverBinary: filepath.Join(root, "knowledge-server")}
	previous := serviceCommand
	t.Cleanup(func() { serviceCommand = previous })
	serviceCommand = func(name string, args ...string) ([]byte, error) {
		if name != f.serverBinary || !reflect.DeepEqual(args, []string{"--help"}) {
			t.Fatalf("unexpected capability command %s %v", name, args)
		}
		return []byte("-machine-id-cache"), nil
	}
	if err := startServicePart(f, "Knowledge session", "server"); err == nil || !strings.Contains(err.Error(), "lacks explicit state paths") {
		t.Fatalf("unsupported server reached launch: %v", err)
	}
}

func TestServiceBinariesResolveSibling(t *testing.T) {
	root := t.TempDir()
	client := filepath.Join(root, serviceBinaryName("knowledge"))
	server := filepath.Join(root, serviceBinaryName("knowledge-server"))
	if err := os.WriteFile(server, []byte("fixture"), 0700); err != nil { //nolint:gosec // Executable discovery fixture must be runnable; never launched.
		t.Fatal(err)
	}
	gotClient, gotServer, err := serviceBinaries(serviceOptions{clientBinary: client})
	if err != nil || gotClient != client || gotServer != server {
		t.Fatalf("sibling binaries = %q %q %v", gotClient, gotServer, err)
	}
}

func TestCommitStagedWindowsRecovery(t *testing.T) {
	for _, scenario := range []string{"fresh", "replace", "publish-failure", "directory"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			staged, err := stageBinary(root, []byte("new"), "windows", "knowledge-server")
			if err != nil {
				t.Fatal(err)
			}
			defer discardStaged([]stagedBinary{staged})
			if scenario == "directory" {
				if err := os.Mkdir(staged.finalPath, 0700); err != nil {
					t.Fatal(err)
				}
			} else if scenario != "fresh" {
				if err := os.WriteFile(staged.finalPath, []byte("old"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "publish-failure" {
				if err := os.Remove(staged.tmpPath); err != nil {
					t.Fatal(err)
				}
			}
			_, err = commitStaged(staged, "windows")
			backups, globErr := filepath.Glob(staged.finalPath + ".previous-*")
			if globErr != nil {
				t.Fatal(globErr)
			}
			switch scenario {
			case "publish-failure":
				if err == nil || len(backups) != 0 || !strings.Contains(err.Error(), "restored previous executable") {
					t.Fatalf("restoration not reported: %v %v", backups, err)
				}
				got, readErr := os.ReadFile(staged.finalPath)
				if readErr != nil || string(got) != "old" {
					t.Fatalf("old executable lost: %q %v", got, readErr)
				}
			case "directory":
				if err == nil || !strings.Contains(err.Error(), "not a regular file") || len(backups) != 0 {
					t.Fatalf("directory displaced: %v %v", backups, err)
				}
				info, statErr := os.Stat(staged.finalPath)
				if statErr != nil || !info.IsDir() {
					t.Fatalf("directory changed: %v", statErr)
				}
			default:
				if err != nil || len(backups) != 0 {
					t.Fatalf("successful unlocked install left recovery files: %v %v", backups, err)
				}
				got, readErr := os.ReadFile(staged.finalPath)
				if readErr != nil || string(got) != "new" {
					t.Fatalf("new executable missing: %q %v", got, readErr)
				}
			}
		})
	}
}

func TestCommitStagedWindowsRestoreFailure(t *testing.T) {
	root := t.TempDir()
	staged, err := stageBinary(root, []byte("new"), "windows", "knowledge-server")
	if err != nil {
		t.Fatal(err)
	}
	defer discardStaged([]stagedBinary{staged})
	if err := os.WriteFile(staged.finalPath, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	publishErr, restoreErr := errors.New("publish denied"), errors.New("restore denied")
	previousRename := renameStagedExecutable
	t.Cleanup(func() { renameStagedExecutable = previousRename })
	renameStagedExecutable = func(from, to string) error {
		if from == staged.tmpPath {
			return publishErr
		}
		return restoreErr
	}
	_, err = commitStaged(staged, "windows")
	backups, globErr := filepath.Glob(staged.finalPath + ".previous-*")
	if globErr != nil || len(backups) != 1 {
		t.Fatalf("missing recovery: %v %v", backups, globErr)
	}
	if !errors.Is(err, publishErr) || !errors.Is(err, restoreErr) || !strings.Contains(err.Error(), backups[0]) {
		t.Fatalf("lost replacement diagnostics: %v", err)
	}
	if got, readErr := os.ReadFile(backups[0]); readErr != nil || string(got) != "old" {
		t.Fatalf("recovery data lost: %q %v", got, readErr)
	}
}
