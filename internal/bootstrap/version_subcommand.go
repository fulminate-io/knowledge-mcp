// SPDX-License-Identifier: Apache-2.0

// version_subcommand.go — the `knowledge version` subcommand and its
// best-effort daemon-version probe. runVersion always prints the client
// binary version (the ldflags-injected bootstrap.Version), then makes a
// single best-effort MCP `initialize` round-trip to the running
// `knowledge serve` daemon and, when reachable, prints the daemon's
// reported serverInfo.version on a second `server <ver>` line. The probe
// never errors or blocks: an unreachable/slow daemon degrades to the
// client line alone.
//
// The bare `knowledge --version` / `-v` flag (handled in
// cmd/knowledge/main.go before ParseFlags) routes here via runVersion too,
// so the flag and the subcommand share one code path and one output shape.

package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// runVersion implements `knowledge version`. It prints `knowledge <version>`
// (the ldflags-injected client binary version, already published into
// bootstrap.Version by cmd/knowledge/main.go's init()) on its own line, then
// best-effort probes the running daemon and, on success, prints a second
// `server <version>` line, followed by a shared skew line when the two known
// stamps differ (renderVersionOutput). It NEVER returns a non-nil error or
// exits non-zero because the daemon is down — the SERVER and skew lines are
// purely additive.
//
// --http-port selects the loopback port the daemon's streamable-HTTP MCP
// endpoint (/mcp) listens on; it defaults to graphclient.DefaultMCPHTTPPort
// (the port that actually serves serverInfo.version — distinct from the
// graph-server --port). Unknown flags are tolerated (the bare `--version`/`-v`
// entry passes no args) so the command stays a no-friction one-shot.
// RunVersion is the exported entry point for the bare `knowledge --version` /
// `-v` flag path in cmd/knowledge/main.go (handled before ParseFlags). It
// delegates to runVersion so the flag and the `version` subcommand share one
// output shape — main.go must NOT duplicate the print logic.
func RunVersion(args []string) error { return runVersion(args) }

func runVersion(args []string) error {
	fs := flag.NewFlagSet("knowledge version", flag.ContinueOnError)
	port := fs.Int("http-port", graphclient.DefaultMCPHTTPPort, "Loopback TCP port the `knowledge serve` daemon binds its MCP endpoint (/mcp) on; probed for the running server's version")
	// Silence the FlagSet's own usage spew on a bad flag — the version
	// command is best-effort and must never derail on flag noise.
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		// A malformed flag is not worth failing a version print over: fall
		// back to the default port so the client line still emits.
		*port = graphclient.DefaultMCPHTTPPort
	}

	serverVersion, ok := probeDaemonVersion(*port)
	// The installed server binary is read off disk under its own bounded
	// context, so a corrupt or wrong-architecture binary cannot wedge a version
	// print; an unreadable one degrades to no line, exactly as an unreachable
	// daemon does.
	binCtx, cancel := context.WithTimeout(context.Background(), serverBinaryVersionBudget)
	defer cancel()
	serverBinVer, serverBinOK := serverBinaryVersion(binCtx)
	fmt.Fprint(os.Stdout, renderVersionOutput(Version, serverVersion, ok, serverBinVer, serverBinOK))
	return nil
}

// renderVersionOutput builds the `knowledge version` output body: a
// `knowledge <clientVer>` line, a `server <daemonVer>` line (only when the
// daemon probe succeeded), and — when both stamps are known and differ — the
// shared graphclient.VersionSkewLine (the SAME skew source manage(status) uses,
// so the two surfaces cannot drift). Pure and side-effect-free so the skew
// formatting is unit-testable without a live daemon; runVersion keeps the
// probe + os.Stdout write and delegates all formatting here.
func renderVersionOutput(clientVer, daemonVer string, daemonKnown bool, serverBinVer string, serverBinKnown bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "knowledge %s\n", clientVer)
	if daemonKnown {
		fmt.Fprintf(&b, "server %s\n", daemonVer)
	}
	// The INSTALLED server binary, distinct from the RUNNING daemon above: one
	// is a file on disk, the other a live process, and they diverge for
	// different reasons with different remedies.
	if serverBinKnown {
		fmt.Fprintf(&b, "server binary %s\n", serverBinVer)
	}
	if line, skewed := graphclient.VersionSkewLine(clientVer, daemonVer); skewed {
		fmt.Fprintf(&b, "%s\n", line)
	}
	if line, skewed := graphclient.ServerBinarySkewLine(clientVer, serverBinVer); skewed {
		fmt.Fprintf(&b, "%s\n", line)
	}
	// The gateway's version verdict and this client's own possession proof,
	// appended by a separate renderer so the skew line above stays untouched.
	// Contributes nothing at all when neither is set.
	b.WriteString(renderClientVersionState())
	return b.String()
}

