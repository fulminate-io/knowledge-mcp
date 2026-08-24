// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestServerSpawnArgvPprofBothDirections pins the argv the client starts
// knowledge-server with, in BOTH directions.
//
// Only the pair is meaningful. The true direction alone passes against an
// unconditional append — which would open /debug/pprof/ on every installation's
// graph server — and the false direction alone passes against a flag that is
// never forwarded at all, which is the exact defect this change repairs.
func TestServerSpawnArgvPprofBothDirections(t *testing.T) {
	base := SpawnArgs{BinPath: "/app/knowledge-server", Port: 15022, Root: "/workspace", GraphStorage: "/data/.knowledge"}

	// --log-file is part of the DEFAULT argv, not an opt-in: the server tees that
	// file with its inherited stderr, and dropping the flag would silently retire
	// the durable half of the log.
	want := []string{
		"--port", "15022", "--root", "/workspace", "--graph-storage", "/data/.knowledge",
		"--log-file", "/data/.knowledge/server.log",
	}

	off := serverSpawnArgv(base)
	require.Equal(t, want, off, "the default spawn argv must stay exactly these four flags")
	require.NotContains(t, off, "--pprof")

	on := base
	on.Pprof = true
	require.Equal(t, append(slices.Clone(want), "--pprof"), serverSpawnArgv(on),
		"Pprof=true must append exactly one trailing --pprof and change nothing else")

	// Known-positive control for the NotContains above: the same containment
	// check finds a token the argv really carries, so the absence assertion means
	// "absent" rather than "the check never looked".
	require.Contains(t, off, "--graph-storage")
}

// TestPprofFlagDefaultIsOff guards the default that was flipped when the flag
// was made real. It was registered default-TRUE while inert — nothing read it,
// so nothing bound — and a wired flag that kept that default would start opening
// a profiling port on every daemon in the field.
func TestPprofFlagDefaultIsOff(t *testing.T) {
	fs := flag.NewFlagSet("knowledge serve", flag.ContinueOnError)
	var cfg Config
	registerConfigFlags(fs, &cfg)

	pprofFlag := fs.Lookup("pprof")
	require.NotNil(t, pprofFlag, "--pprof must still be registered")
	require.Equal(t, "false", pprofFlag.DefValue, "--pprof must default OFF now that it actually binds a port")

	// Control: DefValue on other flags in the SAME set reports their real
	// registered defaults, so the "false" above is a reading rather than a
	// constant this assertion would print for any flag.
	require.Equal(t, "~/.knowledge/", fs.Lookup("graph-storage").DefValue,
		"control: DefValue reflects the registered default, so the --pprof check discriminates")
	require.Equal(t, "info", fs.Lookup("log-level").DefValue)
}

// TestApplyPprofBindsOnlyWhenAsked drives the real applyPprof against a real
// port, in both directions and in that order.
//
// THE OFF CASE RUNS FIRST AND IS NOT DECORATION. profiling's server state is
// process-global and sticky (one bind per process), so an off-case checked after
// a successful bind could not distinguish "did not bind" from "already bound by
// the previous case". Run first, a refused dial means applyPprof genuinely did
// nothing; the on-case that follows is the known-positive proving the dial would
// have succeeded had anything been listening.
func TestApplyPprofBindsOnlyWhenAsked(t *testing.T) {
	port := freePort(t)

	applyPprof(&Config{Pprof: false, PprofPort: port})
	if resp, err := http.Get(pprofURL(port)); err == nil { //nolint:noctx // short local probe
		_ = resp.Body.Close()
		t.Fatalf("applyPprof(Pprof:false) left something listening on %d", port)
	}

	applyPprof(&Config{Pprof: true, PprofPort: port})

	var body []byte
	var status int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(pprofURL(port)) //nolint:noctx // short local probe
		if err == nil {
			status = resp.StatusCode
			body, _ = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	require.Equal(t, http.StatusOK, status,
		"applyPprof(Pprof:true) must serve /debug/pprof/heap on --pprof-port %d", port)
	require.NotEmpty(t, body, "the heap endpoint returned an empty body")

	// The port came from PprofPort, not the package default — so SetPort is
	// applied, not merely EnsureServer. A test on the default port could not tell
	// the two apart.
	require.NotEqual(t, 15021, port, "the probe port must differ from profiling.DefaultPort for this assertion to mean anything")
}

// pprofURL is the heap endpoint on a loopback port.
func pprofURL(port int) string {
	return fmt.Sprintf("http://127.0.0.1:%d/debug/pprof/heap?debug=1", port)
}

// freePort asks the kernel for an unused loopback port and releases it, so the
// probe above binds a port nothing else on the machine holds.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	return port
}
