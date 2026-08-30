// SPDX-License-Identifier: Apache-2.0

// subcommands_test.go — unit coverage for the `knowledge version`
// subcommand surface: the runVersion client-version print, its dispatch
// through RunSubcommand, and the best-effort daemon-version probe.
//
// These tests pin behavior that needs NO live graph server or daemon: the
// client line always prints, the probe degrades to ("", false) when nothing
// is listening, and against an in-process h2c stub serving the documented MCP
// initialize shape the probe reads serverInfo.version back.

package bootstrap

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

// withArgs swaps os.Args for the test body and restores on cleanup.
// RunSubcommand reads os.Args directly, so we set the slice and assert the
// boolean return without spawning a subprocess.
func withArgs(t *testing.T, args []string) {
	t.Helper()
	prev := os.Args
	os.Args = args
	t.Cleanup(func() { os.Args = prev })
}

// captureStdout (install_test.go) is reused here to capture the version print.

// TestRunVersion pins that runVersion always prints `knowledge <Version>` on
// its own line and returns nil even with no daemon reachable (the http-port
// points nowhere, so the best-effort probe yields no server line).
func TestRunVersion(t *testing.T) {
	out := captureStdout(t, func() {
		// --http-port 1 is a privileged port nothing listens on in test, so
		// the probe fails fast and only the client line prints.
		err := runVersion([]string{"--http-port", "1"})
		assert.NoError(t, err)
	})
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	require.GreaterOrEqual(t, len(lines), 1, "expected at least the client version line, got %q", out)
	assert.True(t, strings.HasPrefix(lines[0], "knowledge "),
		"first line must begin with %q, got %q", "knowledge ", lines[0])
	assert.Contains(t, lines[0], Version,
		"client line must carry the build-time bootstrap.Version %q, got %q", Version, lines[0])
}

// TestRunSubcommand_Version pins that `knowledge version` routes through the
// RunSubcommand flat switch to runVersion and reports (handled=true,
// exitCode=0). Output is swallowed so the test log stays clean.
func TestRunSubcommand_Version(t *testing.T) {
	withArgs(t, []string{"knowledge", "version"})
	var handled bool
	var code int
	_ = captureStdout(t, func() { handled, code = RunSubcommand() })
	assert.True(t, handled, "`knowledge version` must be handled by RunSubcommand")
	assert.Equal(t, 0, code, "`knowledge version` must exit 0")
}

// TestProbeDaemonVersion covers both arms of the best-effort contract.
func TestProbeDaemonVersion(t *testing.T) {
	t.Run("nothing listening -> false", func(t *testing.T) {
		// Port 1 has nothing listening in test; the probe must return
		// ("", false) within the timeout, never panicking or blocking.
		v, ok := probeDaemonVersion(1)
		assert.False(t, ok, "probe against a dead port must report ok=false")
		assert.Empty(t, v, "probe against a dead port must return an empty version")
	})

	t.Run("stub serverInfo.version -> true", func(t *testing.T) {
		const wantVersion = "v1.2.3-stub"
		mux := http.NewServeMux()
		mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
			// Mirror handleHTTPInitialize's JSON-RPC result shape
			// (graphclient/mcp_http.go): result.serverInfo.version = h.version.
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-11-25","capabilities":{"tools":{}},"serverInfo":{"name":"knowledge","version":%q}}}`, wantVersion)
		})
		// h2c httptest server: the probe client speaks cleartext HTTP/2 only
		// (HTTP/1.1 off), exactly like the real daemon endpoint. Mirrors the
		// startCountingEngine idiom (router_e2e_test.go:156).
		srv := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
		t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })

		port := portFromURL(t, srv.URL)
		v, ok := probeDaemonVersion(port)
		assert.True(t, ok, "probe against the stub must report ok=true")
		assert.Equal(t, wantVersion, v, "probe must return serverInfo.version verbatim")
	})
}

// portFromURL extracts the numeric port from an http(s)://host:port URL.
func portFromURL(t *testing.T, raw string) int {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	p, err := strconv.Atoi(u.Port())
	require.NoError(t, err)
	return p
}