// probeDaemonVersion best-effort reads the running `knowledge serve` daemon's
// version from its MCP serverInfo. It POSTs a minimal MCP `initialize`
// JSON-RPC request to http://127.0.0.1:<port>/mcp over h2c (cleartext HTTP/2,
// the daemon's transport — see graphclient/mcp_http.go) with a short context
// timeout, decodes the JSON-RPC result, and returns
// (result.serverInfo.version, true) on success.
//
// It returns ("", false) on ANY failure — connection refused, timeout,
// non-200, malformed JSON, or a missing serverInfo.version field — and never
// panics or blocks beyond the timeout. This is the best-effort contract: a
// version print must not depend on a live daemon. The 2s budget mirrors
// checkServer (doctor_checks.go).
func probeDaemonVersion(port int) (string, bool) {
	return probeDaemonVersionWithin(port, versionProbeBudget)
}

// versionProbeBudget is the wall-clock ceiling probeDaemonVersion gives the
// whole round trip. Named rather than inlined so the value the shipped path
// uses is one readable constant, and so a test can pin it independently of the
// tests that drive the probe on a shortened budget.
const versionProbeBudget = 2 * time.Second

// probeDaemonVersionWithin is probeDaemonVersion with the round-trip budget
// supplied by the caller. The budget is a parameter ONLY so tests can drive the
// deadline-expires-mid-request path in milliseconds instead of seconds; every
// shipped caller goes through probeDaemonVersion and gets versionProbeBudget.
func probeDaemonVersionWithin(port int, budget time.Duration) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	// Minimal MCP initialize request — exactly the shape handleHTTPInitialize
	// (graphclient/mcp_http.go) answers, returning serverInfo.version =
	// h.version.
	reqBody := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`)
	url := fmt.Sprintf("http://127.0.0.1:%d/mcp", port)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return "", false
	}
	httpReq.Header.Set("Content-Type", "application/json")

	// The probe client is single-use, so the connection it dials has no second
	// caller and no owner but this call. Releasing it here keeps the h2 read
	// loop (and the peer's serve goroutines) from outliving the one request they
	// were opened for.
	probeClient, releaseProbeConns := newVersionProbeClient()
	defer releaseProbeConns()

	resp, err := probeClient.Do(httpReq)
	if err != nil {
		return "", false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", false
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", false
	}

	// Decode just enough of the JSON-RPC envelope to reach
	// result.serverInfo.version (mcp_http.go:349).
	var decoded struct {
		Result struct {
			ServerInfo struct {
				Version string `json:"version"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", false
	}
	if decoded.Result.ServerInfo.Version == "" {
		return "", false
	}
	return decoded.Result.ServerInfo.Version, true
}

// newVersionProbeClient builds the h2c (cleartext HTTP/2) client the daemon's
// /mcp endpoint requires, and the release func that tears its connections down.
// Mirrors the stdlib-only transport shape of the bench harness's
// newWireHTTPClient (cmd/server-bench/internal/bench/spawn_oss.go): HTTP/1.1
// OFF, unencrypted HTTP/2 ON, so the request reaches the daemon's
// h2c.NewHandler. No global client timeout — the per-call context deadline in
// probeDaemonVersionWithin bounds the request.
//
// WHY RELEASE OWNS THE DIALED CONNECTIONS instead of calling
// CloseIdleConnections. A pool-level release only reaches connections the pool
// considers IDLE. When the probe's deadline fires while the request is still in
// flight — a slow or loaded daemon, which is precisely when the probe times out
// — the connection still carries the aborted stream at release time, so the
// pool skips it; and because this transport sets no IdleConnTimeout, nothing
// reaps it afterwards either. The connection, its read loop and the peer's
// serve goroutines then live for the rest of the process. Holding the net.Conn
// the transport dialed makes teardown unconditional, and unconditional is
// correct here: the client serves exactly one request and is discarded.
func newVersionProbeClient() (client *http.Client, release func()) {
	var (
		mu     sync.Mutex
		dialed []net.Conn
	)

	t := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			var d net.Dialer
			conn, err := d.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			mu.Lock()
			dialed = append(dialed, conn)
			mu.Unlock()
			return conn, nil
		},
	}
	t.Protocols = new(http.Protocols)
	t.Protocols.SetHTTP1(false)
	t.Protocols.SetUnencryptedHTTP2(true)

	return &http.Client{Transport: t}, func() {
		mu.Lock()
		defer mu.Unlock()
		for _, conn := range dialed {
			_ = conn.Close()
		}
		dialed = nil
	}
}
